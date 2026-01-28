/*
Copyright © 2025 ALESSIO TONIOLO

cluster.go contains cluster management and load balancing logic
*/
package serve

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/atoniolo76/gotoni/pkg/db"
	"github.com/atoniolo76/gotoni/pkg/remote"
)

// InstanceType represents the specification of an instance type
// Borrowed from pkg/remote/client.go for local use
type InstanceType struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	GPUDescription    string `json:"gpu_description"`
	PriceCentsPerHour int    `json:"price_cents_per_hour"`
	Specs             struct {
		VCPUs      int `json:"vcpus"`
		MemoryGib  int `json:"memory_gib"`
		StorageGib int `json:"storage_gib"`
		GPUs       int `json:"gpus"`
	} `json:"specs"`
}

type Cluster struct {
	ID        int64
	Name      string
	Instances []remote.RunningInstance
	sshMgr    *remote.SSHClientManager
	database  *db.DB
	mu        sync.RWMutex
	connected bool
}

// ClusterReplicaSpec defines the configuration for a single replica in the cluster
type ClusterReplicaSpec struct {
	InstanceType string `json:"instance_type"`  // Instance type name (e.g., "gpu_1x_a100_sxm4")
	Region       string `json:"region"`         // Region name (e.g., "us-east-1")
	Quantity     int    `json:"quantity"`       // Number of instances of this type to launch
	Name         string `json:"name,omitempty"` // Optional name prefix for instances
}

// ClusterSpec defines the complete cluster configuration
type ClusterSpec struct {
	Name           string               `json:"name"`                      // Cluster name
	Replicas       []ClusterReplicaSpec `json:"replicas"`                  // Replica specifications
	SSHKeyName     string               `json:"ssh_key_name,omitempty"`    // Optional SSH key to use
	FilesystemName string               `json:"filesystem_name,omitempty"` // Optional filesystem to mount
}

// LaunchClusterFromSpec creates a cluster according to the provided specification
func LaunchClusterFromSpec(httpClient *http.Client, apiToken string, spec *ClusterSpec) (*Cluster, error) {
	provider, _ := remote.GetCloudProvider()

	// Get available instance types to validate the spec
	availableInstances, err := provider.GetAvailableInstanceTypes(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get available instance types: %w", err)
	}

	// Validate the cluster spec
	if err := validateClusterSpec(spec, availableInstances); err != nil {
		return nil, fmt.Errorf("invalid cluster spec: %w", err)
	}

	// Initialize cluster with required components
	database, err := db.InitDB()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	cluster := &Cluster{
		Name:      spec.Name,
		Instances: []remote.RunningInstance{}, // Will be populated after instances are ready
		sshMgr:    remote.NewSSHClientManager(),
		database:  database,
	}

	// Save cluster to database
	dbCluster := &db.Cluster{
		Name:           spec.Name,
		SSHKeyName:     spec.SSHKeyName,
		FilesystemName: spec.FilesystemName,
		Status:         "launching",
		CreatedAt:      time.Now(),
	}
	clusterID, err := database.SaveCluster(dbCluster)
	if err != nil {
		return nil, fmt.Errorf("failed to save cluster to database: %w", err)
	}
	cluster.ID = clusterID

	// Save replica specifications to database
	for _, replica := range spec.Replicas {
		dbReplica := &db.ClusterReplica{
			ClusterID:    clusterID,
			InstanceType: replica.InstanceType,
			Region:       replica.Region,
			Quantity:     replica.Quantity,
			Name:         replica.Name,
		}
		_, err := database.SaveClusterReplica(dbReplica)
		if err != nil {
			log.Printf("Warning: failed to save replica spec to database: %v", err)
		}
	}

	var allLaunchedInstances []remote.LaunchedInstance

	// Launch instances according to spec
	for _, replica := range spec.Replicas {
		log.Printf("Launching %d instances of type %s in region %s", replica.Quantity, replica.InstanceType, replica.Region)

		// Use custom name if provided, otherwise use cluster name
		instanceName := replica.Name
		if instanceName == "" {
			instanceName = fmt.Sprintf("%s-%s", spec.Name, strings.ToLower(strings.ReplaceAll(replica.InstanceType, "_", "-")))
		}

		launchedInstances, err := provider.LaunchInstance(
			httpClient,
			apiToken,
			replica.InstanceType,
			replica.Region,
			replica.Quantity,
			instanceName,
			spec.SSHKeyName,
			spec.FilesystemName,
		)
		if err != nil {
			log.Printf("Failed to launch %d instances of type %s in %s: %v", replica.Quantity, replica.InstanceType, replica.Region, err)
			continue // Continue with other replicas even if one fails
		}

		allLaunchedInstances = append(allLaunchedInstances, launchedInstances...)
		log.Printf("Successfully launched %d instances in %s", len(launchedInstances), replica.Region)
	}

	// Wait for instances to be running and get their details
	if len(allLaunchedInstances) > 0 {
		log.Println("Waiting for instances to become available...")
		runningInstances, err := waitForInstances(httpClient, apiToken, allLaunchedInstances, 10*time.Minute)
		if err != nil {
			log.Printf("Warning: some instances may not be fully ready: %v", err)
		}
		cluster.Instances = runningInstances

		// Save instance associations to database
		for _, instance := range runningInstances {
			if err := database.AddInstanceToCluster(clusterID, instance.ID); err != nil {
				log.Printf("Warning: failed to save instance %s to cluster in database: %v", instance.ID, err)
			}
		}
	}

	// Update cluster status to running
	if err := database.UpdateClusterStatus(spec.Name, "running"); err != nil {
		log.Printf("Warning: failed to update cluster status: %v", err)
	}

	log.Printf("Cluster %s launched with %d running instances", spec.Name, len(cluster.Instances))
	return cluster, nil
}

// validateClusterSpec checks if the cluster specification is valid
func validateClusterSpec(spec *ClusterSpec, availableInstances []remote.Instance) error {
	if spec.Name == "" {
		return fmt.Errorf("cluster name is required")
	}

	if len(spec.Replicas) == 0 {
		return fmt.Errorf("at least one replica specification is required")
	}

	// Create a map of available instance types for quick lookup
	availableTypes := make(map[string]bool)
	regionAvailability := make(map[string]map[string]bool)

	for _, instance := range availableInstances {
		instanceTypeName := instance.InstanceType.Name
		availableTypes[instanceTypeName] = true

		if regionAvailability[instanceTypeName] == nil {
			regionAvailability[instanceTypeName] = make(map[string]bool)
		}

		for _, region := range instance.RegionsWithCapacityAvailable {
			regionAvailability[instanceTypeName][region.Name] = true
		}
	}

	// Validate each replica
	for i, replica := range spec.Replicas {
		if replica.InstanceType == "" {
			return fmt.Errorf("replica %d: instance type is required", i)
		}

		if !availableTypes[replica.InstanceType] {
			return fmt.Errorf("replica %d: instance type %s is not available", i, replica.InstanceType)
		}

		if replica.Region == "" {
			return fmt.Errorf("replica %d: region is required", i)
		}

		if !regionAvailability[replica.InstanceType][replica.Region] {
			return fmt.Errorf("replica %d: instance type %s is not available in region %s", i, replica.InstanceType, replica.Region)
		}

		if replica.Quantity <= 0 {
			return fmt.Errorf("replica %d: quantity must be greater than 0", i)
		}
	}

	return nil
}

