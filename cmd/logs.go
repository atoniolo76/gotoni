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

// logsCmd represents the logs command
var logsCmd = &cobra.Command{
	Use:   "logs [instance-id] [session-name]",
	Short: "View logs from a tmux session/service",
	Long: `View logs from a tmux session running on a remote instance.
If session-name is not provided, shows logs from all sessions.`,
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

		lines, err := cmd.Flags().GetInt("lines")
		if err != nil {
			log.Fatalf("Error getting lines flag: %v", err)
		}
		if lines <= 0 {
			lines = 100 // Default
		}

		var instanceID string
		var sessionName string

		if len(args) == 0 {
			log.Fatal("Please provide at least an instance ID. Use 'gotoni logs <instance-id> [session-name]'")
		} else if len(args) == 1 {
			instanceID = args[0]
		} else {
			instanceID = args[0]
			sessionName = args[1]
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

		if sessionName != "" {
			// Show logs for specific session
			logs, err := manager.GetTmuxLogs(instanceDetails.IP, sessionName, lines)
			if err != nil {
				log.Fatalf("Failed to get logs: %v", err)
			}
			fmt.Printf("=== Logs for session '%s' (last %d lines) ===\n\n", sessionName, lines)
			fmt.Println(logs)
		} else {
			// List all sessions and show their logs
			sessions, err := manager.ListTmuxSessions(instanceDetails.IP)
			if err != nil {
				log.Fatalf("Failed to list tmux sessions: %v", err)
			}

			if len(sessions) == 0 {
				fmt.Println("No active tmux sessions found.")
				return
			}

			for _, session := range sessions {
				logs, err := manager.GetTmuxLogs(instanceDetails.IP, session, lines)
				if err != nil {
					fmt.Printf("Error getting logs for %s: %v\n", session, err)
					continue
				}
				fmt.Printf("=== Logs for session '%s' (last %d lines) ===\n\n", session, lines)
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
