package client

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLaunchInstanceWithExistingSSHKey(t *testing.T) {
	// Change to project root directory so relative paths work
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	// If we're in pkg/client, go up two levels to project root
	if strings.HasSuffix(wd, "/pkg/client") {
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

	launchedInstances, err := LaunchInstance(httpClient, apiToken, "gpu_1x_a10", "us-east-1", 1, "test-launch", sshKeyName)
	if err != nil {
		t.Fatalf("Failed to launch instance: %v", err)
	}

	if len(launchedInstances) == 0 {
		t.Error("Expected at least one launched instance, got none")
	}

	for _, inst := range launchedInstances {
		t.Logf("Launched Instance: ID=%s, SSHKeyName=%s, SSHKeyFile=%s", inst.ID, inst.SSHKeyName, inst.SSHKeyFile)
	}

	time.Sleep(10 * time.Second)

	terminatedResponse, err := TerminateInstance(httpClient, apiToken, []string{launchedInstances[0].ID})
	if err != nil {
		t.Fatalf("Failed to terminate instance: %v", err)
	}

	t.Logf("Terminated Instance: ID=%s, Name=%s, Status=%s", terminatedResponse.TerminatedInstances[0].ID, terminatedResponse.TerminatedInstances[0].Name, terminatedResponse.TerminatedInstances[0].Status)
}
