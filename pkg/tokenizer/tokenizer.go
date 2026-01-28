// Package tokenizer provides functions for managing the Rust tokenizer sidecar
// on cluster instances.
package tokenizer

import (
	_ "embed"
	"fmt"
	"strings"
	"sync"
	"time"

	cluster "github.com/atoniolo76/gotoni/pkg/cluster"
	"github.com/atoniolo76/gotoni/pkg/remote"
)

//go:embed main.rs
var MainRS string

//go:embed Cargo.toml
var CargoToml string

const (
	RemoteProjectDir = "/home/ubuntu/tokenizer-sidecar"
	RemoteBinaryPath = "/home/ubuntu/tokenizer-sidecar/target/release/tokenizer-sidecar"
	SocketPath       = "/tmp/tokenizer.sock"
)

// SetupTokenizerSidecar uploads the Rust source and builds it on all cluster instances.
func SetupTokenizerSidecar(cl *cluster.Cluster) error {
	fmt.Println("=== Setting up Tokenizer Sidecar on Cluster ===")

	// Build script that:
	// 1. Creates project directory
	// 2. Installs Rust if needed
	// 3. Builds the sidecar
	buildScript := fmt.Sprintf(`#!/bin/bash
set -e

echo "=== Tokenizer Sidecar Setup ==="

# Create project directory
mkdir -p %s/src

# Write Cargo.toml
cat > %s/Cargo.toml << 'CARGOEOF'
%s
CARGOEOF

# Write main.rs
cat > %s/src/main.rs << 'RUSTEOF'
%s
RUSTEOF

# Install Rust if not present
if ! command -v cargo &> /dev/null; then
    echo "Installing Rust..."
    curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
fi
source $HOME/.cargo/env 2>/dev/null || true

# Install build dependencies
echo "Installing build dependencies..."
sudo apt-get update -qq
sudo apt-get install -y -qq build-essential pkg-config libssl-dev

# Build the sidecar
echo "Building tokenizer sidecar (this may take a few minutes)..."
cd %s
cargo build --release 2>&1

# Check if binary was created
if [ -f target/release/tokenizer-sidecar ]; then
    echo "OK"
    ls -la target/release/tokenizer-sidecar
else
    echo "FAILED"
    exit 1
fi
`, RemoteProjectDir, RemoteProjectDir, CargoToml, RemoteProjectDir, MainRS, RemoteProjectDir)

	fmt.Printf("Building tokenizer sidecar on %d instances...\n", len(cl.Instances))

	var wg sync.WaitGroup
	results := make(map[string]error)
	var resultsMu sync.Mutex

	for _, inst := range cl.Instances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()

			fmt.Printf("  %s: building...\n", instance.Name)

			output, err := cl.ExecuteCommandWithTimeout(
				instance.IP,
				buildScript,
				15*time.Minute, // Rust compilation takes time
			)

			resultsMu.Lock()
			if err != nil {
				results[instance.ID] = fmt.Errorf("build failed: %w", err)
				fmt.Printf("  %s: ❌ build failed\n", instance.Name)
				// Print last few lines of output for debugging
				lines := strings.Split(output, "\n")
				if len(lines) > 10 {
					lines = lines[len(lines)-10:]
				}
				fmt.Printf("    Last output: %s\n", strings.Join(lines, "\n    "))
			} else if strings.Contains(output, "OK") {
				results[instance.ID] = nil
				fmt.Printf("  %s: ✅ built\n", instance.Name)
			} else {
				results[instance.ID] = fmt.Errorf("build output didn't contain OK")
				fmt.Printf("  %s: ❌ unknown status\n", instance.Name)
			}
			resultsMu.Unlock()
		}(inst)
	}

	wg.Wait()

	// Count successes
	successCount := 0
	for _, err := range results {
		if err == nil {
			successCount++
		}
	}

	fmt.Printf("\nTokenizer build complete: %d/%d instances\n", successCount, len(cl.Instances))

	if successCount == 0 {
		return fmt.Errorf("tokenizer build failed on all instances")
	}

	return nil
}