// waitForInstances polls for running instances until they are active or timeout
func waitForInstances(httpClient *http.Client, apiToken string, launchedInstances []remote.LaunchedInstance, timeout time.Duration) ([]remote.RunningInstance, error) {
	provider, _ := remote.GetCloudProvider()
	startTime := time.Now()

	for {
		allActive := true
		var activeInstances []remote.RunningInstance
		var pendingCount int

		// Get current list of running instances
		instances, err := provider.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			return nil, fmt.Errorf("failed to list instances: %w", err)
		}

		// Check if all launched instances are active (not just present in the list)
		for _, launched := range launchedInstances {
			found := false
			for _, instance := range instances {
				if instance.ID == launched.ID {
					found = true
					// Only consider it ready if status is "active"
					if instance.Status == "active" {
						activeInstances = append(activeInstances, instance)
					} else {
						allActive = false
						pendingCount++
					}
					break
				}
			}
			if !found {
				allActive = false
				pendingCount++
			}
		}

		if allActive && len(activeInstances) == len(launchedInstances) {
			log.Printf("All %d instances are now active!", len(activeInstances))
			return activeInstances, nil
		}

		if time.Since(startTime) > timeout {
			return activeInstances, fmt.Errorf("timeout waiting for instances to be ready (%d/%d active)", len(activeInstances), len(launchedInstances))
		}

		log.Printf("Waiting for instances... (%d/%d active)", len(activeInstances), len(launchedInstances))
		time.Sleep(30 * time.Second)
	}
}

// LaunchCluster is deprecated. Use LaunchClusterFromSpec instead.
// This function maintains backward compatibility by launching instances in hardcoded regions.
func LaunchCluster(httpClient *http.Client, apiToken string) *Cluster {
	// Create a default cluster spec for backward compatibility
	spec := &ClusterSpec{
		Name: "sglang-auto-cluster",
		Replicas: []ClusterReplicaSpec{
			{InstanceType: "", Region: "us-west-1", Quantity: 1}, // Will be filled with first available
			{InstanceType: "", Region: "us-east-1", Quantity: 1},
			{InstanceType: "", Region: "us-south-1", Quantity: 1},
		},
	}

	// Get available instances to fill in instance types
	provider, _ := remote.GetCloudProvider()
	availableInstances, err := provider.GetAvailableInstanceTypes(httpClient, apiToken)
	if err != nil {
		log.Printf("Failed to get available instance types: %v", err)
		return &Cluster{}
	}

	// Fill in instance types with first available for each region
	for i := range spec.Replicas {
		replica := &spec.Replicas[i]
		for _, instance := range availableInstances {
			for _, region := range instance.RegionsWithCapacityAvailable {
				if region.Name == replica.Region {
					replica.InstanceType = instance.InstanceType.Name
					break
				}
			}
			if replica.InstanceType != "" {
				break
			}
		}
	}

	cluster, err := LaunchClusterFromSpec(httpClient, apiToken, spec)
	if err != nil {
		log.Printf("Failed to launch cluster: %v", err)
		return &Cluster{}
	}

	return cluster
}

// Connect establishes SSH connections to all instances in the cluster
func (c *Cluster) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected {
		return fmt.Errorf("already connected to cluster")
	}

	for _, instance := range c.Instances {
		if len(instance.SSHKeyNames) == 0 {
			log.Printf("Warning: instance %s has no SSH keys", instance.ID)
			continue
		}

		// Use the first SSH key (assuming there's typically one primary key)
		sshKeyName := instance.SSHKeyNames[0]

		// Get the SSH key path from database
		sshKey, err := c.database.GetSSHKey(sshKeyName)
		if err != nil {
			log.Printf("Failed to get SSH key %s for instance %s: %v", sshKeyName, instance.ID, err)
			continue
		}

		// Connect to the instance
		err = c.sshMgr.ConnectToInstance(instance.IP, sshKey.PrivateKey)
		if err != nil {
			log.Printf("Failed to connect to instance %s (%s): %v", instance.ID, instance.IP, err)
			continue
		}

		log.Printf("Connected to instance %s (%s)", instance.ID, instance.IP)
	}

	c.connected = true
	return nil
}

// ExecuteOnCluster runs a command on all connected instances
func (c *Cluster) ExecuteOnCluster(command string) map[string]*remote.ClusterCommandResult {
	c.mu.RLock()
	defer c.mu.RUnlock()

	results := make(map[string]*remote.ClusterCommandResult)

	for _, instance := range c.Instances {
		result := &remote.ClusterCommandResult{
			InstanceID: instance.ID,
			InstanceIP: instance.IP,
		}

		output, err := c.sshMgr.ExecuteCommand(instance.IP, command)
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
func (c *Cluster) ExecuteOnClusterAsync(command string) chan map[string]*remote.ClusterCommandResult {
	resultChan := make(chan map[string]*remote.ClusterCommandResult, 1)

	go func() {
		results := c.ExecuteOnCluster(command)
		resultChan <- results
	}()

	return resultChan
}

// ClusterHeartbeatResult represents the result of a cluster heartbeat check
type ClusterHeartbeatResult struct {
	InstanceID   string        `json:"instance_id"`
	InstanceIP   string        `json:"instance_ip"`
	Healthy      bool          `json:"healthy"`
	ResponseTime time.Duration `json:"response_time"`
	Error        error         `json:"error,omitempty"`
}

// ClusterHealthSummary provides a summary of cluster health
type ClusterHealthSummary struct {
	TotalInstances   int                      `json:"total_instances"`
	HealthyInstances int                      `json:"healthy_instances"`
	Results          []ClusterHeartbeatResult `json:"results"`
	CheckedAt        time.Time                `json:"checked_at"`
}

// Heartbeat checks the health of all cluster instances
func (c *Cluster) Heartbeat() *ClusterHealthSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	summary := &ClusterHealthSummary{
		TotalInstances: len(c.Instances),
		Results:        make([]ClusterHeartbeatResult, 0, len(c.Instances)),
		CheckedAt:      time.Now(),
	}

	for _, instance := range c.Instances {
		result := ClusterHeartbeatResult{
			InstanceID: instance.ID,
			InstanceIP: instance.IP,
		}

		// Simple heartbeat command
		start := time.Now()
		output, err := c.sshMgr.ExecuteCommand(instance.IP, "echo 'heartbeat'")
		result.ResponseTime = time.Since(start)

		if err != nil {
			result.Healthy = false
			result.Error = err
			log.Printf("Heartbeat failed for instance %s (%s): %v", instance.ID, instance.IP, err)
		} else {
			// Check if we got the expected response
			expected := "heartbeat"
			if strings.TrimSpace(output) == expected {
				result.Healthy = true
				summary.HealthyInstances++
			} else {
				result.Healthy = false
				result.Error = fmt.Errorf("unexpected response: got %q, expected %q", strings.TrimSpace(output), expected)
			}
		}

		summary.Results = append(summary.Results, result)
	}

	return summary
}

// HeartbeatAsync performs heartbeat checks asynchronously
func (c *Cluster) HeartbeatAsync() chan *ClusterHealthSummary {
	resultChan := make(chan *ClusterHealthSummary, 1)

	go func() {
		result := c.Heartbeat()
		resultChan <- result
	}()

	return resultChan
}

// IsHealthy returns true if all instances are healthy
func (c *Cluster) IsHealthy() bool {
	summary := c.Heartbeat()
	return summary.HealthyInstances == summary.TotalInstances
}

// GetHealthyInstances returns a list of healthy instance IPs
func (c *Cluster) GetHealthyInstances() []string {
	summary := c.Heartbeat()
	healthy := make([]string, 0, summary.HealthyInstances)

	for _, result := range summary.Results {
		if result.Healthy {
			healthy = append(healthy, result.InstanceIP)
		}
	}

	return healthy
}

// TaskHealthResult represents the health status of a background task
type TaskHealthResult struct {
	TaskName    string `json:"task_name"`
	InstanceID  string `json:"instance_id"`
	InstanceIP  string `json:"instance_ip"`
	IsRunning   bool   `json:"is_running"`
	SessionName string `json:"session_name,omitempty"`
	Error       error  `json:"error,omitempty"`
}

// TaskHealthSummary provides a summary of task health across the cluster
type TaskHealthSummary struct {
	RunningTasks int                `json:"running_tasks"`
	TotalTasks   int                `json:"total_tasks"`
	Results      []TaskHealthResult `json:"results"`
	CheckedAt    time.Time          `json:"checked_at"`
}

// CheckTaskHealth checks the health of background tasks running on cluster instances
func (c *Cluster) CheckTaskHealth() *TaskHealthSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	summary := &TaskHealthSummary{
		Results:   make([]TaskHealthResult, 0),
		CheckedAt: time.Now(),
	}

	for _, instance := range c.Instances {
		tasks := checkTasksOnInstance(c.sshMgr, instance.IP, instance.ID)
		summary.Results = append(summary.Results, tasks...)
	}

	// Count running tasks
	for _, result := range summary.Results {
		if result.IsRunning {
			summary.RunningTasks++
		}
	}
	summary.TotalTasks = len(summary.Results)

	return summary
}

