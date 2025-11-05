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

// statusCmd represents the status command
var statusCmd = &cobra.Command{
	Use:   "status [instance-id]",
	Short: "Check status of services running on an instance",
	Long: `Check the status of services (tmux sessions) running on a remote instance.
Shows all active tmux sessions and their status.`,
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

		// List tmux sessions
		sessions, err := manager.ListTmuxSessions(instanceDetails.IP)
		if err != nil {
			log.Fatalf("Failed to list tmux sessions: %v", err)
		}

		if len(sessions) == 0 {
			fmt.Println("No active tmux sessions found.")
			return
		}

		fmt.Printf("Active services (tmux sessions):\n\n")
		for _, session := range sessions {
			// Check if session is still alive
			statusCmd := fmt.Sprintf("tmux has-session -t %s 2>/dev/null && echo 'active' || echo 'dead'", session)
			status, _ := manager.ExecuteCommand(instanceDetails.IP, statusCmd)
			statusStr := strings.TrimSpace(status)
			if statusStr == "active" {
				fmt.Printf("  ✓ %s (active)\n", session)
			} else {
				fmt.Printf("  ✗ %s (dead)\n", session)
			}
		}
		fmt.Println()
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")
}
