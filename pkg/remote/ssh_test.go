package remote

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLaunchInstanceAndRunSSHCommand(t *testing.T) {
	// Change to project root directory so relative paths work
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	// If we're in pkg/remote, go up two levels to project root
	if strings.HasSuffix(wd, "/pkg/remote") {
		projectRoot := filepath.Dir(filepath.Dir(wd))
		if err := os.Chdir(projectRoot); err != nil {
			t.Fatalf("Failed to change to project root: %v", err)
		}
		defer os.Chdir(wd) // Restore original directory
	}

	httpClient := &http.Client{
		Timeout:   time.Duration(30) * time.Second,
		Transport: &http.Transport{},
	}

	apiToken := os.Getenv("LAMBDA_API_KEY")
	if apiToken == "" {
		t.Fatal("LAMBDA_API_KEY environment variable is not set")
	}

	sshKeyName := "lambda-key-1762234839" // Matches ssh/lambda-key-1762234839.pem

	// Launch instance
	t.Logf("Launching instance...")
	launchedInstances, err := LaunchInstance(httpClient, apiToken, "gpu_1x_a100_sxm4", "us-east-1", 1, "test-launch", sshKeyName, "")
	if err != nil {
		t.Fatalf("Failed to launch instance: %v", err)
	}

	if len(launchedInstances) == 0 {
		t.Fatal("Expected at least one launched instance, got none")
	}

	instance := launchedInstances[0]
	t.Logf("Launched Instance: ID=%s, SSHKeyName=%s, SSHKeyFile=%s", instance.ID, instance.SSHKeyName, instance.SSHKeyFile)

	// Wait for instance to be ready
	t.Logf("Waiting for instance %s to become ready...", instance.ID)
	if err := WaitForInstanceReady(httpClient, apiToken, instance.ID, 10*time.Minute); err != nil {
		t.Fatalf("Instance failed to become ready: %v", err)
	}
	t.Logf("Instance %s is now ready!", instance.ID)

	// Get instance details to get IP address
	t.Logf("Getting instance details for %s...", instance.ID)
	instanceDetails, err := GetInstance(httpClient, apiToken, instance.ID)
	if err != nil {
		t.Fatalf("Failed to get instance details: %v", err)
	}

	if instanceDetails.IP == "" {
		t.Fatal("Instance IP address is empty")
	}

	t.Logf("Instance IP: %s", instanceDetails.IP)

	// Create SSH client manager and connect
	t.Logf("Creating SSH client manager...")
	manager := NewSSHClientManager()
	defer manager.CloseAllConnections()

	t.Logf("Connecting to instance %s via SSH...", instanceDetails.IP)
	if err := manager.ConnectToInstance(instanceDetails.IP, instance.SSHKeyFile); err != nil {
		t.Fatalf("Failed to connect via SSH: %v", err)
	}
	t.Logf("Successfully connected to instance via SSH!")

	// Execute command
	command := `echo "hello world"`
	t.Logf("Executing command: %s", command)
	output, err := manager.ExecuteCommand(instanceDetails.IP, command)
	if err != nil {
		t.Fatalf("Failed to execute command: %v", err)
	}

	t.Logf("Command output: %s", output)
	fmt.Printf("Command output: %s\n", output)
}

func TestSSHToExistingInstance(t *testing.T) {
	// Change to project root directory so relative paths work
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	// If we're in pkg/remote, go up two levels to project root
	if strings.HasSuffix(wd, "/pkg/remote") {
		projectRoot := filepath.Dir(filepath.Dir(wd))
		if err := os.Chdir(projectRoot); err != nil {
			t.Fatalf("Failed to change to project root: %v", err)
		}
		defer os.Chdir(wd) // Restore original directory
	}

	httpClient := &http.Client{
		Timeout:   time.Duration(30) * time.Second,
		Transport: &http.Transport{},
	}

	apiToken := os.Getenv("LAMBDA_API_KEY")
	if apiToken == "" {
		t.Fatal("LAMBDA_API_KEY environment variable is not set")
	}

	// Hardcoded instance ID - change this to your instance ID
	// Or set to empty string to use the first running instance
	hardcodedInstanceID := "" // Set your instance ID here, e.g., "0920582c7ff041399e34823a0be62549"

	var instanceID string
	var instanceIP string
	var sshKeyFile string

	if hardcodedInstanceID != "" {
		instanceID = hardcodedInstanceID
		t.Logf("Using hardcoded instance ID: %s", instanceID)
	} else {
		// List running instances and use the first one
		t.Logf("Listing running instances...")
		runningInstances, err := ListRunningInstances(httpClient, apiToken)
		if err != nil {
			t.Fatalf("Failed to list running instances: %v", err)
		}

		if len(runningInstances) == 0 {
			t.Fatal("No running instances found. Please launch an instance first or set hardcodedInstanceID in the test.")
		}

		instanceID = runningInstances[0].ID
		t.Logf("Found %d running instance(s), using first one: %s", len(runningInstances), instanceID)
	}

	// Get instance details to get IP address
	t.Logf("Getting instance details for %s...", instanceID)
	instanceDetails, err := GetInstance(httpClient, apiToken, instanceID)
	if err != nil {
		t.Fatalf("Failed to get instance details: %v", err)
	}

	if instanceDetails.IP == "" {
		t.Fatal("Instance IP address is empty")
	}

	instanceIP = instanceDetails.IP
	t.Logf("Instance IP: %s", instanceIP)

	// Get SSH key file for this instance
	t.Logf("Getting SSH key file for instance %s...", instanceID)
	sshKeyFile, err = GetSSHKeyForInstance(instanceID)
	if err != nil {
		t.Fatalf("Failed to get SSH key file: %v", err)
	}
	t.Logf("SSH key file: %s", sshKeyFile)

	// Create SSH client manager and connect
	t.Logf("Creating SSH client manager...")
	manager := NewSSHClientManager()
	defer manager.CloseAllConnections()

	t.Logf("Connecting to instance %s via SSH...", instanceIP)
	if err := manager.ConnectToInstance(instanceIP, sshKeyFile); err != nil {
		t.Fatalf("Failed to connect via SSH: %v", err)
	}
	t.Logf("Successfully connected to instance via SSH!")

	// Execute command - using echo to get guaranteed output
	command := `echo "Hello from SSH! Current user: $(whoami), Working directory: $(pwd)"`
	t.Logf("Executing command: %s", command)

	output, err := manager.ExecuteCommand(instanceIP, command)
	if err != nil {
		t.Fatalf("Failed to execute command: %v", err)
	}

	// Print the output
	t.Logf("Command output: %s", output)
	fmt.Printf("Command output: %s\n", output)
}