// checkTasksOnInstance checks for running tmux sessions on a specific instance
func checkTasksOnInstance(manager *remote.SSHClientManager, instanceIP, instanceID string) []TaskHealthResult {
	var results []TaskHealthResult

	// Check for tmux sessions with our naming pattern
	cmd := "tmux list-sessions 2>/dev/null | grep '^gotoni-' | cut -d: -f1 || true"
	output, err := manager.ExecuteCommand(instanceIP, cmd)
	if err != nil {
		results = append(results, TaskHealthResult{
			TaskName:   "unknown",
			InstanceID: instanceID,
			InstanceIP: instanceIP,
			IsRunning:  false,
			Error:      fmt.Errorf("failed to check tmux sessions: %w", err),
		})
		return results
	}

	if output == "" {
		// No tmux sessions found
		return results
	}

	// Parse session names
	sessionNames := strings.Split(strings.TrimSpace(output), "\n")
	for _, sessionName := range sessionNames {
		sessionName = strings.TrimSpace(sessionName)
		if sessionName == "" {
			continue
		}

		// Extract task name from session name (remove gotoni- prefix)
		taskName := strings.TrimPrefix(sessionName, "gotoni-")

		// Check if session is actually active by trying to list its panes
		checkCmd := fmt.Sprintf("tmux list-panes -t %s 2>/dev/null | wc -l", sessionName)
		paneOutput, paneErr := manager.ExecuteCommand(instanceIP, checkCmd)

		isRunning := false
		if paneErr == nil {
			// If we can list panes and get a number > 0, session is active
			if paneCount := strings.TrimSpace(paneOutput); paneCount != "0" && paneCount != "" {
				isRunning = true
			}
		}

		results = append(results, TaskHealthResult{
			TaskName:    taskName,
			InstanceID:  instanceID,
			InstanceIP:  instanceIP,
			IsRunning:   isRunning,
			SessionName: sessionName,
		})
	}

	return results
}

// GetRunningTasks returns a list of currently running task names
func (c *Cluster) GetRunningTasks() []string {
	summary := c.CheckTaskHealth()
	running := make([]string, 0, summary.RunningTasks)

	for _, result := range summary.Results {
		if result.IsRunning {
			running = append(running, result.TaskName)
		}
	}

	return running
}

// IsTaskRunning checks if a specific task is running on any instance
func (c *Cluster) IsTaskRunning(taskName string) bool {
	runningTasks := c.GetRunningTasks()
	for _, runningTask := range runningTasks {
		if runningTask == taskName {
			return true
		}
	}
	return false
}

// ListBackgroundSessions lists all running background sessions on cluster instances

// InstanceType utilities

// GetInstanceTypeByName finds an instance type by name from available types
func GetInstanceTypeByName(httpClient *http.Client, apiToken, name string) (*InstanceType, error) {
	instances, err := remote.GetAvailableInstanceTypes(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get available instance types: %w", err)
	}

	for _, instance := range instances {
		if instance.InstanceType.Name == name {
			// Convert remote.Instance to local InstanceType
			return &InstanceType{
				Name:              instance.InstanceType.Name,
				Description:       instance.InstanceType.Description,
				GPUDescription:    instance.InstanceType.GPUDescription,
				PriceCentsPerHour: instance.InstanceType.PriceCentsPerHour,
				Specs: struct {
					VCPUs      int `json:"vcpus"`
					MemoryGib  int `json:"memory_gib"`
					StorageGib int `json:"storage_gib"`
					GPUs       int `json:"gpus"`
				}{
					VCPUs:      instance.InstanceType.Specs.VCPUs,
					MemoryGib:  instance.InstanceType.Specs.MemoryGib,
					StorageGib: instance.InstanceType.Specs.StorageGib,
					GPUs:       instance.InstanceType.Specs.GPUs,
				},
			}, nil
		}
	}

	return nil, fmt.Errorf("instance type %s not found", name)
}

// FilterInstanceTypesByGPU filters instance types that have GPUs
func FilterInstanceTypesByGPU(httpClient *http.Client, apiToken string) ([]InstanceType, error) {
	instances, err := remote.GetAvailableInstanceTypes(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get available instance types: %w", err)
	}

	var gpuInstances []InstanceType
	for _, instance := range instances {
		if instance.InstanceType.Specs.GPUs > 0 {
			gpuInstances = append(gpuInstances, InstanceType{
				Name:              instance.InstanceType.Name,
				Description:       instance.InstanceType.Description,
				GPUDescription:    instance.InstanceType.GPUDescription,
				PriceCentsPerHour: instance.InstanceType.PriceCentsPerHour,
				Specs: struct {
					VCPUs      int `json:"vcpus"`
					MemoryGib  int `json:"memory_gib"`
					StorageGib int `json:"storage_gib"`
					GPUs       int `json:"gpus"`
				}{
					VCPUs:      instance.InstanceType.Specs.VCPUs,
					MemoryGib:  instance.InstanceType.Specs.MemoryGib,
					StorageGib: instance.InstanceType.Specs.StorageGib,
					GPUs:       instance.InstanceType.Specs.GPUs,
				},
			})
		}
	}

	return gpuInstances, nil
}

