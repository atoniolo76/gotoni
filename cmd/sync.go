/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/atoniolo76/gotoni/pkg/db"
	"github.com/atoniolo76/gotoni/pkg/remote"

	"github.com/spf13/cobra"
)

// syncCmd represents the sync command
var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync running instances and SSH keys from cloud provider to local database",
	Long: `Sync your running instances and associated SSH keys from your cloud provider to the local gotoni database.
This command fetches currently running instances from the API and updates the local database.
SSH keys are expected to be present in the ~/.ssh/ directory and will be registered in the database if found.

Examples:
  # Sync with Lambda (default)
  gotoni sync

  # Sync with specific provider
  gotoni sync --provider lambda

  # Sync with API token
  gotoni sync --api-token YOUR_TOKEN`,
	Run: func(cmd *cobra.Command, args []string) {
		provider, err := cmd.Flags().GetString("provider")
		if err != nil {
			log.Fatalf("Error getting provider: %v", err)
		}

		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// Create HTTP client
		httpClient := remote.NewHTTPClient()

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

		// List running instances
		runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Error listing running instances: %v", err)
		}

		if len(runningInstances) == 0 {
			fmt.Println("No running instances found.")
			return
		}

		// Get database
		database, err := db.InitDB()
		if err != nil {
			log.Fatalf("Error initializing database: %v", err)
		}
		defer database.Close()

		fmt.Printf("Syncing %d running instance(s)...\n", len(runningInstances))

		syncedCount := 0
		keyCount := 0

		for _, instance := range runningInstances {
			// Determine SSH key name
			var sshKeyName string
			if len(instance.SSHKeyNames) > 0 {
				sshKeyName = instance.SSHKeyNames[0] // Use the first SSH key
			}

			// Create DB instance
			dbInstance := &db.Instance{
				ID:             instance.ID,
				Name:           instance.Name,
				Region:         instance.Region.Name,
				Status:         instance.Status,
				SSHKeyName:     sshKeyName,
				InstanceType:   instance.InstanceType.Name,
				IPAddress:      instance.IP,
				CreatedAt:      time.Now(), // We don't have creation time from API, use current time
			}

			// Save instance to DB
			if err := database.SaveInstance(dbInstance); err != nil {
				log.Printf("Error saving instance %s: %v", instance.ID, err)
				continue
			}

			fmt.Printf("Synced instance: %s (%s)\n", instance.ID, instance.Name)

			// Sync SSH key if present
			if sshKeyName != "" {
				// Check if SSH key file exists
				homeDir, err := os.UserHomeDir()
				if err != nil {
					log.Printf("Error getting home directory: %v", err)
					continue
				}
				keyPath := filepath.Join(homeDir, ".ssh", sshKeyName+".pem")

				if _, err := os.Stat(keyPath); err == nil {
					// Key exists, save to DB
					sshKey := &db.SSHKey{
						Name:       sshKeyName,
						PrivateKey: keyPath,
					}
					if err := database.SaveSSHKey(sshKey); err != nil {
						log.Printf("Error saving SSH key %s: %v", sshKeyName, err)
					} else {
						fmt.Printf("  Registered SSH key: %s\n", sshKeyName)
						keyCount++
					}
				} else {
					fmt.Printf("  Warning: SSH key file not found: %s\n", keyPath)
				}
			}

			syncedCount++
		}

		fmt.Printf("\nSync complete: %d instance(s) synced, %d SSH key(s) registered.\n", syncedCount, keyCount)
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)

	syncCmd.Flags().StringP("provider", "p", "lambda", "Cloud provider to use (lambda or orgo)")
	syncCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY or ORGO_API_KEY env var)")
}