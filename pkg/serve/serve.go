/*
Copyright © 2025 ALESSIO TONIOLO

serve.go contains cluster management and command execution functionality
*/
package serve

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/atoniolo76/gotoni/pkg/remote"
)

// CreateClusterAndRunEcho creates a new cluster and runs an echo command on each instance
func CreateClusterAndRunEcho(httpClient *http.Client, apiToken string) error {
	// Create a new cluster
	cluster := LaunchCluster(httpClient, apiToken)

	if len(cluster.Instances) == 0 {
		log.Println("No instances in cluster - waiting for instances to be launched...")
		// Wait a bit for instances to be launched (in a real implementation,
		// you'd poll the API to check instance status)
		time.Sleep(30 * time.Second)

		// Try to get running instances
		runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			return fmt.Errorf("failed to list running instances: %w", err)
		}

		cluster.Instances = runningInstances
	}

	if len(cluster.Instances) == 0 {
		return fmt.Errorf("no instances available in cluster")
	}

	fmt.Printf("Found %d instances in cluster\n", len(cluster.Instances))

	// Create cluster manager
	clusterMgr := NewClusterManager(cluster)
	defer clusterMgr.DisconnectFromCluster()

	// Connect to all instances
	fmt.Println("Connecting to cluster instances...")
	if err := clusterMgr.ConnectToCluster(); err != nil {
		return fmt.Errorf("failed to connect to cluster: %w", err)
	}

	// Run echo command on all instances
	echoCommand := "echo 'Hello from cluster instance!'"
	fmt.Printf("Running command on all instances: %s\n", echoCommand)

	results := clusterMgr.ExecuteOnCluster(echoCommand)

	// Display results
	fmt.Println("\nCommand execution results:")
	for instanceID, result := range results {
		if result.Error != nil {
			fmt.Printf("❌ Instance %s: ERROR - %v\n", instanceID, result.Error)
		} else {
			fmt.Printf("✅ Instance %s: %s\n", instanceID, result.Output)
		}
	}

	return nil
}