// GetInstanceTypesByPriceRange filters instance types by price range (in cents per hour)
func GetInstanceTypesByPriceRange(httpClient *http.Client, apiToken string, minPrice, maxPrice int) ([]InstanceType, error) {
	instances, err := remote.GetAvailableInstanceTypes(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get available instance types: %w", err)
	}

	var filteredInstances []InstanceType
	for _, instance := range instances {
		price := instance.InstanceType.PriceCentsPerHour
		if price >= minPrice && price <= maxPrice {
			filteredInstances = append(filteredInstances, InstanceType{
				Name:              instance.InstanceType.Name,
				Description:       instance.InstanceType.Description,
				GPUDescription:    instance.InstanceType.GPUDescription,
				PriceCentsPerHour: instance.InstanceType.PriceCentsPerHour,
				Specs: struct {
					VCPUs      int `json:"vcpus"`
					MemoryGib  int `json:"memory_gib"`
					StorageGib int `json:"storage_gib"`
					GPUs       int `json:"gpus"`
				}{
					VCPUs:      instance.InstanceType.Specs.VCPUs,
					MemoryGib:  instance.InstanceType.Specs.MemoryGib,
					StorageGib: instance.InstanceType.Specs.StorageGib,
					GPUs:       instance.InstanceType.Specs.GPUs,
				},
			})
		}
	}

	return filteredInstances, nil
}

// FormatInstanceType returns a formatted string representation of an instance type
func (it *InstanceType) FormatInstanceType() string {
	gpuInfo := ""
	if it.Specs.GPUs > 0 {
		gpuInfo = fmt.Sprintf(", %d GPU(s): %s", it.Specs.GPUs, it.GPUDescription)
	}

	return fmt.Sprintf("%s: %d vCPUs, %d GB RAM, %d GB storage%s - $%.2f/hour",
		it.Name,
		it.Specs.VCPUs,
		it.Specs.MemoryGib,
		it.Specs.StorageGib,
		gpuInfo,
		float64(it.PriceCentsPerHour)/100.0,
	)
}

// IsGPUInstance returns true if the instance type has GPUs
func (it *InstanceType) IsGPUInstance() bool {
	return it.Specs.GPUs > 0
}

// GetEstimatedMonthlyCost calculates estimated monthly cost (24/7 usage)
func (it *InstanceType) GetEstimatedMonthlyCost() float64 {
	hourlyRate := float64(it.PriceCentsPerHour) / 100.0
	return hourlyRate * 24 * 30 // Rough estimate
}

// RunTaskOnCluster executes a single task on all cluster instances
func (c *Cluster) RunTaskOnCluster(task remote.Task) map[string]error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	results := make(map[string]error)

	for _, instance := range c.Instances {
		// Execute the task on this instance
		err := remote.ExecuteTask(c.sshMgr, instance.IP, task, make(map[string]bool))
		if err != nil {
			results[instance.ID] = fmt.Errorf("failed to execute task %s: %w", task.Name, err)
			log.Printf("Task %s failed on instance %s: %v", task.Name, instance.ID, err)
		} else {
			results[instance.ID] = nil
			log.Printf("Task %s completed successfully on instance %s", task.Name, instance.ID)
		}
	}

	return results
}

// RunTaskOnClusterAsync executes a single task on all cluster instances asynchronously
func (c *Cluster) RunTaskOnClusterAsync(task remote.Task) chan map[string]error {
	resultChan := make(chan map[string]error, 1)

	go func() {
		results := c.RunTaskOnCluster(task)
		resultChan <- results
	}()

	return resultChan
}

// RunTasksOnCluster executes multiple tasks with dependency resolution on all cluster instances
func (c *Cluster) RunTasksOnCluster(tasks []remote.Task) map[string]error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	results := make(map[string]error)

	for _, instance := range c.Instances {
		// Execute tasks on this instance with dependency resolution
		err := remote.ExecuteTasks(c.sshMgr, instance.IP, tasks)
		if err != nil {
			results[instance.ID] = fmt.Errorf("failed to execute tasks on instance: %w", err)
			log.Printf("Tasks failed on instance %s: %v", instance.ID, err)
		} else {
			results[instance.ID] = nil
			log.Printf("All tasks completed successfully on instance %s", instance.ID)
		}
	}

	return results
}

// RunTasksOnClusterAsync executes multiple tasks on all cluster instances asynchronously
func (c *Cluster) RunTasksOnClusterAsync(tasks []remote.Task) chan map[string]error {
	resultChan := make(chan map[string]error, 1)

	go func() {
		results := c.RunTasksOnCluster(tasks)
		resultChan <- results
	}()

	return resultChan
}

// StartServiceOnCluster creates and starts a tmux session on all cluster instances
// Deprecated: Use RunTaskOnCluster with Background=true instead
func (c *Cluster) StartServiceOnCluster(serviceName, command, workingDir string, envVars map[string]string) map[string]error {
	// Create a background task (tmux session)
	task := remote.Task{
		Name:       serviceName,
		Type:       "command",
		Command:    command,
		Background: true,
		WorkingDir: workingDir,
		Env:        envVars,
		Restart:    "always",
	}

	return c.RunTaskOnCluster(task)
}

// StopService stops a systemd service on all cluster instances (deprecated - use tasks instead)
func (c *Cluster) StopService(serviceName string) map[string]error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	results := make(map[string]error)

	for _, instance := range c.Instances {
		err := c.sshMgr.StopSystemdService(instance.IP, serviceName)
		if err != nil {
			results[instance.ID] = err
			continue
		}

		log.Printf("Stopped service %s on instance %s", serviceName, instance.ID)
	}

	return results
}

// Disconnect closes all SSH connections
func (c *Cluster) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sshMgr.CloseAllConnections()
	c.connected = false

	// Update cluster status in database
	if c.database != nil && c.Name != "" {
		if err := c.database.UpdateClusterStatus(c.Name, "disconnected"); err != nil {
			log.Printf("Warning: failed to update cluster status: %v", err)
		}
	}

	log.Println("Disconnected from all cluster instances")
}

// Close closes the cluster and its database connection
func (c *Cluster) Close() {
	c.Disconnect()
	if c.database != nil {
		c.database.Close()
	}
}

// ExecuteCommand executes a command on a specific instance by IP
// Returns the command output and any error
func (c *Cluster) ExecuteCommand(instanceIP string, command string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.connected {
		return "", fmt.Errorf("cluster not connected")
	}

	return c.sshMgr.ExecuteCommand(instanceIP, command)
}

