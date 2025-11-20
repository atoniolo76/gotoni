/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/atoniolo76/gotoni/pkg/client"

	"github.com/spf13/cobra"
)

// sshKeysCmd represents the ssh-keys command
var sshKeysCmd = &cobra.Command{
	Use:   "ssh-keys",
	Short: "Manage SSH keys on Lambda Cloud",
	Long:  `List, add, delete SSH keys, or get SSH key for an instance.`,
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

// sshKeysAddCmd represents the add subcommand
var sshKeysAddCmd = &cobra.Command{
	Use:   "add <key-path>",
	Short: "Add an existing SSH key to gotoni configuration",
	Long:  `Add an existing SSH key file to the gotoni configuration. The key will be copied to the ssh/ directory and registered in the config.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		keyPath := args[0]
		keyName, err := cmd.Flags().GetString("name")
		if err != nil {
			log.Fatalf("Error getting key name: %v", err)
		}

		addedKeyName, targetPath, err := client.AddExistingSSHKey(keyPath, keyName)
		if err != nil {
			log.Fatalf("Error adding SSH key: %v", err)
		}

		fmt.Printf("Successfully added SSH key\n")
		fmt.Printf("  Source: %s\n", keyPath)
		fmt.Printf("  Name: %s\n", addedKeyName)
		fmt.Printf("  Path: %s\n", targetPath)
		fmt.Printf("\nThe key has been copied to %s and added to your gotoni configuration.\n", targetPath)
	},
}

// sshKeysGetCmd represents the get subcommand
var sshKeysGetCmd = &cobra.Command{
	Use:   "get <instance-id>",
	Short: "Get SSH key associated with an instance",
	Long:  `Show the SSH key file path associated with a specific instance. Optionally create a symlink in the current directory.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		instanceID := args[0]
		createLink, err := cmd.Flags().GetBool("link")
		if err != nil {
			log.Fatalf("Error getting link flag: %v", err)
		}

		// Get SSH key file path for the instance
		sshKeyPath, err := client.GetSSHKeyForInstance(instanceID)
		if err != nil {
			log.Fatalf("Error getting SSH key for instance: %v", err)
		}

		// Check if the SSH key file exists
		if _, err := os.Stat(sshKeyPath); os.IsNotExist(err) {
			log.Fatalf("SSH key file does not exist: %s", sshKeyPath)
		}

		// Get absolute path of the SSH key file
		absSSHKeyPath, err := filepath.Abs(sshKeyPath)
		if err != nil {
			log.Fatalf("Error getting absolute path: %v", err)
		}

		// Get the key name from the path
		keyName := strings.TrimSuffix(filepath.Base(sshKeyPath), ".pem")

		fmt.Printf("Instance: %s\n", instanceID)
		fmt.Printf("SSH Key Name: %s\n", keyName)
		fmt.Printf("SSH Key Path: %s\n", absSSHKeyPath)

		if createLink {
			// Get current working directory
			cwd, err := os.Getwd()
			if err != nil {
				log.Fatalf("Error getting current directory: %v", err)
			}

			// Create symlink in current directory
			linkPath := filepath.Join(cwd, keyName+".pem")

			// Remove existing symlink or file if it exists
			if _, err := os.Lstat(linkPath); err == nil {
				if err := os.Remove(linkPath); err != nil {
					log.Fatalf("Error removing existing file/symlink: %v", err)
				}
			}

			// Create the symlink
			if err := os.Symlink(absSSHKeyPath, linkPath); err != nil {
				log.Fatalf("Error creating symlink: %v", err)
			}

			fmt.Printf("\n✓ Created symlink: %s -> %s\n", linkPath, absSSHKeyPath)
		}
	},
}

func init() {
	rootCmd.AddCommand(sshKeysCmd)

	// Add list subcommand
	sshKeysCmd.AddCommand(sshKeysListCmd)
	sshKeysListCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY env var)")

	// Add get subcommand
	sshKeysCmd.AddCommand(sshKeysGetCmd)
	sshKeysGetCmd.Flags().BoolP("link", "l", false, "Create a symlink to the SSH key in the current directory")

	// Add delete subcommand
	sshKeysCmd.AddCommand(sshKeysDeleteCmd)
	sshKeysDeleteCmd.Flags().StringP("api-token", "a", "", "API token for cloud provider (can also be set via LAMBDA_API_KEY env var)")
	sshKeysDeleteCmd.Flags().StringSliceP("ids", "i", []string{}, "SSH key IDs to delete (can also be provided as arguments)")

	// Add add subcommand
	sshKeysCmd.AddCommand(sshKeysAddCmd)
	sshKeysAddCmd.Flags().StringP("name", "n", "", "Name for the SSH key (defaults to filename without extension)")
}
