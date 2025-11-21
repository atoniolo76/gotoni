#!/bin/bash
set -e

# This script builds a Docker container running SSH, generates a key,
# configures the container to accept it, and then runs a Go test
# to verify our tool can update config and connect.

# 1. Generate a test SSH key
echo "Generating test SSH key..."
KEY_DIR="./tests/docker_ssh/keys"
mkdir -p "$KEY_DIR"
# -N "" means no passphrase, -f output file
ssh-keygen -t ed25519 -N "" -f "$KEY_DIR/id_ed25519_test"

# 2. Build Docker image
echo "Building Docker image..."
docker build -t gotoni-ssh-test ./tests/docker_ssh

# 3. Run Docker container
echo "Starting Docker container..."
# Map container port 22 to host port 2222
CONTAINER_ID=$(docker run -d -p 2222:22 gotoni-ssh-test)

# Ensure container is cleaned up on exit
cleanup() {
    echo "Cleaning up..."
    docker stop "$CONTAINER_ID" >/dev/null
    docker rm "$CONTAINER_ID" >/dev/null
    rm -rf "$KEY_DIR"
}
trap cleanup EXIT

# 4. Copy public key to container's authorized_keys
echo "Configuring container with public key..."
docker exec -i "$CONTAINER_ID" sh -c 'cat >> /home/ubuntu/.ssh/authorized_keys' < "$KEY_DIR/id_ed25519_test.pub"
docker exec "$CONTAINER_ID" chown ubuntu:ubuntu /home/ubuntu/.ssh/authorized_keys
docker exec "$CONTAINER_ID" chmod 600 /home/ubuntu/.ssh/authorized_keys

# 5. Wait a moment for SSH to be ready
sleep 2

# 6. Run a special Go test that uses this local container
# We'll need to pass environment variables to tell the test where to connect
echo "Running integration test..."
export TEST_SSH_HOST="localhost"
export TEST_SSH_PORT="2222"
export TEST_SSH_USER="ubuntu"
export TEST_SSH_KEY="$(pwd)/$KEY_DIR/id_ed25519_test"

# We create a temporary test file here because we want to run it in the context of the main package
# but with specific integration logic.
cat > cmd/integration_test.go <<EOF
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
    
    "github.com/atoniolo76/gotoni/pkg/client"
)

func TestIntegrationSSH(t *testing.T) {
	host := os.Getenv("TEST_SSH_HOST")
	if host == "" {
		t.Skip("Skipping integration test: TEST_SSH_HOST not set")
	}
    
    // Inputs
    keyFile := os.Getenv("TEST_SSH_KEY")
    // port := os.Getenv("TEST_SSH_PORT") // We can't easily change port in standard ssh config without HostName syntax hacks or specific port directive support in our tool
    // Our tool assumes default port 22 usually, but standard ssh config allows Port directive.
    // For this test, we'll verify that we can update the config, but we might need to manually hack the port into the connection string 
    // or update our tool to support non-standard ports if we wanted to test full end-to-end connectivity via the tool's "connect" command.
    // However, "gotoni open" relies on the system "ssh" command reading the config.
    // So if we write "Port 2222" into the config, it should work.
    
    // Update our tool's UpdateSSHConfig to support custom port? 
    // For now, let's just test that we can write the config entry.
    
    instanceName := "docker-test-instance"
    
    // Mock HOME to avoid messing with user's real config
    tempHome, _ := os.MkdirTemp("", "gotoni-int-test")
    defer os.RemoveAll(tempHome)
    os.Setenv("HOME", tempHome)
    os.Mkdir(tempHome+"/.ssh", 0700)
    
    // 1. Update SSH Config
    // Note: Our current UpdateSSHConfig doesn't support Port.
    // We will call it, then verify the file exists, then append the Port manually for the connection test if needed.
    
    err := client.UpdateSSHConfig(instanceName, "localhost", keyFile)
    if err != nil {
        t.Fatalf("UpdateSSHConfig failed: %v", err)
    }
    
    // Manually append Port 2222 to the config for this test host
    // because our tool doesn't support setting port yet (and cloud instances are usually on 22)
    configPath := tempHome + "/.ssh/config"
    f, _ := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0600)
    f.WriteString("  Port 2222\n")
    f.Close()
    
    // 2. Verify "ssh" command can connect using this alias
    // This proves that VS Code / Cursor would be able to connect using "ssh-remote+docker-test-instance"
    cmd := exec.Command("ssh", "-F", configPath, instanceName, "echo success")
    output, err := cmd.CombinedOutput()
    
    fmt.Printf("SSH Output: %s\n", output)
    
    if err != nil {
        t.Fatalf("Failed to connect via SSH using alias: %v. Output: %s", err, output)
    }
    
    if string(output) != "success\n" {
        t.Errorf("Unexpected output: %q", string(output))
    }
}
EOF

# Run the test
go test -v cmd/integration_test.go cmd/root.go cmd/open.go

# Cleanup test file
rm cmd/integration_test.go

echo "Integration test passed!"