// ExecuteCommandWithTimeout executes a command on a specific instance with a timeout
// Returns the command output and any error
func (c *Cluster) ExecuteCommandWithTimeout(instanceIP string, command string, timeout time.Duration) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.connected {
		return "", fmt.Errorf("cluster not connected")
	}

	return c.sshMgr.ExecuteCommandWithTimeout(instanceIP, command, timeout)
}

// Helper functions for creating common task types

// NewTask creates a new task with the specified parameters
func NewTask(name, command string, background bool) remote.Task {
	task := remote.Task{
		Name:       name,
		Type:       "command",
		Command:    command,
		Background: background,
		WorkingDir: "/home/ubuntu",
		Restart:    "always",
		RestartSec: 10,
	}

	if background {
		task.Restart = "always"
	}

	return task
}

// NewBackgroundTask creates a task that runs in the background (using tmux)
func NewBackgroundTask(name, command, workingDir string, envVars map[string]string) remote.Task {
	task := NewTask(name, command, true) // background = true
	task.WorkingDir = workingDir
	task.Env = envVars
	return task
}

// NewForegroundTask creates a task that runs once and completes
func NewForegroundTask(name, command, workingDir string, envVars map[string]string) remote.Task {
	task := NewTask(name, command, false) // background = false
	task.WorkingDir = workingDir
	task.Env = envVars
	task.Restart = "no" // Don't restart foreground tasks
	return task
}

// NewCluster creates a new cluster with the given name and instances
// This is used when creating a cluster that doesn't exist in the database yet
func NewCluster(name string, instances []remote.RunningInstance, database *db.DB) *Cluster {
	return &Cluster{
		Name:      name,
		Instances: instances,
		sshMgr:    remote.NewSSHClientManager(),
		database:  database,
	}
}

// GetCluster loads a cluster from the database by name and populates running instances
func GetCluster(httpClient *http.Client, apiToken string, name string) (*Cluster, error) {
	database, err := db.InitDB()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	// Get cluster from database
	dbCluster, err := database.GetCluster(name)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("cluster %s not found", name)
		}
		return nil, fmt.Errorf("failed to get cluster from database: %w", err)
	}

	cluster := &Cluster{
		ID:       dbCluster.ID,
		Name:     dbCluster.Name,
		sshMgr:   remote.NewSSHClientManager(),
		database: database,
	}

	// Get instance IDs associated with this cluster
	instanceIDs, err := database.GetClusterInstances(dbCluster.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster instances: %w", err)
	}

	// Get running instances from the provider
	if len(instanceIDs) > 0 {
		provider, _ := remote.GetCloudProvider()
		allRunning, err := provider.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			return nil, fmt.Errorf("failed to list running instances: %w", err)
		}

		// Filter to only include instances in this cluster
		instanceIDMap := make(map[string]bool)
		for _, id := range instanceIDs {
			instanceIDMap[id] = true
		}

		for _, instance := range allRunning {
			if instanceIDMap[instance.ID] {
				cluster.Instances = append(cluster.Instances, instance)
			}
		}
	}

	return cluster, nil
}

// ListClusters returns all clusters from the database
func ListClusters() ([]db.Cluster, error) {
	database, err := db.InitDB()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	defer database.Close()

	return database.ListClusters()
}

// DeleteClusterByName deletes a cluster from the database (does not terminate instances)
func DeleteClusterByName(name string) error {
	database, err := db.InitDB()
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer database.Close()

	return database.DeleteCluster(name)
}

// TerminateCluster terminates all instances in a cluster and removes it from the database
func TerminateCluster(httpClient *http.Client, apiToken string, name string) error {
	database, err := db.InitDB()
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer database.Close()

	// Get cluster from database
	dbCluster, err := database.GetCluster(name)
	if err != nil {
		return fmt.Errorf("failed to get cluster: %w", err)
	}

	// Get instance IDs
	instanceIDs, err := database.GetClusterInstances(dbCluster.ID)
	if err != nil {
		return fmt.Errorf("failed to get cluster instances: %w", err)
	}

	// Terminate instances
	if len(instanceIDs) > 0 {
		_, err := remote.TerminateInstance(httpClient, apiToken, instanceIDs)
		if err != nil {
			log.Printf("Warning: failed to terminate some instances: %v", err)
		}
	}

	// Update cluster status
	if err := database.UpdateClusterStatus(name, "terminated"); err != nil {
		log.Printf("Warning: failed to update cluster status: %v", err)
	}

	// Delete cluster from database
	return database.DeleteCluster(name)
}

// GetClusterSpec retrieves the cluster specification from the database
func GetClusterSpec(name string) (*ClusterSpec, error) {
	database, err := db.InitDB()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	defer database.Close()

	// Get cluster from database
	dbCluster, err := database.GetCluster(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}

	// Get replicas
	dbReplicas, err := database.GetClusterReplicas(dbCluster.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster replicas: %w", err)
	}

	spec := &ClusterSpec{
		Name:           dbCluster.Name,
		SSHKeyName:     dbCluster.SSHKeyName,
		FilesystemName: dbCluster.FilesystemName,
		Replicas:       make([]ClusterReplicaSpec, 0, len(dbReplicas)),
	}

	for _, replica := range dbReplicas {
		spec.Replicas = append(spec.Replicas, ClusterReplicaSpec{
			InstanceType: replica.InstanceType,
			Region:       replica.Region,
			Quantity:     replica.Quantity,
			Name:         replica.Name,
		})
	}

	return spec, nil
}

// RefreshClusterInstances updates the cluster's running instances from the provider
func (c *Cluster) RefreshClusterInstances(httpClient *http.Client, apiToken string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Get instance IDs from database
	instanceIDs, err := c.database.GetClusterInstances(c.ID)
	if err != nil {
		return fmt.Errorf("failed to get cluster instances: %w", err)
	}

	if len(instanceIDs) == 0 {
		c.Instances = []remote.RunningInstance{}
		return nil
	}

	// Get running instances from provider
	provider, _ := remote.GetCloudProvider()
	allRunning, err := provider.ListRunningInstances(httpClient, apiToken)
	if err != nil {
		return fmt.Errorf("failed to list running instances: %w", err)
	}

	// Filter to only include instances in this cluster
	instanceIDMap := make(map[string]bool)
	for _, id := range instanceIDs {
		instanceIDMap[id] = true
	}

	c.Instances = []remote.RunningInstance{}
	for _, instance := range allRunning {
		if instanceIDMap[instance.ID] {
			c.Instances = append(c.Instances, instance)
		}
	}

	return nil
}

// AddInstance adds an instance to the cluster
func (c *Cluster) AddInstance(instance remote.RunningInstance) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Add to database
	if err := c.database.AddInstanceToCluster(c.ID, instance.ID); err != nil {
		return fmt.Errorf("failed to add instance to cluster in database: %w", err)
	}

	// Add to in-memory list
	c.Instances = append(c.Instances, instance)
	return nil
}

