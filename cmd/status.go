/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"

	"github.com/atoniolo76/gotoni/pkg/remote"

	"github.com/spf13/cobra"
)

// statusCmd represents the status command
var statusCmd = &cobra.Command{
	Use:   "status [instance-name]",
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
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
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
	statusCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY env var)")
}
