/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/atoniolo76/gotoni/pkg/client"
	"github.com/atoniolo76/gotoni/pkg/db"
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

		// Read content into memory
		data, err := io.ReadAll(msg)
		if err != nil {
			log.Fatalf("Failed to read received data: %v", err)
		}

		var payload SharePayload
		isJSON := false

		// Try to unmarshal as JSON payload
		if strings.HasSuffix(msg.Name, ".json") {
			if err := json.Unmarshal(data, &payload); err == nil {
				isJSON = true
			} else {
				log.Printf("Warning: Received .json file but failed to parse payload: %v", err)
			}
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

		var fileName string
		var fileContent []byte
		var instanceIP string

		if isJSON {
			fmt.Printf("Received configuration for instance '%s'\n", payload.InstanceName)
			fileName = payload.KeyName
			fileContent = []byte(payload.KeyData)
			instanceIP = payload.InstanceIP
			if instanceName == "" {
				instanceName = payload.InstanceName
			}
		} else {
			// Legacy/Direct file transfer
			fileName = msg.Name
			fileContent = data
			fmt.Printf("Receiving file '%s' (%d bytes)...\n", fileName, len(data))
		}

		// If instance name still empty, derive from filename
		if instanceName == "" {
			instanceName = strings.TrimSuffix(fileName, filepath.Ext(fileName))
		}

		targetPath := filepath.Join(sshDir, fileName)

		// 4. Save file with secure permissions
		// Open destination file with 0600 permissions (read/write owner)
		err = os.WriteFile(targetPath, fileContent, 0600)
		if err != nil {
			log.Fatalf("Failed to write key file: %v", err)
		}

		// Now lock it down to read-only (0400)
		if err := os.Chmod(targetPath, 0400); err != nil {
			log.Printf("Warning: failed to set secure permissions (0400): %v", err)
		}

	fmt.Printf("Successfully received key: %s\n", targetPath)

	// 5. Save to database
	fmt.Println("Adding key to gotoni database...")
	database, err := db.InitDB()
	if err != nil {
		log.Printf("Warning: Failed to init database: %v", err)
	} else {
		defer database.Close()
		keyName := strings.TrimSuffix(fileName, filepath.Ext(fileName))
		if err := database.SaveSSHKey(&db.SSHKey{Name: keyName, PrivateKey: targetPath}); err != nil {
			log.Printf("Warning: Failed to save key to database: %v", err)
		}
	}

	// 6. Update SSH config (~/.ssh/config)
	if instanceIP != "" {
		fmt.Printf("Configuring SSH access for %s (%s)...\n", instanceName, instanceIP)
		if err := client.UpdateSSHConfig(instanceName, instanceIP, targetPath); err != nil {
			log.Printf("Failed to update SSH config: %v", err)
		} else {
			fmt.Printf("\n✓ You can now connect with: ssh %s\n", instanceName)
		}
	} else {
		// Fallback for non-payload transfer
		fmt.Println("\nTo complete setup, you need to know the Instance IP.")
		fmt.Printf("Run: ssh -i %s ubuntu@<IP>\n", targetPath)

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