// StartTokenizerSidecar starts the tokenizer sidecar on all cluster instances.
func StartTokenizerSidecar(cl *cluster.Cluster) error {
	fmt.Println("Starting tokenizer sidecar on all instances...")

	startScript := fmt.Sprintf(`#!/bin/bash
set +e

# Kill any existing tokenizer process (ignore errors)
pkill -f tokenizer-sidecar 2>/dev/null
rm -f %s 2>/dev/null
sleep 1

# Check if binary exists
if [ ! -f %s ]; then
    echo "BINARY_NOT_FOUND"
    exit 0
fi

# Start tokenizer sidecar in background
nohup %s > /home/ubuntu/tokenizer.log 2>&1 &
sleep 5

# Verify it's running
if [ -S %s ]; then
    echo "OK"
else
    echo "FAILED"
    cat /home/ubuntu/tokenizer.log 2>/dev/null | tail -10
fi
exit 0
`, SocketPath, RemoteBinaryPath, RemoteBinaryPath, SocketPath)

	var wg sync.WaitGroup
	results := make(map[string]string)
	var resultsMu sync.Mutex

	for _, inst := range cl.Instances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()

			output, err := cl.ExecuteCommandWithTimeout(instance.IP, startScript, 30*time.Second)

			resultsMu.Lock()
			if err != nil {
				results[instance.ID] = "error: " + err.Error()
				fmt.Printf("  %s: ❌ %v\n", instance.Name, err)
			} else if strings.Contains(output, "OK") {
				results[instance.ID] = "running"
				fmt.Printf("  %s: ✅ tokenizer running\n", instance.Name)
			} else if strings.Contains(output, "BINARY_NOT_FOUND") {
				results[instance.ID] = "not_built"
				fmt.Printf("  %s: ❌ binary not found (run 'gotoni tokenizer setup' first)\n", instance.Name)
			} else {
				results[instance.ID] = "failed: " + output
				fmt.Printf("  %s: ❌ failed to start\n", instance.Name)
			}
			resultsMu.Unlock()
		}(inst)
	}

	wg.Wait()

	// Count successes
	runningCount := 0
	for _, status := range results {
		if status == "running" {
			runningCount++
		}
	}

	fmt.Printf("\nTokenizer started on %d/%d instances\n", runningCount, len(cl.Instances))

	if runningCount == 0 {
		return fmt.Errorf("tokenizer failed to start on any instance")
	}

	return nil
}

// StopTokenizerSidecar stops the tokenizer sidecar on all cluster instances.
func StopTokenizerSidecar(cl *cluster.Cluster) error {
	fmt.Println("Stopping tokenizer sidecar on all instances...")

	stopScript := `pkill -f tokenizer-sidecar 2>/dev/null && echo "OK" || echo "NOT_RUNNING"`

	var wg sync.WaitGroup

	for _, inst := range cl.Instances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()

			output, _ := cl.ExecuteCommandWithTimeout(instance.IP, stopScript, 10*time.Second)

			if strings.Contains(output, "OK") {
				fmt.Printf("  %s: ✅ stopped\n", instance.Name)
			} else {
				fmt.Printf("  %s: ⚠️  was not running\n", instance.Name)
			}
		}(inst)
	}

	wg.Wait()
	return nil
}

// TokenizerStatus returns the status of the tokenizer sidecar on all cluster instances.
func TokenizerStatus(cl *cluster.Cluster) (map[string]string, error) {
	fmt.Println("Checking tokenizer sidecar status...")

	checkScript := fmt.Sprintf(`#!/bin/bash
if [ -S %s ]; then
    # Try to get health
    curl -s --unix-socket %s http://localhost/health 2>/dev/null || echo '{"status":"socket_exists"}'
else
    if pgrep -f tokenizer-sidecar > /dev/null; then
        echo '{"status":"starting"}'
    else
        echo '{"status":"not_running"}'
    fi
fi
`, SocketPath, SocketPath)

	results := make(map[string]string)
	var resultsMu sync.Mutex
	var wg sync.WaitGroup

	fmt.Printf("\n%-20s %-12s %-40s\n", "NAME", "STATUS", "INFO")
	fmt.Println(strings.Repeat("-", 75))

	for _, inst := range cl.Instances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()

			output, err := cl.ExecuteCommandWithTimeout(instance.IP, checkScript, 10*time.Second)

			resultsMu.Lock()
			defer resultsMu.Unlock()

			if err != nil {
				results[instance.ID] = "error"
				fmt.Printf("%-20s %-12s %-40s\n", instance.Name, "❌", err.Error())
				return
			}

			output = strings.TrimSpace(output)

			if strings.Contains(output, `"status":"ok"`) {
				results[instance.ID] = "running"
				// Extract model info if present
				if strings.Contains(output, "model") {
					fmt.Printf("%-20s %-12s %-40s\n", instance.Name, "✅", "running (Mistral tokenizer)")
				} else {
					fmt.Printf("%-20s %-12s %-40s\n", instance.Name, "✅", "running")
				}
			} else if strings.Contains(output, "socket_exists") {
				results[instance.ID] = "socket_only"
				fmt.Printf("%-20s %-12s %-40s\n", instance.Name, "⚠️", "socket exists but no response")
			} else if strings.Contains(output, "starting") {
				results[instance.ID] = "starting"
				fmt.Printf("%-20s %-12s %-40s\n", instance.Name, "⏳", "starting...")
			} else {
				results[instance.ID] = "not_running"
				fmt.Printf("%-20s %-12s %-40s\n", instance.Name, "❌", "not running")
			}
		}(inst)
	}

	wg.Wait()

	return results, nil
}
