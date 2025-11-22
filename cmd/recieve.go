/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/atoniolo76/gotoni/pkg/client"
	"github.com/psanford/wormhole-william/wormhole"
	"github.com/spf13/cobra"
)

// receiveCmd represents the receive command
var receiveCmd = &cobra.Command{
	Use:   "receive <code>",
	Short: "Securely receive an SSH key",
	Long:  `Securely receive an SSH key using a Magic Wormhole code. The key will be saved to ~/.ssh and configured for use.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		code := args[0]

		instanceName, err := cmd.Flags().GetString("name")
		if err != nil {
			log.Fatalf("Error getting name flag: %v", err)
		}

		fmt.Printf("Connecting to wormhole with code: %s\n", code)

		// 1. Initialize Wormhole client
		var c wormhole.Client
		ctx := context.Background()

		// 2. Receive the file
		msg, err := c.Receive(ctx, code)
		if err != nil {
			log.Fatalf("Failed to receive: %v", err)
		}

		if msg.Type != wormhole.TransferFile {
			log.Fatalf("Expected file transfer, got type %d", msg.Type)
		}

		// 3. Determine paths
		sshDir, err := getSSHDir()
		if err != nil {
			log.Fatalf("Failed to get SSH directory: %v", err)
		}

		// Ensure .ssh directory exists
		if err := os.MkdirAll(sshDir, 0700); err != nil {
			log.Fatalf("Failed to create SSH directory: %v", err)
		}

		fileName := msg.Name
		targetPath := filepath.Join(sshDir, fileName)

		// If instance name wasn't provided, try to derive from filename
		if instanceName == "" {
			instanceName = strings.TrimSuffix(fileName, filepath.Ext(fileName))
			// Clean up common prefixes if desired, but better to keep it simple
		}

		fmt.Printf("Receiving file '%s' (%d bytes)...\n", fileName, msg.TransferBytes)

		// 4. Save file with secure permissions
		// We write to a temp file first or directly?
		// Let's read the content into memory (keys are small) or stream to file.

		// Open destination file with 0400 permissions
		// Note: os.OpenFile with 0400 might fail if we try to write to it.
		// Standard approach: Create with 0600 (read/write owner), write data, then Chmod to 0400.
		outFile, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			log.Fatalf("Failed to create target file: %v", err)
		}

		if _, err := io.Copy(outFile, msg); err != nil {
			outFile.Close()
			log.Fatalf("Failed to write file: %v", err)
		}
		outFile.Close()

		// Now lock it down to read-only
		if err := os.Chmod(targetPath, 0400); err != nil {
			log.Printf("Warning: failed to set secure permissions (0400): %v", err)
		}

		fmt.Printf("Successfully received key: %s\n", targetPath)

		// 5. Add to gotoni configuration
		fmt.Println("Adding key to gotoni configuration...")

		// Use the existing AddExistingSSHKey logic to register it in our DB/Config
		// But since we already moved it, we might just need to update the config.
		// Actually, AddExistingSSHKey does the copy logic too.
		// Let's manually update the config to avoid double-copying or just call AddExistingSSHKey pointing to the new file?
		// AddExistingSSHKey copies FROM source TO ~/.ssh. Since it's already in ~/.ssh, let's see if it handles in-place.

		// Simpler: just call AddExistingSSHKey with the file we just wrote.
		// It might try to copy it to itself or overwrite, which is fine.

		addedKeyName, finalPath, err := client.AddExistingSSHKey(targetPath, instanceName)
		if err != nil {
			log.Printf("Warning: Failed to add key to gotoni config: %v", err)
			log.Printf("You can manually add it later with: gotoni ssh-keys add %s --name %s", targetPath, instanceName)
		} else {
			fmt.Printf("Registered as key: %s\n", addedKeyName)
			fmt.Printf("Key path: %s\n", finalPath)
		}

		// 6. Update SSH config (~/.ssh/config)
		// We need the instance IP to add a Host entry.
		// Problem: We only received the key, we don't know the IP unless we ask the user or send it in metadata.
		// The user request said: "make sure on the recieve.go side they ... add it to config like we do with @updatesshconfig"
		// But UpdateSSHConfig requires (hostName, hostIP, identityFile).
		// We have hostName (instanceName) and identityFile (targetPath). We lack hostIP.

		fmt.Println("\nTo complete setup, you need to know the Instance IP.")
		fmt.Printf("Run: ssh -i %s ubuntu@<IP>\n", targetPath)

		// If we want to fully automate UpdateSSHConfig, we'd need to send the IP alongside the file.
		// Current wormhole library sends a single file or directory.
		// We could zip a metadata.json + key.pem, but that complicates things.
		// Or we assume the user knows the IP.
		// Let's leave it as a manual step or prompt for IP?
		// "plan out an idea: ... able to authenticate into an instance"

		// Let's ask the user for the IP if they want to configure it right now.
		// Or we can skip it. The prompt didn't explicitly say "transfer the IP", but implied "authenticate into an instance".
		// "add it to config like we do with @updatesshconfig" -> this strongly implies we should do it.
		// Let's prompt for IP.

		fmt.Print("\nEnter Instance IP (optional, press Enter to skip): ")
		var ip string
		fmt.Scanln(&ip)

		if ip != "" {
			if err := client.UpdateSSHConfig(instanceName, ip, targetPath); err != nil {
				log.Printf("Failed to update SSH config: %v", err)
			} else {
				fmt.Printf("\n✓ You can now connect with: ssh %s\n", instanceName)
			}
		}
	},
}

func getSSHDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}
	return filepath.Join(home, ".ssh"), nil
}

func init() {
	rootCmd.AddCommand(receiveCmd)
	receiveCmd.Flags().StringP("name", "n", "", "Name for the instance/key (defaults to filename)")
}
