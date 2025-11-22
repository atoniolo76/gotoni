/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"

	"github.com/atoniolo76/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// provisionCmd represents the provision command
var provisionCmd = &cobra.Command{
	Use:   "provision [instance-name]",
	Short: "Run setup tasks/playbooks on an instance",
	Long: `Run tasks defined in .gotoni/config.yaml on a remote instance.
Tasks can include commands, file uploads, scripts, and background services.`,
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// If API token not provided via flag, get from config or environment
		if apiToken == "" {
			apiToken = client.GetAPIToken()
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		// Load config
		config, err := client.LoadConfig()
		if err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}

		if len(config.Tasks) == 0 {
			log.Fatal("No tasks defined in config. Add tasks to .gotoni/config.yaml")
		}

		var instanceDetails *client.RunningInstance
		if len(args) > 0 {
			instanceName := args[0]
			// Resolve instance name/ID to instance details
			httpClient := client.NewHTTPClient()
			instanceDetails, err = client.ResolveInstance(httpClient, apiToken, instanceName)
			if err != nil {
				log.Fatalf("Failed to resolve instance '%s': %v", instanceName, err)
			}
		} else {
			// Use first running instance if no name provided
			httpClient := client.NewHTTPClient()
			runningInstances, err := client.ListRunningInstances(httpClient, apiToken)
			if err != nil {
				log.Fatalf("Failed to list running instances: %v", err)
			}
			if len(runningInstances) == 0 {
				log.Fatal("No running instances found. Please provide an instance name or launch an instance first.")
			}
			instanceDetails = &runningInstances[0]
			fmt.Printf("Using instance: %s\n", instanceDetails.Name)
		}

		if instanceDetails.IP == "" {
			log.Fatalf("Instance IP address is empty. Instance status: %s. The instance may still be booting. Please wait a moment and try again, or check the instance status with 'gotoni list'.", instanceDetails.Status)
		}

		// Get SSH key
		sshKeyFile, err := client.GetSSHKeyForInstance(instanceDetails.ID)
		if err != nil {
			log.Fatalf("Failed to get SSH key: %v", err)
		}

		// Create SSH manager and connect
		manager := client.NewSSHClientManager()
		defer manager.CloseAllConnections()

		fmt.Printf("Connecting to instance %s (%s)...\n", instanceDetails.Name, instanceDetails.IP)
		if err := manager.ConnectToInstance(instanceDetails.IP, sshKeyFile); err != nil {
			log.Fatalf("Failed to connect via SSH: %v", err)
		}
		fmt.Printf("Connected!\n\n")

		// Filter and execute only command tasks
		commandTasks := client.FilterTasksByType(config.Tasks, "command")
		if len(commandTasks) == 0 {
			log.Fatal("No command tasks found.")
		}

		fmt.Printf("Executing %d command task(s)...\n\n", len(commandTasks))
		if err := client.ExecuteTasks(manager, instanceDetails.IP, commandTasks); err != nil {
			log.Fatalf("Failed to execute tasks: %v", err)
		}

		fmt.Printf("\nAll setup tasks completed successfully!\n")
	},
}

func init() {
	rootCmd.AddCommand(provisionCmd)
	provisionCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")
}
