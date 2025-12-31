/*
Copyright © 2025 ALESSIO TONIOLO

cluster.go contains cluster management and load balancing logic
*/
package serve

import (
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/atoniolo76/gotoni/pkg/db"
	"github.com/atoniolo76/gotoni/pkg/remote"
)

type Cluster struct {
	Instances []remote.RunningInstance
}

var latencyMap = map[string]int{
	"us-us":     0,
	"us-eu":     102,
	"us-asia":   124,
	"us-sa":     137,
	"eu-us":     102,
	"eu-eu":     0,
	"eu-asia":   222,
	"eu-sa":     200,
	"asia-us":   124,
	"asia-eu":   222,
	"asia-asia": 0,
	"asia-sa":   256,
	"sa-us":     137,
	"sa-eu":     200,
	"sa-asia":   257,
	"sa-sa":     0,
}

func LaunchCluster(httpClient *http.Client, apiToken string) *Cluster {
	provider, _ := remote.GetCloudProvider()

	// Get available instance types from the cloud provider
	availableInstances, err := provider.GetAvailableInstanceTypes(httpClient, apiToken)
	if err != nil {
		// Handle error - for now just return empty cluster
		log.Printf("Failed to get available instance types: %v", err)
		return &Cluster{}
	}

	// Target regions: us-west, us-east, us-south
	targetRegions := []string{"us-west-1", "us-east-1", "us-south-1"}
	cluster := &Cluster{}

	// Launch one instance per region using the first available instance type for each region
	for i, targetRegion := range targetRegions {
		// Find the first instance type that has capacity in this region
		for _, instanceType := range availableInstances {
			// Check if this instance is available in the target region
			for _, region := range instanceType.RegionsWithCapacityAvailable {
				if region.Name == targetRegion {
					// Launch one instance of this type in this region
					copyRegion := targetRegion
					targetRegions[i] = ""
					go func(region string, instType remote.Instance) {
						_, err := provider.LaunchInstance(httpClient, apiToken, instType.InstanceType.Name, region, 1, "gotoni-cluster", "", "")
						if err != nil {
							log.Printf("Failed to launch instance in %s: %v", region, err)
							return
						}

						// Note: In practice, you'd wait for instances to be ready and get full details
						// For now, we'll need to get running instances separately
						log.Printf("Launched instances in %s, will populate cluster when instances are ready", region)
					}(copyRegion, instanceType)
					break
				}
			}
		}
	}

	return cluster
}

// ClusterManager provides simplified cluster-wide operations
type ClusterManager struct {
	cluster   *Cluster
	sshMgr    *remote.SSHClientManager
	database  *db.DB
	mu        sync.RWMutex
	connected bool
}

// NewClusterManager creates a new cluster manager
func NewClusterManager(cluster *Cluster) *ClusterManager {
	database, err := db.InitDB()
	if err != nil {
		log.Printf("Failed to initialize database: %v", err)
		return nil
	}

	return &ClusterManager{
		cluster:  cluster,
		sshMgr:   remote.NewSSHClientManager(),
		database: database,
	}
}

// ConnectToCluster establishes SSH connections to all instances in the cluster
func (cm *ClusterManager) ConnectToCluster() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.connected {
		return fmt.Errorf("already connected to cluster")
	}

	for _, instance := range cm.cluster.Instances {
		if len(instance.SSHKeyNames) == 0 {
			log.Printf("Warning: instance %s has no SSH keys", instance.ID)
			continue
		}

		// Use the first SSH key (assuming there's typically one primary key)
		sshKeyName := instance.SSHKeyNames[0]

		// Get the SSH key path from database
		sshKey, err := cm.database.GetSSHKey(sshKeyName)
		if err != nil {
			log.Printf("Failed to get SSH key %s for instance %s: %v", sshKeyName, instance.ID, err)
			continue
		}

		// Connect to the instance
		err = cm.sshMgr.ConnectToInstance(instance.IP, sshKey.PrivateKey)
		if err != nil {
			log.Printf("Failed to connect to instance %s (%s): %v", instance.ID, instance.IP, err)
			continue
		}

		log.Printf("Connected to instance %s (%s)", instance.ID, instance.IP)
	}

	cm.connected = true
	return nil
}

// ExecuteOnCluster runs a command on all connected instances
func (cm *ClusterManager) ExecuteOnCluster(command string) map[string]*remote.ClusterCommandResult {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	results := make(map[string]*remote.ClusterCommandResult)

	for _, instance := range cm.cluster.Instances {
		result := &remote.ClusterCommandResult{
			InstanceID: instance.ID,
			InstanceIP: instance.IP,
		}

		output, err := cm.sshMgr.ExecuteCommand(instance.IP, command)
		if err != nil {
			result.Error = err
			log.Printf("Command failed on %s: %v", instance.ID, err)
		} else {
			result.Output = output
			log.Printf("Command succeeded on %s", instance.ID)
		}

		results[instance.ID] = result
	}

	return results
}

// ExecuteOnClusterAsync runs a command on all instances asynchronously
func (cm *ClusterManager) ExecuteOnClusterAsync(command string) chan map[string]*remote.ClusterCommandResult {
	resultChan := make(chan map[string]*remote.ClusterCommandResult, 1)

	go func() {
		results := cm.ExecuteOnCluster(command)
		resultChan <- results
	}()

	return resultChan
}

// StartServiceOnCluster creates and starts a systemd service on all cluster instances
func (cm *ClusterManager) StartServiceOnCluster(serviceName, command, workingDir string, envVars map[string]string) map[string]error {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	results := make(map[string]error)

	// Create a task object for service configuration
	task := &remote.Task{
		Name:       serviceName,
		Command:    command,
		Background: true,
		WorkingDir: workingDir,
		Restart:    "always",
	}

	for _, instance := range cm.cluster.Instances {
		err := cm.sshMgr.CreateSystemdService(instance.IP, serviceName, command, workingDir, envVars, task)
		if err != nil {
			results[instance.ID] = fmt.Errorf("failed to create service: %w", err)
			continue
		}

		err = cm.sshMgr.StartSystemdService(instance.IP, serviceName)
		if err != nil {
			results[instance.ID] = fmt.Errorf("failed to start service: %w", err)
			continue
		}

		log.Printf("Started service %s on instance %s", serviceName, instance.ID)
	}

	return results
}

// StopServiceOnCluster stops a systemd service on all cluster instances
func (cm *ClusterManager) StopServiceOnCluster(serviceName string) map[string]error {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	results := make(map[string]error)

	for _, instance := range cm.cluster.Instances {
		err := cm.sshMgr.StopSystemdService(instance.IP, serviceName)
		if err != nil {
			results[instance.ID] = err
			continue
		}

		log.Printf("Stopped service %s on instance %s", serviceName, instance.ID)
	}

	return results
}

// DisconnectFromCluster closes all SSH connections
func (cm *ClusterManager) DisconnectFromCluster() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.sshMgr.CloseAllConnections()
	cm.connected = false
	log.Println("Disconnected from all cluster instances")
}
