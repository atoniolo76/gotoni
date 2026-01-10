/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"strings"

	"github.com/atoniolo76/gotoni/pkg/remote"

	"github.com/spf13/cobra"
)

// runCmd represents the run command
var runCmd = &cobra.Command{
	Use:   "run [instance-name] <command>",
	Short: "Run a command on a remote instance/computer",
	Long: `Run a command directly on a remote instance (Lambda) via SSH or computer (Orgo) via API.
Examples:
  gotoni run "ls -la"                    # Run on first running instance
  gotoni run my-instance "ls -la"        # Run on specific instance
  gotoni run --provider orgo "ls -la"   # Run on Orgo computer`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		provider, err := cmd.Flags().GetString("provider")
		if err != nil {
			log.Fatalf("Error getting provider: %v", err)
		}

		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// If API token not provided via flag, get from environment based on provider
		if apiToken == "" {
			if provider == "orgo" {
				apiToken = remote.GetAPITokenForProvider(remote.CloudProviderOrgo)
			} else {
				apiToken = remote.GetAPITokenForProvider(remote.CloudProviderLambda)
			}
			if apiToken == "" {
				if provider == "orgo" {
					log.Fatal("API token not provided via --api-token flag or ORGO_API_KEY environment variable")
				} else {
					log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
				}
			}
		}

		var instanceID string
		var instanceName string
		var command string

		// Parse arguments: [instance-name] <command>
		if len(args) == 1 {
			// Only command provided, use first running instance/computer
			command = args[0]
			httpClient := remote.NewHTTPClient()
			runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
			if err != nil {
				log.Fatalf("Failed to list running instances: %v", err)
			}
			if len(runningInstances) == 0 {
				resourceType := "instances"
				if provider == "orgo" {
					resourceType = "computers"
				}
				log.Fatalf("No running %s found. Please provide an instance/computer name or launch one first.", resourceType)
			}
			instanceID = runningInstances[0].ID
			instanceName = runningInstances[0].Name
			resourceType := "instance"
			if provider == "orgo" {
				resourceType = "computer"
			}
			fmt.Printf("Using %s: %s\n", resourceType, instanceName)
		} else {
			// Instance/computer name and command provided
			instanceName = args[0]
			command = strings.Join(args[1:], " ")

			// Resolve instance/computer name/ID to instance details
			httpClient := remote.NewHTTPClient()
			instanceDetails, err := remote.ResolveInstance(httpClient, apiToken, instanceName)
			if err != nil {
				resourceType := "instance"
				if provider == "orgo" {
					resourceType = "computer"
				}
				log.Fatalf("Failed to resolve %s '%s': %v", resourceType, instanceName, err)
			}
			instanceID = instanceDetails.ID
		}

		// Execute command based on provider
		if provider == "orgo" {
			// Use Orgo API for command execution
			fmt.Printf("Executing on Orgo computer %s: %s\n\n", instanceName, command)

			// Create Orgo provider instance
			orgoProvider := remote.NewOrgoProvider()

			// Execute command using Orgo API
			output, err := orgoProvider.ExecuteBashCommand(instanceID, command)
			if err != nil {
				log.Fatalf("Command failed: %v", err)
			}

			fmt.Printf("%s\n", output)
		} else {
			// Use SSH for Lambda instances
			httpClient := remote.NewHTTPClient()
			instanceDetails, err := remote.GetInstance(httpClient, apiToken, instanceID)
			if err != nil {
				log.Fatalf("Failed to get instance details: %v", err)
			}

			if instanceDetails.IP == "" {
				log.Fatalf("Instance IP address is empty. Instance status: %s. The instance may still be booting. Please wait a moment and try again, or check the instance status with 'gotoni list'.", instanceDetails.Status)
			}

			// Get SSH key
			sshKeyFile, err := remote.GetSSHKeyForInstance(instanceDetails.ID)
			if err != nil {
				log.Fatalf("Failed to get SSH key: %v", err)
			}

			// Create SSH manager and connect
			manager := remote.NewSSHClientManager()
			defer manager.CloseAllConnections()

			fmt.Printf("Connecting to instance %s (%s)...\n", instanceDetails.Name, instanceDetails.IP)
			if err := manager.ConnectToInstance(instanceDetails.IP, sshKeyFile); err != nil {
				log.Fatalf("Failed to connect via SSH: %v", err)
			}
			fmt.Printf("Connected!\n\n")

			// Execute command
			fmt.Printf("Executing: %s\n\n", command)
			output, err := manager.ExecuteCommand(instanceDetails.IP, command)
			if err != nil {
				log.Fatalf("Command failed: %v\nOutput: %s", err, output)
			}

			fmt.Printf("%s\n", output)
		}
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().StringP("provider", "p", "lambda", "Cloud provider to use (lambda or orgo)")
	runCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY or ORGO_API_KEY env var)")
}
