package cmd

import (
	"fmt"
	"log"

	"github.com/atoniolo76/gotoni/pkg/db"
	"github.com/atoniolo76/gotoni/pkg/remote"

	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve your favorite models and processes on vLLM across instances and regions",
	Long:  "Serve your favorite models and processeson vLLM across instances and regions.",
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		if apiToken == "" {
			apiToken = remote.GetAPIToken()
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		httpClient := remote.NewHTTPClient()
		runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Error listing running instances: %v", err)
		}

		if len(runningInstances) == 0 {
			fmt.Println("No running instances found.")
			return
		}

		// Initialize database
		database, err := db.InitDB()
		if err != nil {
			log.Fatalf("Error initializing database: %v", err)
		}
		defer database.Close()

		// Create SSH client manager
		manager := remote.NewSSHClientManager()

		// Connect to each running instance
		for _, instance := range runningInstances {
			// Look up instance in database to get SSH key name
			dbInstance, err := database.GetInstanceByIP(instance.IP)
			if err != nil {
				log.Printf("Warning: Could not find instance %s in database: %v", instance.IP, err)
				continue
			}

			// Get SSH key from database
			sshKey, err := database.GetSSHKey(dbInstance.SSHKeyName)
			if err != nil {
				log.Printf("Warning: Could not find SSH key %s for instance %s: %v", dbInstance.SSHKeyName, instance.IP, err)
				continue
			}

			// Connect to instance
			err = manager.ConnectToInstance(instance.IP, sshKey.PrivateKey)
			if err != nil {
				log.Printf("Warning: Failed to connect to instance %s: %v", instance.IP, err)
				continue
			}

			fmt.Printf("Connected to instance %s (%s)\n", instance.IP, dbInstance.Name)
		}

		fmt.Println("Successfully connected to cluster instances")
	},
}
