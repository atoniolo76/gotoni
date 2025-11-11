/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"os"
	"strings"

	"toni/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// sshKeysCmd represents the ssh-keys command
var sshKeysCmd = &cobra.Command{
	Use:   "ssh-keys",
	Short: "Manage SSH keys on Lambda Cloud",
	Long:  `List and delete SSH keys on Lambda Cloud.`,
}

// sshKeysListCmd represents the list subcommand
var sshKeysListCmd = &cobra.Command{
	Use:   "list",
	Short: "List SSH keys",
	Long:  `List all SSH keys in your Lambda Cloud account.`,
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// Create HTTP client
		httpClient := client.NewHTTPClient()

		// If API token not provided via flag, get from environment
		if apiToken == "" {
			apiToken = os.Getenv("LAMBDA_API_KEY")
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		sshKeys, err := client.ListSSHKeys(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Error listing SSH keys: %v", err)
		}

		if len(sshKeys) == 0 {
			fmt.Println("No SSH keys found.")
			return
		}

		fmt.Printf("Found %d SSH key(s):\n\n", len(sshKeys))
		for _, key := range sshKeys {
			fmt.Printf("ID: %s\n", key.ID)
			fmt.Printf("Name: %s\n", key.Name)
			// Truncate public key for display if too long
			publicKey := key.PublicKey
			if len(publicKey) > 60 {
				publicKey = publicKey[:60] + "..."
			}
			fmt.Printf("Public Key: %s\n", publicKey)
			fmt.Println("---")
		}
	},
}

// sshKeysDeleteCmd represents the delete subcommand
var sshKeysDeleteCmd = &cobra.Command{
	Use:   "delete [ssh-key-ids...]",
	Short: "Delete SSH keys",
	Long:  `Delete one or more SSH keys from Lambda Cloud by providing their IDs.`,
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		// If API token not provided via flag, get from environment
		if apiToken == "" {
			apiToken = os.Getenv("LAMBDA_API_KEY")
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		// Get SSH key IDs from args or flags
		var sshKeyIDs []string

		if len(args) > 0 {
			// SSH key IDs provided as arguments
			sshKeyIDs = args
		} else {
			// Check for SSH key IDs flag
			idsFlag, err := cmd.Flags().GetStringSlice("ids")
			if err != nil {
				log.Fatalf("Error getting SSH key IDs: %v", err)
			}
			sshKeyIDs = idsFlag
		}

		if len(sshKeyIDs) == 0 {
			log.Fatal("No SSH key IDs provided. Use 'gotoni ssh-keys delete <ssh-key-id>' or 'gotoni ssh-keys delete --ids <id1,id2>'")
		}

		// Create HTTP client
		httpClient := client.NewHTTPClient()

		fmt.Printf("Deleting SSH key(s): %s\n", strings.Join(sshKeyIDs, ", "))

		var deletedCount int
		for _, sshKeyID := range sshKeyIDs {
			if err := client.DeleteSSHKey(httpClient, apiToken, sshKeyID); err != nil {
				log.Printf("Error deleting SSH key %s: %v", sshKeyID, err)
				continue
			}
			fmt.Printf("Successfully deleted SSH key: %s\n", sshKeyID)
			deletedCount++
		}

		if deletedCount > 0 {
			fmt.Printf("\nSuccessfully deleted %d SSH key(s)\n", deletedCount)
		}
	},
}

func init() {
	rootCmd.AddCommand(sshKeysCmd)

	// Add list subcommand
	sshKeysCmd.AddCommand(sshKeysListCmd)
	sshKeysListCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")

	// Add delete subcommand
	sshKeysCmd.AddCommand(sshKeysDeleteCmd)
	sshKeysDeleteCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")
	sshKeysDeleteCmd.Flags().StringSliceP("ids", "i", []string{}, "SSH key IDs to delete (can also be provided as arguments)")
}
