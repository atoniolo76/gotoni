package client

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateSSHConfig(t *testing.T) {
	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "ssh-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Mock getSSHDir function using a wrapper or interface in real code, 
	// but for this test we'll manually create the structure and point to it
	// Since we can't easily mock the internal getSSHDir without dependency injection,
	// we will test the logic by creating a fake config file and validating the format string logic
	// or we can temporarily set HOME env var which getSSHDir uses.

	originalHome := os.Getenv("HOME")
	defer os.Setenv("HOME", originalHome)
	
	os.Setenv("HOME", tempDir)
	
	// Create .ssh directory
	sshDir := filepath.Join(tempDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatalf("Failed to create .ssh dir: %v", err)
	}

	// Define test inputs
	hostName := "test-instance"
	hostIP := "192.168.1.100"
	identityFile := "/path/to/key.pem"

	// Execute the function
	if err := UpdateSSHConfig(hostName, hostIP, identityFile); err != nil {
		t.Fatalf("UpdateSSHConfig failed: %v", err)
	}

	// Verify the file content
	configPath := filepath.Join(sshDir, "config")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read config file: %v", err)
	}

	contentStr := string(content)
	
	expectedLines := []string{
		fmt.Sprintf("Host %s", hostName),
		fmt.Sprintf("HostName %s", hostIP),
		"User ubuntu",
		fmt.Sprintf("IdentityFile %s", identityFile),
		"StrictHostKeyChecking no",
		"UserKnownHostsFile /dev/null",
	}

	for _, line := range expectedLines {
		if !strings.Contains(contentStr, line) {
			t.Errorf("Config file missing expected line: '%s'.\nGot:\n%s", line, contentStr)
		}
	}

	// Test idempotency (running it again shouldn't duplicate)
	// Note: The current implementation prints a warning and returns nil if it exists.
	// This basic check relies on string containment which is simple.
	if err := UpdateSSHConfig(hostName, hostIP, identityFile); err != nil {
		t.Fatalf("UpdateSSHConfig (2nd run) failed: %v", err)
	}

	content2, _ := os.ReadFile(configPath)
	if len(content2) != len(content) {
		t.Errorf("Config file changed on second run, expected no change (idempotency check).")
	}
}

