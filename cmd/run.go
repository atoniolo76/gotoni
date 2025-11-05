/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"toni/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// runCmd represents the run command
var runCmd = &cobra.Command{
	Use:   "run [instance-id] <command>",
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
			apiToken = client.GetAPIToken()
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag, config.yaml, or LAMBDA_API_KEY environment variable")
			}
		}

		var instanceID string
		var command string

		// Parse arguments: [instance-id] <command>
		if len(args) == 1 {
			// Only command provided, use first running instance
			command = args[0]
			httpClient := &http.Client{
				Timeout:   time.Duration(30) * time.Second,
				Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
			}
			runningInstances, err := client.ListRunningInstances(httpClient, apiToken)
			if err != nil {
				log.Fatalf("Failed to list running instances: %v", err)
			}
			if len(runningInstances) == 0 {
				log.Fatal("No running instances found. Please provide an instance ID or launch an instance first.")
			}
			instanceID = runningInstances[0].ID
			fmt.Printf("Using instance: %s\n", instanceID)
		} else {
			// Instance ID and command provided
			instanceID = args[0]
			command = strings.Join(args[1:], " ")
		}

		// Get instance details
		httpClient := &http.Client{
			Timeout:   time.Duration(30) * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		}

		instanceDetails, err := client.GetInstance(httpClient, apiToken, instanceID)
		if err != nil {
			log.Fatalf("Failed to get instance details: %v", err)
		}

		if instanceDetails.IP == "" {
			log.Fatal("Instance IP address is empty")
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

