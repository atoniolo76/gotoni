/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atoniolo76/gotoni/pkg/client"
	"github.com/psanford/wormhole-william/wormhole"
	"github.com/spf13/cobra"
)

type SharePayload struct {
	KeyName      string `json:"key_name"`
	KeyData      string `json:"key_data"`
	InstanceIP   string `json:"instance_ip"`
	InstanceName string `json:"instance_name"`
}

// shareCmd represents the share command
var shareCmd = &cobra.Command{
	Use:   "share <instance-id-or-name>",
	Short: "Securely share an instance's SSH key with another user",
	Long:  `Securely share an instance's SSH key using the Magic Wormhole protocol. Generates a code that the receiver can use to download the key and automatically configure access.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		instanceID := args[0]

		// 1. Get API Token
		apiToken := client.GetAPIToken()
		if apiToken == "" {
			log.Fatal("API token not provided via LAMBDA_API_KEY environment variable")
		}

		// 2. Create HTTP client
		httpClient := client.NewHTTPClient()

		// 3. Resolve instance to get IP and real ID
		instance, err := client.ResolveInstance(httpClient, apiToken, instanceID)
		if err != nil {
			log.Fatalf("Error resolving instance '%s': %v", instanceID, err)
		}

		if instance.IP == "" {
			log.Fatalf("Instance %s has no IP address yet. Is it running?", instance.ID)
		}

		// 4. Lookup SSH key for the instance
		sshKeyPath, err := client.GetSSHKeyForInstance(instance.ID)
		if err != nil {
			// If we can't find it by ID, try to find it by name mapping or ask user?
			// For now, fail if not found in local config
			log.Fatalf("Error finding SSH key for instance %s: %v", instance.ID, err)
		}

		// Verify file exists
		if _, err := os.Stat(sshKeyPath); os.IsNotExist(err) {
			log.Fatalf("SSH key file does not exist at path: %s", sshKeyPath)
		}

		// Read SSH key content
		keyData, err := os.ReadFile(sshKeyPath)
		if err != nil {
			log.Fatalf("Failed to read SSH key file: %v", err)
		}

		// 5. Prepare payload
		payload := SharePayload{
			KeyName:      filepath.Base(sshKeyPath),
			KeyData:      string(keyData),
			InstanceIP:   instance.IP,
			InstanceName: instance.Name, // Or instanceID if name is empty?
		}

		if payload.InstanceName == "" {
			payload.InstanceName = instance.ID
		}

		// Marshal to JSON
		jsonData, err := json.Marshal(payload)
		if err != nil {
			log.Fatalf("Failed to marshal payload: %v", err)
		}

		fmt.Printf("Preparing to share access to instance '%s' (%s)\n", payload.InstanceName, payload.InstanceIP)
		fmt.Printf("Key file: %s\n\n", sshKeyPath)

		// 6. Initialize Wormhole client
		var c wormhole.Client
		ctx := context.Background()

		fmt.Println("Generating secure code...")

		// 7. Send payload
		// We send it as a JSON file so the receiver can detect it's a payload
		// Use a stream for the JSON data
		reader := strings.NewReader(string(jsonData))

		code, status, err := c.SendFile(ctx, "gotoni-share.json", reader)
		if err != nil {
			log.Fatalf("Failed to start send: %v", err)
		}

		fmt.Printf("\nShare this code with the receiver:\n\n")
		fmt.Printf("\t%s\n\n", code)
		fmt.Println("Waiting for receiver to connect...")

		// Wait for transfer to complete
		select {
		case s := <-status:
			if s.Error != nil {
				log.Fatalf("Transfer failed: %v", s.Error)
			} else if s.OK {
				fmt.Println("\nTransfer completed successfully!")
			}
		case <-time.After(10 * time.Minute):
			log.Fatal("Transfer timed out after 10 minutes")
		}
	},
}

func init() {
	rootCmd.AddCommand(shareCmd)
}