// RemoveInstance removes an instance from the cluster
func (c *Cluster) RemoveInstance(instanceID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Remove from database
	if err := c.database.RemoveInstanceFromCluster(c.ID, instanceID); err != nil {
		return fmt.Errorf("failed to remove instance from cluster in database: %w", err)
	}

	// Remove from in-memory list
	for i, instance := range c.Instances {
		if instance.ID == instanceID {
			c.Instances = append(c.Instances[:i], c.Instances[i+1:]...)
			break
		}
	}

	return nil
}

// GetStatus returns the cluster status from the database
func (c *Cluster) GetStatus() (string, error) {
	dbCluster, err := c.database.GetClusterByID(c.ID)
	if err != nil {
		return "", fmt.Errorf("failed to get cluster status: %w", err)
	}
	return dbCluster.Status, nil
}

// SetStatus updates the cluster status in the database
func (c *Cluster) SetStatus(status string) error {
	return c.database.UpdateClusterStatus(c.Name, status)
}

// SSHMgr returns the SSH manager for this cluster
func (c *Cluster) SSHMgr() *remote.SSHClientManager {
	return c.sshMgr
}

// WaitForHealthyCluster waits for the specified number of healthy instances
func WaitForHealthyCluster(cluster *Cluster, minHealthy int, timeout time.Duration) error {
	startTime := time.Now()
	pollInterval := 15 * time.Second

	for {
		heartbeat := cluster.Heartbeat()
		healthyCount := heartbeat.HealthyInstances

		if healthyCount >= minHealthy {
			fmt.Printf("Found %d healthy instances!\n", healthyCount)
			return nil
		}

		if time.Since(startTime) > timeout {
			return fmt.Errorf("timeout: only %d/%d healthy instances after %v", healthyCount, heartbeat.TotalInstances, timeout)
		}

		fmt.Printf("Currently %d healthy instances. Waiting for more to become available...\n", healthyCount)
		time.Sleep(pollInterval)
	}
}

// LBDeployConfig holds configuration for load balancer deployment
type LBDeployConfig struct {
	Strategy             string
	ObservabilityEnabled bool
	LokiEndpoint         string
	ClusterName          string
	MaxConcurrent        int
	RunningThreshold     int
	Peers                []string
}

// DeployLBStrategy stops any existing load balancer and starts a new one with the given strategy
// on all cluster instances. This is lightweight and can be called multiple times with different
// strategies on the same cluster.
func (c *Cluster) DeployLBStrategy(strategy string) error {
	return c.DeployLBStrategyWithConfig(LBDeployConfig{Strategy: strategy})
}

// DeployLBStrategyWithObservability deploys LB with observability enabled to push to Loki
func (c *Cluster) DeployLBStrategyWithObservability(strategy, lokiEndpoint string) error {
	return c.DeployLBStrategyWithConfig(LBDeployConfig{
		Strategy:             strategy,
		ObservabilityEnabled: true,
		LokiEndpoint:         lokiEndpoint,
	})
}

// DeployLBStrategyWithConfig deploys LB with full configuration options
func (c *Cluster) DeployLBStrategyWithConfig(config LBDeployConfig) error {
	fmt.Printf("=== Deploying LB Strategy: %s ===\n", config.Strategy)
	if config.ObservabilityEnabled {
		fmt.Printf("Observability enabled, pushing to: %s\n", config.LokiEndpoint)
	}

	// Kill existing load balancer tmux sessions on all instances
	fmt.Println("Stopping existing load balancers...")
	killCmd := "tmux kill-session -t gotoni-start_gotoni_load_balancer 2>/dev/null || true; pkill -f 'gotoni lb' 2>/dev/null || true"
	c.ExecuteOnCluster(killCmd)

	// Brief pause to let processes clean up
	time.Sleep(2 * time.Second)

	// Collect all peer IPs for mesh configuration
	var allIPs []string
	for _, inst := range c.Instances {
		allIPs = append(allIPs, inst.IP)
	}

	failCount := 0
	for i, inst := range c.Instances {
		// Build peer list (all other nodes)
		var peers []string
		for j, peerIP := range allIPs {
			if i != j {
				peers = append(peers, fmt.Sprintf("%s:8000", peerIP))
			}
		}

		// Build the command with optional observability and peer flags
		lbCommand := fmt.Sprintf("/home/ubuntu/gotoni lb start --listen-port 8000 --local-port 8080 --strategy %s", config.Strategy)
		if config.ObservabilityEnabled && config.LokiEndpoint != "" {
			nodeID := fmt.Sprintf("node-%d", i)
			if inst.Name != "" {
				nodeID = inst.Name
			}
			lbCommand += fmt.Sprintf(" --observability --loki-endpoint %s --node-id %s --cluster-name %s",
				config.LokiEndpoint, nodeID, config.ClusterName)
		}

		// Add peers
		for _, peer := range peers {
			lbCommand += fmt.Sprintf(" --peers %s", peer)
		}

		// Start load balancer with new strategy on this instance
		lbTask := remote.Task{
			Name:       "start gotoni load balancer",
			Command:    lbCommand,
			Background: true,
			WorkingDir: "/home/ubuntu",
		}

		err := remote.ExecuteTask(c.sshMgr, inst.IP, lbTask, make(map[string]bool))
		if err != nil {
			fmt.Printf("Failed to start LB on %s: %v\n", inst.IP, err)
			failCount++
		} else {
			fmt.Printf("LB (%s) started on %s with %d peers\n", config.Strategy, inst.IP, len(peers))
		}
	}

	if failCount == len(c.Instances) {
		return fmt.Errorf("failed to start load balancer on all instances")
	}

	// Wait for LB to start accepting connections
	time.Sleep(3 * time.Second)

	fmt.Printf("=== LB Strategy %s Deployed ===\n", config.Strategy)
	return nil
}

