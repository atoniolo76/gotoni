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

	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/psanford/wormhole-william/wormhole"
	"github.com/spf13/cobra"
)

type SharePayload struct {
	KeyName      string `json:"key_name"`
	KeyData      string `json:"key_data"`
	InstanceIP   string `json:"instance_ip,omitempty"`
	InstanceName string `json:"instance_name"`
	TunnelHost   string `json:"tunnel_host,omitempty"`
	TunnelPort   string `json:"tunnel_port,omitempty"`
	Provider     string `json:"provider,omitempty"`
	SSHUser      string `json:"ssh_user,omitempty"`
}

// sshPrivateKeyCandidates mirrors the key types that the Python gotoni.add_ssh()
// helper auto-detects, but uses the *private* key paths.
var sshPrivateKeyCandidates = []string{
	"~/.ssh/id_ed25519",
	"~/.ssh/id_rsa",
	"~/.ssh/id_ecdsa",
	"~/.ssh/id_dsa",
}

func resolveLocalSSHKey() (string, error) {
	for _, candidate := range sshPrivateKeyCandidates {
		resolved := os.ExpandEnv(candidate)
		if strings.HasPrefix(resolved, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				continue
			}
			resolved = filepath.Join(home, resolved[2:])
		}
		if _, err := os.Stat(resolved); err == nil {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("no SSH private key found in ~/.ssh/ (tried ed25519, rsa, ecdsa, dsa)")
}

// shareCmd represents the share command
var shareCmd = &cobra.Command{
	Use:   "share [instance-id-or-name]",
	Short: "Securely share instance access with another user",
	Long: `Securely share instance access using the Magic Wormhole protocol.
Generates a code that the receiver can use with 'gotoni receive' to automatically
configure SSH access.

Supports both Lambda (SSH key + IP) and Modal (SSH key + tunnel) instances.`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		_, providerType := remote.GetCloudProvider()

		switch providerType {
		case remote.CloudProviderModal:
			runShareModal(args)
		default:
			runShareLambda(args)
		}
	},
}

func runShareLambda(args []string) {
	var instanceID string
	if len(args) > 0 {
		instanceID = args[0]
	}

	apiToken := remote.GetAPIToken()
	if apiToken == "" {
		log.Fatal("API token not provided via LAMBDA_API_KEY environment variable")
	}

	httpClient := remote.NewHTTPClient()

	var instance *remote.RunningInstance
	var err error

	if instanceID != "" {
		instance, err = remote.ResolveInstance(httpClient, apiToken, instanceID)
		if err != nil {
			log.Fatalf("Error resolving instance '%s': %v", instanceID, err)
		}
	} else {
		runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Error listing running instances: %v", err)
		}
		if len(runningInstances) == 0 {
			log.Fatal("No running instances found. Please provide an instance ID or name.")
		}
		instance = &runningInstances[0]
		fmt.Printf("Using instance: %s (%s)\n", instance.Name, instance.ID)
	}

	if instance.IP == "" {
		log.Fatalf("Instance %s has no IP address yet. Is it running?", instance.ID)
	}

	sshKeyPath, err := remote.GetSSHKeyForInstance(instance.ID)
	if err != nil {
		log.Fatalf("Error finding SSH key for instance %s: %v", instance.ID, err)
	}

	if _, err := os.Stat(sshKeyPath); os.IsNotExist(err) {
		log.Fatalf("SSH key file does not exist at path: %s", sshKeyPath)
	}

	keyData, err := os.ReadFile(sshKeyPath)
	if err != nil {
		log.Fatalf("Failed to read SSH key file: %v", err)
	}

	payload := SharePayload{
		KeyName:      filepath.Base(sshKeyPath),
		KeyData:      string(keyData),
		InstanceIP:   instance.IP,
		InstanceName: instance.Name,
		Provider:     string(remote.CloudProviderLambda),
		SSHUser:      "ubuntu",
	}

	if payload.InstanceName == "" {
		payload.InstanceName = instance.ID
	}

	fmt.Printf("Preparing to share access to instance '%s' (%s)\n", payload.InstanceName, payload.InstanceIP)
	fmt.Printf("Key file: %s\n\n", sshKeyPath)

	sendPayload(payload)
}

func runShareModal(args []string) {
	var sandboxID string
	if len(args) > 0 {
		sandboxID = args[0]
	}

	apiToken := remote.GetAPIToken()
	httpClient := remote.NewHTTPClient()

	var instance *remote.RunningInstance
	var err error

	if sandboxID != "" {
		instance, err = remote.ResolveInstance(httpClient, apiToken, sandboxID)
		if err != nil {
			log.Fatalf("Error resolving Modal sandbox '%s': %v", sandboxID, err)
		}
	} else {
		runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Error listing Modal sandboxes: %v", err)
		}
		if len(runningInstances) == 0 {
			log.Fatal("No running Modal sandboxes found. Please provide a sandbox ID.")
		}
		instance = &runningInstances[0]
		fmt.Printf("Using sandbox: %s\n", instance.ID)
	}

	if len(instance.TunnelURLs) == 0 {
		log.Fatal("No tunnels found for this sandbox. Make sure the sandbox is running with gotoni.start_ssh().")
	}

	// Find the SSH tunnel – look for a tunnel whose port looks like the sshd port.
	// The Python start_ssh() defaults to port 2222, but we accept any single tunnel.
	var tunnelHost, tunnelPort string
	for _, url := range instance.TunnelURLs {
		parts := strings.SplitN(url, ":", 2)
		if len(parts) == 2 {
			tunnelHost = parts[0]
			tunnelPort = parts[1]
			break
		}
	}
	if tunnelHost == "" {
		log.Fatal("Could not parse tunnel URL from sandbox. Ensure start_ssh() is running inside the sandbox.")
	}

	sshKeyPath, err := resolveLocalSSHKey()
	if err != nil {
		log.Fatalf("Error finding local SSH private key: %v\nThe receiver needs the private key matching the public key baked into the Modal image by gotoni.add_ssh().", err)
	}

	keyData, err := os.ReadFile(sshKeyPath)
	if err != nil {
		log.Fatalf("Failed to read SSH key file: %v", err)
	}

	instanceName := instance.Name
	if instanceName == "" || instanceName == instance.ID {
		instanceName = "modal-" + instance.ID[:8]
	}

	payload := SharePayload{
		KeyName:      filepath.Base(sshKeyPath),
		KeyData:      string(keyData),
		InstanceName: instanceName,
		TunnelHost:   tunnelHost,
		TunnelPort:   tunnelPort,
		Provider:     string(remote.CloudProviderModal),
		SSHUser:      "root",
	}

	fmt.Printf("Preparing to share access to Modal sandbox '%s'\n", instanceName)
	fmt.Printf("Tunnel: %s:%s\n", tunnelHost, tunnelPort)
	fmt.Printf("Key file: %s\n\n", sshKeyPath)

	sendPayload(payload)
}

func sendPayload(payload SharePayload) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Fatalf("Failed to marshal payload: %v", err)
	}

	var c wormhole.Client
	ctx := context.Background()

	fmt.Println("Generating secure code...")

	reader := strings.NewReader(string(jsonData))
	code, status, err := c.SendFile(ctx, "gotoni-share.json", reader)
	if err != nil {
		log.Fatalf("Failed to start send: %v", err)
	}

	fmt.Printf("\nShare this code with the receiver:\n\n")
	fmt.Printf("\t%s\n\n", code)
	fmt.Println("Waiting for receiver to connect...")

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
}

func init() {
	rootCmd.AddCommand(shareCmd)
}
