/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"os"
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
			} else if provider == "modal" {
				apiToken = os.Getenv("MODAL_TOKEN_ID")
			} else {
				apiToken = remote.GetAPITokenForProvider(remote.CloudProviderLambda)
			}
			if apiToken == "" {
				if provider == "orgo" {
					log.Fatal("API token not provided via --api-token flag or ORGO_API_KEY environment variable")
				} else if provider == "modal" {
					log.Fatal("API token not provided via --api-token flag or MODAL_TOKEN_ID environment variable")
				} else {
					log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
				}
			}
		}

		// Create provider based on flag
		var cloudProvider remote.CloudProvider
		switch provider {
		case "modal":
			cloudProvider = remote.NewModalProvider()
		case "orgo":
			cloudProvider = remote.NewOrgoProvider()
		default:
			cloudProvider = remote.NewLambdaProvider()
		}

		var instanceID string
		var instanceName string
		var command string

		// Parse arguments: [instance-name] <command>
		if len(args) == 1 {
			// Only command provided, use first running instance/computer
			command = args[0]
			httpClient := remote.NewHTTPClient()
			runningInstances, err := cloudProvider.ListRunningInstances(httpClient, apiToken)
			if err != nil {
				log.Fatalf("Failed to list running instances: %v", err)
			}
			if len(runningInstances) == 0 {
				resourceType := "instances"
				if provider == "orgo" {
					resourceType = "computers"
				} else if provider == "modal" {
					resourceType = "sandboxes"
				}
				log.Fatalf("No running %s found. Please provide an instance/computer/sandbox name or launch one first.", resourceType)
			}
			instanceID = runningInstances[0].ID
			instanceName = runningInstances[0].Name
			resourceType := "instance"
			if provider == "orgo" {
				resourceType = "computer"
			} else if provider == "modal" {
				resourceType = "sandbox"
			}
			fmt.Printf("Using %s: %s\n", resourceType, instanceName)
		} else {
			// Instance/computer name and command provided
			instanceName = args[0]
			command = strings.Join(args[1:], " ")

			// Resolve instance/computer name/ID to instance details
			httpClient := remote.NewHTTPClient()
			instanceDetails, err := cloudProvider.GetInstance(httpClient, apiToken, instanceName)
			if err != nil {
				// Try listing to find by name
				instances, listErr := cloudProvider.ListRunningInstances(httpClient, apiToken)
				if listErr == nil {
					for _, inst := range instances {
						if inst.Name == instanceName || inst.ID == instanceName {
							instanceDetails = &inst
							break
						}
					}
				}
				if instanceDetails == nil {
					resourceType := "instance"
					if provider == "orgo" {
						resourceType = "computer"
					} else if provider == "modal" {
						resourceType = "sandbox"
					}
					log.Fatalf("Failed to resolve %s '%s': %v", resourceType, instanceName, err)
				}
			}
			instanceID = instanceDetails.ID
		}

		// Execute command based on provider
		if provider == "orgo" || provider == "modal" {
			// Use provider's ExecuteBashCommand
			resourceType := "computer"
			if provider == "modal" {
				resourceType = "sandbox"
			}
			fmt.Printf("Executing on %s %s: %s\n\n", provider, resourceType, instanceName)

			output, err := cloudProvider.ExecuteBashCommand(instanceID, command)
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
	runCmd.Flags().StringP("provider", "p", "lambda", "Cloud provider to use (lambda, orgo, or modal)")
	runCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY, ORGO_API_KEY, or MODAL_TOKEN_ID env vars)")
}
