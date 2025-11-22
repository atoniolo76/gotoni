/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"strings"

	"github.com/atoniolo76/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// logsCmd represents the logs command
var logsCmd = &cobra.Command{
	Use:   "logs [instance-name] [service-name]",
	Short: "View logs from a systemd service",
	Long: `View logs from a systemd user service running on a remote instance using journalctl.
If service-name is not provided, shows logs from all services.`,
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

		lines, err := cmd.Flags().GetInt("lines")
		if err != nil {
			log.Fatalf("Error getting lines flag: %v", err)
		}
		if lines <= 0 {
			lines = 100 // Default
		}

		var instanceDetails *client.RunningInstance
		var sessionName string

		if len(args) == 0 {
			log.Fatal("Please provide at least an instance name. Use 'gotoni logs <instance-name> [service-name]'")
		} else if len(args) == 1 {
			instanceName := args[0]
			// Resolve instance name/ID to instance details
			httpClient := client.NewHTTPClient()
			instanceDetails, err = client.ResolveInstance(httpClient, apiToken, instanceName)
			if err != nil {
				log.Fatalf("Failed to resolve instance '%s': %v", instanceName, err)
			}
		} else {
			instanceName := args[0]
			sessionName = args[1]

			// Resolve instance name/ID to instance details
			httpClient := client.NewHTTPClient()
			instanceDetails, err = client.ResolveInstance(httpClient, apiToken, instanceName)
			if err != nil {
				log.Fatalf("Failed to resolve instance '%s': %v", instanceName, err)
			}
		}
		if err != nil {
			log.Fatalf("Failed to get instance details: %v", err)
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

		if sessionName != "" {
			// Show logs for specific service
			logs, err := manager.GetSystemdLogs(instanceDetails.IP, sessionName, lines)
			if err != nil {
				log.Fatalf("Failed to get logs: %v", err)
			}
			fmt.Printf("=== Logs for service '%s' (last %d lines) ===\n\n", sessionName, lines)
			fmt.Println(logs)
		} else {
			// List all services and show their logs
			services, err := manager.ListSystemdServices(instanceDetails.IP)
			if err != nil {
				log.Fatalf("Failed to list systemd services: %v", err)
			}

			if len(services) == 0 {
				fmt.Println("No active services found.")
				return
			}

			for _, service := range services {
				logs, err := manager.GetSystemdLogs(instanceDetails.IP, service, lines)
				if err != nil {
					fmt.Printf("Error getting logs for %s: %v\n", service, err)
					continue
				}
				fmt.Printf("=== Logs for service '%s' (last %d lines) ===\n\n", service, lines)
				fmt.Println(logs)
				fmt.Println(strings.Repeat("=", 60))
				fmt.Println()
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
	logsCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")
	logsCmd.Flags().IntP("lines", "n", 100, "Number of lines to show (default: 100)")
}
