/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"time"

	"toni/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// startCmd represents the start command
var startCmd = &cobra.Command{
	Use:   "start [instance-id]",
	Short: "Start service tasks on an instance",
	Long: `Start service tasks (background services) defined in .gotoni/config.yaml on a remote instance.
Services run in tmux sessions for easy management.`,
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

		// Load config
		config, err := client.LoadConfig()
		if err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}

		var instanceID string
		if len(args) > 0 {
			instanceID = args[0]
		} else {
			// Use first running instance if no ID provided
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

		// Filter and execute only service tasks
		serviceTasks := client.FilterTasksByType(config.Tasks, "service")
		if len(serviceTasks) == 0 {
			log.Fatal("No service tasks found. Add service tasks to .gotoni/config.yaml")
		}

		fmt.Printf("Starting %d service task(s)...\n\n", len(serviceTasks))
		if err := client.ExecuteTasks(manager, instanceDetails.IP, serviceTasks); err != nil {
			log.Fatalf("Failed to start services: %v", err)
		}

		fmt.Printf("\nAll services started successfully!\n")
		fmt.Printf("Use 'systemctl --user list-units' or 'gotoni status' to see running services.\n")
	},
}

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")
}