// SetupSGLangCluster discovers running instances, creates/loads a cluster, connects via SSH,
// sets up SGLang Docker, deploys SGLang servers, and waits for them to be healthy.
// Returns a connected cluster with SGLang running on all instances.
// Caller is responsible for calling cluster.Disconnect() when done.
func SetupSGLangCluster(httpClient *http.Client, apiToken, clusterName, hfToken string) (*Cluster, error) {
	fmt.Println("=== Setting up SGLang Cluster ===")

	// 1. Discover all running instances
	fmt.Println("1. Discovering all running instances...")
	provider, _ := remote.GetCloudProvider()
	allInstances, err := provider.ListRunningInstances(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to list running instances: %w", err)
	}
	if len(allInstances) == 0 {
		return nil, fmt.Errorf("no running instances found")
	}

	fmt.Printf("Found %d running instance(s):\n", len(allInstances))
	for _, inst := range allInstances {
		fmt.Printf("  - %s (%s) @ %s [%s]\n", inst.Name, inst.ID[:16], inst.IP, inst.Region.Name)
	}

	// 2. Create or load cluster with all running instances
	cl, err := GetCluster(httpClient, apiToken, clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to get/create cluster: %w", err)
	}

	// Ensure all instances are in the cluster
	existingIDs := make(map[string]bool)
	for _, inst := range cl.Instances {
		existingIDs[inst.ID] = true
	}
	for _, inst := range allInstances {
		if !existingIDs[inst.ID] {
			fmt.Printf("  Adding instance: %s (%s) @ %s\n", inst.Name, inst.ID[:16], inst.IP)
			if addErr := cl.AddInstance(inst); addErr != nil {
				fmt.Printf("    Failed to add: %v\n", addErr)
			}
		}
	}
	cl.Instances = allInstances
	fmt.Printf("Cluster '%s' has %d instances\n", clusterName, len(cl.Instances))

	// 3. Connect SSH to all instances
	fmt.Println("2. Connecting to cluster instances...")
	if err := cl.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to cluster: %w", err)
	}
	if err := WaitForHealthyCluster(cl, len(cl.Instances), 10*time.Minute); err != nil {
		cl.Disconnect()
		return nil, fmt.Errorf("failed to get healthy cluster: %w", err)
	}
	fmt.Printf("Connected to %d instances in cluster\n", len(cl.Instances))

	// 4. Setup SGLang Docker on all instances
	fmt.Println("\n3. Setting up SGLang on cluster instances...")
	if hfToken == "" {
		fmt.Println("Warning: HF_TOKEN not set, model download may fail for gated models")
	}
	SetupSGLangDockerOnCluster(cl, hfToken)

	// 5. Deploy SGLang server task
	fmt.Println("\n4. Deploying SGLang servers to cluster instances...")
	sglangServerTask := remote.Task{
		Name: "sglang-server-docker",
		Command: `#!/bin/bash
# Check if container is already running AND healthy
if sudo docker ps --filter name=sglang-server --filter status=running | grep -q sglang-server; then
    if curl -s --connect-timeout 5 http://localhost:8080/health > /dev/null 2>&1; then
        echo "Container sglang-server is already running and healthy"
        exit 0
    else
        echo "Container exists but not healthy, restarting..."
        sudo docker rm -f sglang-server 2>/dev/null || true
    fi
fi

if ! sudo docker images lmsysorg/sglang | grep -q sglang; then
    echo "Pulling lmsysorg/sglang:latest image..."
    sudo docker pull lmsysorg/sglang:latest
fi

sudo docker rm -f sglang-server 2>/dev/null || true
sudo docker rm -f llama-server 2>/dev/null || true

HF_TOKEN_FILE="/home/ubuntu/.cache/huggingface/token"
HF_TOKEN_ENV=""
if [ -f "$HF_TOKEN_FILE" ]; then
    HF_TOKEN=$(cat "$HF_TOKEN_FILE")
    if [ -n "$HF_TOKEN" ]; then
        HF_TOKEN_ENV="-e HF_TOKEN=$HF_TOKEN -e HUGGING_FACE_HUB_TOKEN=$HF_TOKEN"
        echo "Using HF_TOKEN from cache file"
    fi
fi

sudo docker run --gpus all \
    -v /home/ubuntu/.cache/huggingface:/root/.cache/huggingface \
    -p 8080:8080 \
    $HF_TOKEN_ENV \
    --name sglang-server \
    --restart unless-stopped \
    -d lmsysorg/sglang:latest \
    python -m sglang.launch_server \
    --model-path mistralai/Mistral-7B-Instruct-v0.3 \
    --port 8080 \
    --host 0.0.0.0 \
    --enable-metrics

echo 'SGLang container started successfully (with --enable-metrics)'
echo 'Note: Model loading can take 2-5 minutes on first run'
`,
		Background: true,
		WorkingDir: "/home/ubuntu",
		Env: map[string]string{
			"CUDA_VISIBLE_DEVICES": "0",
		},
	}

	resultChan := make(chan map[string]error, 1)
	go func() {
		resultChan <- cl.RunTaskOnCluster(sglangServerTask)
	}()

	select {
	case results := <-resultChan:
		successCount := 0
		for instanceID, err := range results {
			if err != nil {
				fmt.Printf("Task sglang-server-docker failed on instance %s: %v\n", instanceID, err)
			} else {
				fmt.Printf("Task sglang-server-docker completed on instance %s\n", instanceID)
				successCount++
			}
		}
		fmt.Printf("SGLang deployed on %d/%d instances\n", successCount, len(cl.Instances))
	case <-time.After(5 * time.Minute):
		fmt.Println("SGLang deployment timed out after 5 minutes")
	}

	// 6. Wait for SGLang to initialize and verify health
	fmt.Println("\n5. Waiting for SGLang to initialize...")
	time.Sleep(60 * time.Second)

	fmt.Println("Running SGLang diagnostics...")
	DiagnoseSGLangOnCluster(cl)

	taskHealth := cl.CheckTaskHealth()
	fmt.Printf("Task Health: %d/%d running\n", taskHealth.RunningTasks, taskHealth.TotalTasks)

	if !cl.IsTaskRunning("sglang-server-docker") {
		fmt.Println("Warning: sglang-server-docker is not running on all instances")
	}

	// 7. Verify SGLang API endpoints
	fmt.Println("\n6. Verifying SGLang API endpoints...")
	TestSGLangAPIEndpoints(cl, cl.Instances)

	fmt.Println("=== SGLang Cluster Ready ===")
	return cl, nil
}

// SetupSGLangDockerOnCluster sets up Docker-based SGLang deployment (parallel)
func SetupSGLangDockerOnCluster(cl *Cluster, hfToken string) {
	fmt.Println("Checking SGLang Docker setup on all instances...")

	// Check if SGLang container is already running
	checkCmd := "sudo docker ps --filter name=sglang-server --filter status=running --format '{{.Names}}' 2>/dev/null | grep -q sglang-server && echo 'running' || echo 'not_running'"
	results := cl.ExecuteOnCluster(checkCmd)

	// Collect instances that need setup
	var needsSetup []string
	for instanceID, result := range results {
		if result.Error != nil {
			fmt.Printf("❌ Failed to check SGLang on %s: %v\n", instanceID[:16], result.Error)
			needsSetup = append(needsSetup, instanceID)
			continue
		}

		output := strings.TrimSpace(result.Output)
		if output == "running" {
			fmt.Printf("✅ SGLang already running on %s\n", instanceID[:16])
		} else {
			needsSetup = append(needsSetup, instanceID)
		}
	}

	// Setup HuggingFace credentials and pull Docker image in parallel
	if len(needsSetup) > 0 {
		fmt.Printf("\n📦 Setting up SGLang on %d instances in parallel...\n", len(needsSetup))
		var wg sync.WaitGroup
		for _, instanceID := range needsSetup {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				fmt.Printf("  → Starting SGLang setup on %s...\n", id[:16])
				setupSGLangOnInstance(cl, id, hfToken)
			}(instanceID)
		}
		wg.Wait()
		fmt.Println("✅ All SGLang setups complete!")
	}
}

