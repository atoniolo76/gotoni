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

// statusCmd represents the status command
var statusCmd = &cobra.Command{
	Use:   "status [instance-id]",
	Short: "Check status of services running on an instance",
	Long: `Check the status of systemd user services running on a remote instance.
Shows all active services and their status.`,
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// If API token not provided via flag, get from config or environment
		if apiToken == "" {
			apiToken = client.GetAPIToken()
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or appropriate environment variable (LAMBDA_API_KEY or NEBIUS_API_KEY based on GOTONI_CLOUD)")
			}
		}

		var instanceID string
		if len(args) > 0 {
			instanceID = args[0]
		} else {
			// Use first running instance if no ID provided
			httpClient := client.NewHTTPClient()
			runningInstances, err := client.ListRunningInstances(httpClient, apiToken)
			if err != nil {
				log.Fatalf("Failed to list running instances: %v", err)
			}
			if len(runningInstances) == 0 {
				log.Fatal("No running instances found. Please provide an instance ID or launch an instance first.")
			}
			instanceID = runningInstances[0].ID
			fmt.Printf("Using instance: %s\n", instanceID)
		}

		// Get instance details
		httpClient := client.NewHTTPClient()

		instanceDetails, err := client.GetInstance(httpClient, apiToken, instanceID)
		if err != nil {
			log.Fatalf("Failed to get instance details: %v", err)
		}

		if instanceDetails.IP == "" {
			log.Fatalf("Instance IP address is empty. Instance status: %s. The instance may still be booting. Please wait a moment and try again, or check the instance status with 'gotoni list'.", instanceDetails.Status)
		}

		// Get SSH key
		sshKeyFile, err := client.GetSSHKeyForInstance(instanceID)
		if err != nil {
			log.Fatalf("Failed to get SSH key: %v", err)
		}

		// Create SSH manager and connect
		manager := client.NewSSHClientManager()
		defer manager.CloseAllConnections()

		fmt.Printf("Connecting to instance %s (%s)...\n", instanceID, instanceDetails.IP)
		if err := manager.ConnectToInstance(instanceDetails.IP, sshKeyFile); err != nil {
			log.Fatalf("Failed to connect via SSH: %v", err)
		}
		fmt.Printf("Connected!\n\n")

		// List systemd services
		services, err := manager.ListSystemdServices(instanceDetails.IP)
		if err != nil {
			log.Fatalf("Failed to list systemd services: %v", err)
		}

		if len(services) == 0 {
			fmt.Println("No active services found.")
			return
		}

		fmt.Printf("Active services (systemd):\n\n")
		for _, service := range services {
			// Check service status
			status, err := manager.GetSystemdServiceStatus(instanceDetails.IP, service)
			if err != nil {
				fmt.Printf("  ? %s (unknown)\n", service)
				continue
			}
			if status == "active" {
				fmt.Printf("  ✓ %s (active)\n", service)
			} else {
				fmt.Printf("  ✗ %s (%s)\n", service, status)
			}
		}
		fmt.Println()
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY or NEBIUS_API_KEY env vars based on GOTONI_CLOUD)")
}
