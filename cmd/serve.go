package cmd

import (
	"fmt"
	"log"

	"github.com/atoniolo76/gotoni/pkg/remote"

	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve your favorite models and processes on vLLM across instances and regions",
	Long:  "Serve your favorite models and processes on vLLM across instances and regions.",
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

		// Create SSH client manager
		manager := remote.NewSSHClientManager()

		// Connect to each running instance
		for _, instance := range runningInstances {
			// Get SSH key using Lambda API key names + local file lookup
			sshKeyPath, err := remote.GetSSHKeyFileForInstance(&instance)
			if err != nil {
				log.Printf("Warning: Could not find SSH key for instance %s: %v", instance.IP, err)
				continue
			}

			// Connect to instance
			err = manager.ConnectToInstance(instance.IP, sshKeyPath)
			if err != nil {
				log.Printf("Warning: Failed to connect to instance %s: %v", instance.IP, err)
				continue
			}

			fmt.Printf("Connected to instance %s (%s)\n", instance.IP, instance.Name)
		}

		fmt.Println("Successfully connected to cluster instances")
	},
}
