package sglang

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	serve "github.com/atoniolo76/gotoni/pkg/cluster"
	"github.com/atoniolo76/gotoni/pkg/remote"
)

// TestSGLangAPIEndpoints tests the SGLang server endpoints using /get_server_info
func TestSGLangAPIEndpoints(cl *serve.Cluster, instances []remote.RunningInstance) {
	testClient := &http.Client{Timeout: 10 * time.Second}

	// All SGLang servers run on port 8080
	port := 8080

	for _, instance := range instances {
		// Test health endpoint
		healthURL := fmt.Sprintf("http://%s:%d/health", instance.IP, port)
		resp, err := testClient.Get(healthURL)
		if err != nil {
			fmt.Printf("❌ Health check failed for %s:%d - %v\n", instance.IP, port, err)
		} else {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				fmt.Printf("✅ Health endpoint %s:%d is responding\n", instance.IP, port)
			} else {
				fmt.Printf("⚠️  Health endpoint %s:%d returned status %d\n", instance.IP, port, resp.StatusCode)
			}
		}

		// Test SGLang metrics endpoint (/get_server_info) as used in sglang_metrics.go
		metricsURL := fmt.Sprintf("http://%s:%d/get_server_info", instance.IP, port)
		resp, err = testClient.Get(metricsURL)
		if err != nil {
			fmt.Printf("❌ Metrics check failed for %s:%d - %v\n", instance.IP, port, err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == 200 {
			// Parse the SGLang server info
			var serverInfo struct {
				NumRunningReqs int     `json:"num_running_reqs"`
				NumWaitingReqs int     `json:"num_waiting_reqs"`
				MaxRunningReqs int     `json:"max_running_reqs"`
				GPUCacheUsage  float64 `json:"token_usage"`
			}
			if decodeErr := json.NewDecoder(resp.Body).Decode(&serverInfo); decodeErr != nil {
				fmt.Printf("⚠️  Failed to decode server info from %s:%d: %v\n", instance.IP, port, decodeErr)
			} else {
				fmt.Printf("✅ SGLang metrics from %s:%d:\n", instance.IP, port)
				fmt.Printf("   Running requests: %d\n", serverInfo.NumRunningReqs)
				fmt.Printf("   Waiting requests: %d\n", serverInfo.NumWaitingReqs)
				fmt.Printf("   Max running requests: %d\n", serverInfo.MaxRunningReqs)
				fmt.Printf("   GPU cache usage: %.2f%%\n", serverInfo.GPUCacheUsage*100)
			}
		} else {
			fmt.Printf("⚠️  Metrics endpoint %s:%d returned status %d\n", instance.IP, port, resp.StatusCode)
		}
	}
}

// SetupSGLangDocker sets up Docker-based SGLang deployment (parallel)
func SetupSGLangDocker(cl *serve.Cluster, hfToken string) {
	fmt.Println("Checking SGLang Docker setup on all instances...")

	// Check if SGLang container is already running
	checkCmd := "sudo docker ps --filter name=sglang-server --filter status=running --format '{{.Names}}' 2>/dev/null | grep -q sglang-server && echo 'running' || echo 'not_running'"
	results := cl.ExecuteOnCluster(checkCmd)

	// Collect instances that need setup
	var needsSetup []string
	for instanceID, result := range results {
		if result.Error != nil {
			fmt.Printf("❌ Failed to check SGLang on %s: %v\n", instanceID[:16], result.Error)
			needsSetup = append(needsSetup, instanceID)
			continue
		}

		output := strings.TrimSpace(result.Output)
		if output == "running" {
			fmt.Printf("✅ SGLang already running on %s\n", instanceID[:16])
		} else {
			needsSetup = append(needsSetup, instanceID)
		}
	}

	// Setup HuggingFace credentials and pull Docker image in parallel
	if len(needsSetup) > 0 {
		fmt.Printf("\n📦 Setting up SGLang on %d instances in parallel...\n", len(needsSetup))
		var wg sync.WaitGroup
		for _, instanceID := range needsSetup {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				fmt.Printf("  → Starting SGLang setup on %s...\n", id[:16])
				SetupSGLangOnInstance(cl, id, hfToken)
			}(instanceID)
		}
		wg.Wait()
		fmt.Println("✅ All SGLang setups complete!")
	}
}

