package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"toni/gpusnapshot/internal/providers"
	"toni/gpusnapshot/internal/providers/lambdalabs"
)

// getSSHKeyPath returns the path where SSH keys should be stored
func getSSHKeyPath(keyName string) (string, error) {
	// Check for custom SSH directory environment variable
	sshDir := os.Getenv("GPU_SNAPSHOT_SSH_DIR")
	if sshDir == "" {
		// Get user home directory
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get user home directory: %w", err)
		}
		// Default to ~/.gpusnapshot/ssh-keys/
		sshDir = filepath.Join(homeDir, ".gpusnapshot", "ssh-keys")
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create SSH keys directory: %w", err)
	}

	// Generate unique filename based on timestamp to avoid conflicts
	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("gpu-instance-%s-%s.pem", keyName, timestamp)

	return filepath.Join(sshDir, filename), nil
}

func main() {
	// Get API token from environment
	apiToken := os.Getenv("LAMBDA_API_TOKEN")
	if apiToken == "" {
		log.Fatal("Please set LAMBDA_API_TOKEN environment variable")
	}

	// Create Lambda Labs provider directly (avoiding factory import cycle)
	provider := lambdalabs.NewClient(apiToken)

	fmt.Printf("🚀 Launching lowest-tier GPU instance on %s...\n", provider.GetProviderName())

	// Ensure we have an SSH key for instance access
	fmt.Printf("🔐 Checking SSH keys...\n")
	sshKeys, err := provider.ListSSHKeys()
	if err != nil {
		log.Fatalf("Failed to list SSH keys: %v", err)
	}

	var sshKeyName string
	var createdNewKey bool
	var privateKeyFile string
	if len(sshKeys) == 0 {
		fmt.Printf("   No SSH keys found. Creating a new one...\n")
		sshKey, err := provider.CreateSSHKey("sample-instance-key")
		if err != nil {
			log.Fatalf("Failed to create SSH key: %v", err)
		}

		sshKeyName = sshKey.Name
		createdNewKey = true

		// Get proper path for saving the private key
		privateKeyFile, err := getSSHKeyPath(sshKey.Name)
		if err != nil {
			log.Fatalf("Failed to get SSH key path: %v", err)
		}

		// Save the private key to a secure location
		if err := os.WriteFile(privateKeyFile, []byte(sshKey.PrivateKey), 0600); err != nil {
			log.Fatalf("Failed to save private key: %v", err)
		}

		// Show which directory the key was saved to
		keyDir := os.Getenv("GPU_SNAPSHOT_SSH_DIR")
		if keyDir == "" {
			homeDir, _ := os.UserHomeDir()
			keyDir = filepath.Join(homeDir, ".gpusnapshot", "ssh-keys")
		}

		fmt.Printf("   ✅ Created SSH key: %s\n", sshKey.Name)
		fmt.Printf("   🔑 Private key saved to: %s\n", privateKeyFile)
		fmt.Printf("   📁 SSH keys directory: %s\n", keyDir)
		fmt.Printf("   🛡️  Keep this file secure - it's your only way to access the instance!\n")
	} else {
		sshKeyName = sshKeys[0].Name
		fmt.Printf("   ✅ Using existing SSH key: %s\n", sshKeyName)
	}

	// Get available instance types
	instanceTypes, err := provider.ListInstanceTypes()
	if err != nil {
		log.Fatalf("Failed to list instance types: %v", err)
	}

	if len(instanceTypes) == 0 {
		log.Fatal("No instance types available")
	}

	// Find the lowest cost instance type with available capacity
	var lowestTier *providers.InstanceType
	minPrice := 999999 // High starting value in cents

	for _, instanceType := range instanceTypes {
		// Only consider instances with available regions
		if len(instanceType.Regions) > 0 && instanceType.PriceCentsPerHour < minPrice {
			lowestTier = instanceType
			minPrice = instanceType.PriceCentsPerHour
		}
	}

	if lowestTier == nil {
		log.Fatal("No instances with available capacity found")
	}

	fmt.Printf("📊 Selected: %s ($%d/hour)\n", lowestTier.Name, minPrice/100)
	fmt.Printf("   - Description: %s\n", lowestTier.Description)
	fmt.Printf("   - GPUs: %d x %s\n", len(lowestTier.GPUs), lowestTier.GPUs[0].Description)
	fmt.Printf("   - CPU: %d vCPUs\n", lowestTier.VCPUs)
	fmt.Printf("   - RAM: %d GB\n", lowestTier.RAM_GB)
	fmt.Printf("   - Storage: %d GB\n", lowestTier.Storage_GB)
	fmt.Printf("   - Available regions: %v\n", lowestTier.Regions)

	// For demo purposes, we'll use the first available region
	// In production, you might want to let the user choose or select the cheapest region
	selectedRegion := lowestTier.Regions[0]

	fmt.Printf("🌍 Using region: %s\n", selectedRegion)

	// Launch the instance
	launchReq := &providers.LaunchRequest{
		InstanceTypeName: lowestTier.Name,
		Region:           selectedRegion,
		Name:             "sample-gpu-instance",
		SSHKeyNames:      []string{sshKeyName},
	}

	fmt.Printf("⚡ Launching instance...\n")

	instanceID, err := provider.LaunchInstance(launchReq)
	if err != nil {
		log.Fatalf("Failed to launch instance: %v", err)
	}

	fmt.Printf("✅ Instance launched! ID: %s\n", instanceID)
	fmt.Printf("⏳ Waiting for instance to be ready...\n")

	// Poll for instance status until it's running
	var instance *providers.Instance
	for {
		instance, err = provider.GetInstance(instanceID)
		if err != nil {
			log.Fatalf("Failed to get instance status: %v", err)
		}

		fmt.Printf("   Status: %s\n", instance.Status)

		if instance.Status == "active" {
			break
		}

		// Wait 10 seconds before checking again
		time.Sleep(10 * time.Second)
	}

	fmt.Printf("🎉 Instance is ready!\n")
	fmt.Printf("📍 Public IP: %s\n", instance.PublicIP)

	if instance.JupyterURL != "" && instance.JupyterToken != "" {
		fmt.Printf("🧠 Jupyter Lab: %s\n", instance.JupyterURL)
		fmt.Printf("🔑 Jupyter Token: %s\n", instance.JupyterToken)
	}

	if len(instance.SSHKeys) > 0 {
		fmt.Printf("🔐 SSH Keys: %v\n", instance.SSHKeys)
		if createdNewKey {
			// We created a new key, show the command with the saved file
			fmt.Printf("💡 SSH Command: ssh -i %s ubuntu@%s\n", privateKeyFile, instance.PublicIP)
		} else {
			// Using existing key - user should know their own key location
			fmt.Printf("💡 SSH Command: ssh ubuntu@%s\n", instance.PublicIP)
			fmt.Printf("   ℹ️  Using your existing SSH key (not managed by this script)\n")
		}
	}

	fmt.Printf("\n⚠️  REMEMBER TO TERMINATE THE INSTANCE WHEN DONE ⚠️\n")
	fmt.Printf("💰 Cost: $%d/hour\n", minPrice/100)
	fmt.Printf("🛑 To terminate: implement termination logic or use Lambda dashboard\n")

	// Note: In a real application, you'd want to:
	// 1. Handle SSH key setup
	// 2. Implement proper error handling
	// 3. Add instance termination
	// 4. Add timeout handling
	// 5. Add signal handling for graceful shutdown
}
