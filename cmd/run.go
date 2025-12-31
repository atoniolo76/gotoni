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
	Short: "Run a command on a remote instance",
	Long: `Run a command directly on a remote instance via SSH.
Example: gotoni run "ls -la"`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// If API token not provided via flag, get from config or environment
		if apiToken == "" {
			apiToken = remote.GetAPIToken()
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		var instanceDetails *remote.RunningInstance
		var command string

		// Parse arguments: [instance-name] <command>
		if len(args) == 1 {
			// Only command provided, use first running instance
			command = args[0]
			httpClient := remote.NewHTTPClient()
			runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
			if err != nil {
				log.Fatalf("Failed to list running instances: %v", err)
			}
			if len(runningInstances) == 0 {
				log.Fatal("No running instances found. Please provide an instance name or launch an instance first.")
			}
			instanceDetails = &runningInstances[0]
			fmt.Printf("Using instance: %s\n", instanceDetails.Name)
		} else {
			// Instance name and command provided
			instanceName := args[0]
			command = strings.Join(args[1:], " ")

			// Resolve instance name/ID to instance details
			httpClient := remote.NewHTTPClient()
			instanceDetails, err = remote.ResolveInstance(httpClient, apiToken, instanceName)
			if err != nil {
				log.Fatalf("Failed to resolve instance '%s': %v", instanceName, err)
			}
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
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")
}