// SetupSGLangOnInstance sets up SGLang on a specific instance
func SetupSGLangOnInstance(cl *serve.Cluster, instanceID string, hfToken string) {
	// Find the instance IP
	var instanceIP string
	for _, inst := range cl.Instances {
		if inst.ID == instanceID {
			instanceIP = inst.IP
			break
		}
	}
	if instanceIP == "" {
		fmt.Printf("❌ Instance %s not found\n", instanceID[:16])
		return
	}

	// Setup HuggingFace credentials and pull SGLang Docker image
	setupScript := fmt.Sprintf(`#!/bin/bash
set -e

echo "Setting up HuggingFace credentials..."
mkdir -p /home/ubuntu/.cache/huggingface
if [ -n "%s" ]; then
    echo "%s" > /home/ubuntu/.cache/huggingface/token
    echo "HF_TOKEN saved to cache"
fi

echo "Checking Docker installation..."
if ! command -v docker &> /dev/null; then
    echo "Docker not found, installing..."
    curl -fsSL https://get.docker.com -o get-docker.sh
    sudo sh get-docker.sh
    rm get-docker.sh
    sudo usermod -aG docker ubuntu
fi

echo "Pulling SGLang Docker image (this may take a while)..."
sudo docker pull lmsysorg/sglang:latest

echo "SGLang Docker setup complete!"
`, hfToken, hfToken)

	output, err := cl.ExecuteCommandWithTimeout(instanceIP, setupScript, 15*time.Minute)
	if err != nil {
		fmt.Printf("❌ Failed to setup SGLang on %s: %v\n", instanceID[:16], err)
		fmt.Printf("Output: %s\n", output)
		return
	}
	fmt.Printf("✅ SGLang setup complete on %s\n", instanceID[:16])
}

// DiagnoseSGLangOnCluster runs diagnostic commands on all instances to identify connectivity issues
func DiagnoseSGLangOnCluster(cl *serve.Cluster) {
	fmt.Println("Running diagnostics on all cluster instances...")

	diagScript := `#!/bin/bash
echo "=== DIAGNOSTICS FOR $(hostname) ==="

echo ""
echo "1. Docker container status:"
sudo docker ps -a --filter name=sglang-server --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"

echo ""
echo "2. Container running check:"
if sudo docker ps --filter name=sglang-server --filter status=running | grep -q sglang-server; then
    echo "✅ Container is RUNNING"
else
    echo "❌ Container is NOT RUNNING"
    echo "   Last 20 lines of container logs (if available):"
    sudo docker logs sglang-server --tail 20 2>&1 || echo "   No logs available"
fi

echo ""
echo "3. Port 8080 listening check (netstat/ss):"
ss -tlnp | grep :8080 || echo "   Port 8080 is NOT listening on any interface"

echo ""
echo "4. Docker port mappings:"
sudo docker port sglang-server 2>/dev/null || echo "   No port mappings (container not running?)"

echo ""
echo "5. Local connectivity test (curl localhost:8080):"
curl -s -o /dev/null -w "HTTP Status: %{http_code}\n" --connect-timeout 5 http://localhost:8080/health 2>/dev/null || echo "   Failed to connect to localhost:8080"

echo ""
echo "6. Local metrics endpoint test:"
curl -s --connect-timeout 5 http://localhost:8080/get_server_info 2>/dev/null | head -c 500 || echo "   Failed to get server info"

echo ""
echo "7. Network interfaces (for binding verification):"
ip addr show | grep "inet " | head -5

echo ""
echo "8. Docker network inspection:"
sudo docker inspect sglang-server --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null || echo "   Container not found"

echo ""
echo "9. GPU availability check:"
nvidia-smi --query-gpu=name,memory.used,memory.total --format=csv,noheader 2>/dev/null || echo "   nvidia-smi not available"

echo ""
echo "10. Container environment (HF_TOKEN check):"
sudo docker exec sglang-server env 2>/dev/null | grep -E "^(HF_|HUGGING)" || echo "   No HF environment variables set in container"

echo ""
echo "=== END DIAGNOSTICS ==="
`

	results := cl.ExecuteOnCluster(diagScript)

	for instanceID, result := range results {
		// Find instance IP for display
		var instanceIP string
		for _, inst := range cl.Instances {
			if inst.ID == instanceID {
				instanceIP = inst.IP
				break
			}
		}

		fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Printf("Instance: %s (%s)\n", instanceID[:16], instanceIP)
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

		if result.Error != nil {
			fmt.Printf("❌ Diagnostic failed: %v\n", result.Error)
		} else {
			fmt.Println(result.Output)
		}
	}

	// Summary of potential issues
	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("COMMON ISSUES TO CHECK:")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("1. Container not running → Check Docker logs for crash reason")
	fmt.Println("2. Port not listening → Server failed to start inside container")
	fmt.Println("3. No HF environment vars → Model download will fail for gated models")
	fmt.Println("4. GPU memory issues → Check nvidia-smi for OOM")
	fmt.Println("5. Model not downloaded → First startup can take 10+ minutes")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
}