/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/atoniolo76/gotoni/pkg/db"
	"github.com/atoniolo76/gotoni/pkg/remote"

	"github.com/spf13/cobra"
)

// syncCmd represents the sync command
var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync SSH keys from running instances to local database",
	Long: `Sync SSH keys used by your running instances to the local gotoni database.
This command fetches running instances from Lambda API and registers their SSH keys locally.
Instance state is managed by Lambda API - only SSH key file paths are stored locally.

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

		// List running instances from Lambda API (source of truth)
		runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Error listing running instances: %v", err)
		}

		if len(runningInstances) == 0 {
			fmt.Println("No running instances found.")
			return
		}

		// Get database for SSH key storage
		database, err := db.InitDB()
		if err != nil {
			log.Fatalf("Error initializing database: %v", err)
		}
		defer database.Close()

		fmt.Printf("Found %d running instance(s) from API...\n", len(runningInstances))
		fmt.Println("Syncing SSH keys...")

		keyCount := 0
		keysNotFound := 0

		// Collect unique SSH key names
		keyNames := make(map[string]bool)
		for _, instance := range runningInstances {
			for _, keyName := range instance.SSHKeyNames {
				keyNames[keyName] = true
			}
		}

		// Get home directory
		homeDir, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("Error getting home directory: %v", err)
		}
		sshDir := filepath.Join(homeDir, ".ssh")

		// Register each SSH key if the file exists locally
		for keyName := range keyNames {
			// Try common file naming patterns
			possiblePaths := []string{
				filepath.Join(sshDir, keyName+".pem"),
				filepath.Join(sshDir, keyName),
			}

			var foundPath string
			for _, path := range possiblePaths {
				if _, err := os.Stat(path); err == nil {
					foundPath = path
					break
				}
			}

			if foundPath != "" {
				// Key file exists, save to DB
				sshKey := &db.SSHKey{
					Name:       keyName,
					PrivateKey: foundPath,
				}
				if err := database.SaveSSHKey(sshKey); err != nil {
					log.Printf("Error saving SSH key %s: %v", keyName, err)
				} else {
					fmt.Printf("  ✅ Registered SSH key: %s -> %s\n", keyName, foundPath)
					keyCount++
				}
			} else {
				fmt.Printf("  ⚠️  SSH key file not found locally: %s\n", keyName)
				fmt.Printf("     Expected at: %s.pem or %s\n", filepath.Join(sshDir, keyName), filepath.Join(sshDir, keyName))
				keysNotFound++
			}
		}

		fmt.Printf("\nSync complete: %d SSH key(s) registered", keyCount)
		if keysNotFound > 0 {
			fmt.Printf(", %d key(s) not found locally", keysNotFound)
		}
		fmt.Println()

		if keysNotFound > 0 {
			fmt.Println("\nTo add missing keys:")
			fmt.Println("  1. Download the private key file from Lambda Cloud dashboard")
			fmt.Println("  2. Save it to ~/.ssh/<key-name>.pem")
			fmt.Println("  3. Run 'gotoni sync' again")
		}
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)

	syncCmd.Flags().StringP("provider", "p", "lambda", "Cloud provider to use (lambda or orgo)")
	syncCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY or ORGO_API_KEY env var)")
}