// setupSGLangOnInstance sets up SGLang on a specific instance
func setupSGLangOnInstance(cl *Cluster, instanceID string, hfToken string) {
	// Find the instance IP
	var instanceIP string
	for _, inst := range cl.Instances {
		if inst.ID == instanceID {
			instanceIP = inst.IP
			break
		}
	}
	if instanceIP == "" {
		fmt.Printf("❌ Instance %s not found\n", instanceID[:16])
		return
	}

	// Setup HuggingFace credentials and pull SGLang Docker image
	setupScript := fmt.Sprintf(`#!/bin/bash
set -e

echo "Setting up HuggingFace credentials..."
mkdir -p /home/ubuntu/.cache/huggingface
if [ -n "%s" ]; then
    echo "%s" > /home/ubuntu/.cache/huggingface/token
    echo "HF_TOKEN saved to cache"
fi

echo "Checking Docker installation..."
if ! command -v docker &> /dev/null; then
    echo "Docker not found, installing..."
    curl -fsSL https://get.docker.com -o get-docker.sh
    sudo sh get-docker.sh
    rm get-docker.sh
    sudo usermod -aG docker ubuntu
fi

echo "Pulling SGLang Docker image (this may take a while)..."
sudo docker pull lmsysorg/sglang:latest

echo "SGLang Docker setup complete!"
`, hfToken, hfToken)

	output, err := cl.sshMgr.ExecuteCommandWithTimeout(instanceIP, setupScript, 15*time.Minute)
	if err != nil {
		fmt.Printf("❌ Failed to setup SGLang on %s: %v\n", instanceID[:16], err)
		fmt.Printf("Output: %s\n", output)
		return
	}
	fmt.Printf("✅ SGLang setup complete on %s\n", instanceID[:16])
}

// DiagnoseSGLangOnCluster runs diagnostic commands on all instances to identify connectivity issues
func DiagnoseSGLangOnCluster(cl *Cluster) {
	fmt.Println("Running diagnostics on all cluster instances...")

	diagScript := `#!/bin/bash
echo "=== DIAGNOSTICS FOR $(hostname) ==="

echo ""
echo "1. Docker container status:"
sudo docker ps -a --filter name=sglang-server --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"

echo ""
echo "2. Container running check:"
if sudo docker ps --filter name=sglang-server --filter status=running | grep -q sglang-server; then
    echo "✅ Container is RUNNING"
else
    echo "❌ Container is NOT RUNNING"
    echo "   Last 20 lines of container logs (if available):"
    sudo docker logs sglang-server --tail 20 2>&1 || echo "   No logs available"
fi

echo ""
echo "3. Port 8080 listening check (netstat/ss):"
ss -tlnp | grep :8080 || echo "   Port 8080 is NOT listening on any interface"

echo ""
echo "4. Docker port mappings:"
sudo docker port sglang-server 2>/dev/null || echo "   No port mappings (container not running?)"

echo ""
echo "5. Local connectivity test (curl localhost:8080):"
curl -s -o /dev/null -w "HTTP Status: %{http_code}\n" --connect-timeout 5 http://localhost:8080/health 2>/dev/null || echo "   Failed to connect to localhost:8080"

echo ""
echo "6. Local metrics endpoint test:"
curl -s --connect-timeout 5 http://localhost:8080/get_server_info 2>/dev/null | head -c 500 || echo "   Failed to get server info"

echo ""
echo "7. Network interfaces (for binding verification):"
ip addr show | grep "inet " | head -5

echo ""
echo "8. Docker network inspection:"
sudo docker inspect sglang-server --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null || echo "   Container not found"

echo ""
echo "9. GPU availability check:"
nvidia-smi --query-gpu=name,memory.used,memory.total --format=csv,noheader 2>/dev/null || echo "   nvidia-smi not available"

echo ""
echo "10. Container environment (HF_TOKEN check):"
sudo docker exec sglang-server env 2>/dev/null | grep -E "^(HF_|HUGGING)" || echo "   No HF environment variables set in container"

echo ""
echo "=== END DIAGNOSTICS ==="
`

	results := cl.ExecuteOnCluster(diagScript)

	for instanceID, result := range results {
		// Find instance IP for display
		var instanceIP string
		for _, inst := range cl.Instances {
			if inst.ID == instanceID {
				instanceIP = inst.IP
				break
			}
		}

		fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Printf("Instance: %s (%s)\n", instanceID[:16], instanceIP)
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		if result.Error != nil {
			fmt.Printf("❌ Diagnostic failed: %v\n", result.Error)
		} else {
			fmt.Println(result.Output)
		}
	}

	// Summary of potential issues
	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("COMMON ISSUES TO CHECK:")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("1. Container not running → Check Docker logs for crash reason")
	fmt.Println("2. Port not listening → Server failed to start inside container")
	fmt.Println("3. No HF environment vars → Model download will fail for gated models")
	fmt.Println("4. GPU memory issues → Check nvidia-smi for OOM")
	fmt.Println("5. Model not downloaded → First startup can take 10+ minutes")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
}

// TestSGLangAPIEndpoints tests the SGLang server endpoints using /get_server_info
func TestSGLangAPIEndpoints(cl *Cluster, instances []remote.RunningInstance) {
	testClient := &http.Client{Timeout: 10 * time.Second}

	// All SGLang servers run on port 8080
	port := 8080

	for _, instance := range instances {
		// Test health endpoint
		healthURL := fmt.Sprintf("http://%s:%d/health", instance.IP, port)
		resp, err := testClient.Get(healthURL)
		if err != nil {
			fmt.Printf("❌ Health check failed for %s:%d - %v\n", instance.IP, port, err)
		} else {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				fmt.Printf("✅ Health endpoint %s:%d is responding\n", instance.IP, port)
			} else {
				fmt.Printf("⚠️  Health endpoint %s:%d returned status %d\n", instance.IP, port, resp.StatusCode)
			}
		}

		// Test SGLang metrics endpoint (/get_server_info) as used in sglang_metrics.go
		metricsURL := fmt.Sprintf("http://%s:%d/get_server_info", instance.IP, port)
		resp, err = testClient.Get(metricsURL)
		if err != nil {
			fmt.Printf("❌ Metrics check failed for %s:%d - %v\n", instance.IP, port, err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == 200 {
			// Parse the SGLang server info
			var serverInfo struct {
				NumRunningReqs int     `json:"num_running_reqs"`
				NumWaitingReqs int     `json:"num_waiting_reqs"`
				MaxRunningReqs int     `json:"max_running_reqs"`
				GPUCacheUsage  float64 `json:"token_usage"`
			}
			if decodeErr := json.NewDecoder(resp.Body).Decode(&serverInfo); decodeErr != nil {
				fmt.Printf("⚠️  Failed to decode server info from %s:%d: %v\n", instance.IP, port, decodeErr)
			} else {
				fmt.Printf("✅ SGLang metrics from %s:%d:\n", instance.IP, port)
				fmt.Printf("   Running requests: %d\n", serverInfo.NumRunningReqs)
				fmt.Printf("   Waiting requests: %d\n", serverInfo.NumWaitingReqs)
				fmt.Printf("   Max running requests: %d\n", serverInfo.MaxRunningReqs)
				fmt.Printf("   GPU cache usage: %.2f%%\n", serverInfo.GPUCacheUsage*100)
			}
		} else {
			fmt.Printf("⚠️  Metrics endpoint %s:%d returned status %d\n", instance.IP, port, resp.StatusCode)
		}
	}
}
