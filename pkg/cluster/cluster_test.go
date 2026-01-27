package serve

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atoniolo76/gotoni/pkg/db"
	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/joho/godotenv"
)

func TestClusterLlamaCpp(t *testing.T) {
	fmt.Println("=== Testing Cluster Llama.cpp Server Deployment ===")

	// Load environment variables from .env file
	if err := godotenv.Load("../../.env"); err != nil {
		t.Logf("Warning: Could not load .env file: %v", err)
	}

	httpClient := remote.NewHTTPClient()
	apiToken := os.Getenv("LAMBDA_API_KEY")

	if apiToken == "" {
		t.Skip("LAMBDA_API_KEY not set, skipping integration test")
	}

	// Get ALL running instances from the provider
	fmt.Println("1. Discovering all running instances...")
	provider, _ := remote.GetCloudProvider()
	allInstances, err := provider.ListRunningInstances(httpClient, apiToken)
	if err != nil {
		t.Fatalf("Failed to list running instances: %v", err)
	}

	if len(allInstances) == 0 {
		t.Fatal("No running instances found. Please launch some instances first.")
	}

	fmt.Printf("Found %d running instance(s):\n", len(allInstances))
	for _, inst := range allInstances {
		fmt.Printf("  - %s (%s) @ %s [%s]\n", inst.Name, inst.ID[:16], inst.IP, inst.Region.Name)
	}

	// Create or load cluster with all running instances
	clusterName := "llama-auto-cluster"
	cluster, err := GetCluster(httpClient, apiToken, clusterName)
	if err != nil {
		// Cluster doesn't exist, create a new one
		fmt.Printf("\nCreating new cluster '%s' with all %d instances...\n", clusterName, len(allInstances))

		// Initialize database for SSH key lookups
		database, dbErr := db.InitDB()
		if dbErr != nil {
			t.Fatalf("Failed to initialize database: %v", dbErr)
		}

		cluster = &Cluster{
			Name:      clusterName,
			Instances: allInstances,
			sshMgr:    remote.NewSSHClientManager(),
			database:  database,
		}
	} else {
		// Cluster exists, sync it with all running instances
		fmt.Printf("\nSyncing cluster '%s' with all running instances...\n", clusterName)

		// Build set of existing instance IDs
		existingIDs := make(map[string]bool)
		for _, inst := range cluster.Instances {
			existingIDs[inst.ID] = true
		}

		// Add any new instances not already in cluster
		for _, inst := range allInstances {
			if !existingIDs[inst.ID] {
				fmt.Printf("  → Adding instance: %s (%s) @ %s\n", inst.Name, inst.ID[:16], inst.IP)
				if addErr := cluster.AddInstance(inst); addErr != nil {
					fmt.Printf("    ⚠️  Failed to add: %v\n", addErr)
				} else {
					fmt.Printf("    ✅ Added!\n")
				}
			}
		}

		// Update cluster instances to match all running instances
		cluster.Instances = allInstances
	}

	fmt.Printf("Cluster '%s' has %d instances\n", clusterName, len(cluster.Instances))

	// 2. Connect to the cluster instances FIRST (required for heartbeat to work)
	fmt.Println("2. Connecting to cluster instances...")

	if err := cluster.Connect(); err != nil {
		t.Fatalf("Failed to connect to cluster: %v", err)
	}
	defer cluster.Disconnect()

	// Wait for all instances to be healthy (requires SSH connection)
	if err := waitForHealthyCluster(cluster, len(cluster.Instances), 10*time.Minute); err != nil {
		t.Fatalf("Failed to get healthy cluster: %v", err)
	}

	fmt.Printf("Connected to %d instances in cluster\n", len(cluster.Instances))

	// Clean up any running alloy containers on existing instances before proceeding
	cleanupAlloyContainers(cluster)

	// 3. Ensure firewall allows Loki port (3100)
	fmt.Println("\n3. Configuring firewall to allow Loki traffic on port 3100...")
	if err := ensureLokiFirewallRule(httpClient, apiToken); err != nil {
		t.Fatalf("Failed to configure firewall for Loki: %v", err)
	}

	// 4. Setup Loki on the first instance (gpu-0) for centralized logging
	fmt.Println("\n4. Setting up Loki logging on gpu-0...")
	loggingInstance := cluster.Instances[0]
	fmt.Printf("Using instance %s (%s) for Loki logging\n", loggingInstance.Name, loggingInstance.ID[:16])

	// Setup Loki on the logging instance
	setupLokiOnInstance(cluster, loggingInstance.IP)

	// Get the public IP using ipify
	lokiEndpoint, err := getLokiEndpointViaIpify(cluster, loggingInstance.IP)
	if err != nil {
		t.Fatalf("Failed to get Loki endpoint via ipify: %v", err)
	}

	fmt.Printf("Using Loki endpoint: %s\n", lokiEndpoint)

	// Validate endpoint format with regex
	endpointRegex := `^https?://[^\s/$.?#].[^\s]*$`
	if matched, _ := regexp.MatchString(endpointRegex, lokiEndpoint); !matched {
		t.Fatalf("Invalid LOKI_ENDPOINT format: %s (expected format: http://host:port/path)", lokiEndpoint)
	}

	// 5. Setup: Check and install llama.cpp if needed
	fmt.Println("\n5. Setting up llama.cpp on cluster instances...")
	hfToken := os.Getenv("HF_TOKEN")
	if hfToken == "" {
		fmt.Println("Warning: HF_TOKEN not set, model download may fail")
	}

	setupLlamaCppDocker(cluster, hfToken)

	// 6. Define llama.cpp server tasks for different configurations
	fmt.Println("\n6. Defining llama.cpp server tasks...")

	// Task for a Llama 3.1 8B model running in Docker container with conditional image pulling
	llama8BTask := remote.Task{
		Name: "llama-3.1-8b-docker-server",
		Command: `#!/bin/bash
# Check if container is already running
if sudo docker ps --filter name=llama-server --filter status=running | grep -q llama-server; then
    echo "Container llama-server is already running"
    exit 0
fi

# Check if Docker image exists, pull if not
if ! sudo docker images voplica/llama-cpp | grep -q llama-cpp; then
    echo "Pulling voplica/llama-cpp:latest image..."
    sudo docker pull voplica/llama-cpp:latest
fi

# Remove any stopped containers with the same name
sudo docker rm -f llama-server 2>/dev/null || true

# Start the container
sudo docker run --gpus all -v /home/ubuntu/models:/models -p 8080:8080 --name llama-server --restart unless-stopped -d voplica/llama-cpp:latest --server -m /models/llama-3.1-8b.gguf --port 8080 --ctx-size 4096 --host 0.0.0.0

echo 'Container started successfully'
`,
		Background: true,
		WorkingDir: "/home/ubuntu",
		Env: map[string]string{
			"CUDA_VISIBLE_DEVICES": "0",
		},
	}

	// 7. Deploy tasks to different instances
	fmt.Println("7. Deploying llama.cpp servers to cluster instances...")

	// Create Alloy task with Orgo Loki endpoint
	alloyTask := remote.Task{
		Name: "grafana-alloy-docker-logs",
		Command: fmt.Sprintf(`#!/bin/bash
# Check if Alloy container is already running
if sudo docker ps --filter name=alloy-docker-logs --filter status=running | grep -q alloy-docker-logs; then
    echo "Alloy container alloy-docker-logs is already running"
    exit 0
fi

# Check if Docker image exists, pull if not
if ! sudo docker images grafana/alloy | grep -q alloy; then
    echo "Pulling grafana/alloy:latest image..."
    sudo docker pull grafana/alloy:latest
fi

# Use Orgo Loki endpoint passed from test setup
LOKI_ENDPOINT="%s"
echo "Using Orgo Loki endpoint: $LOKI_ENDPOINT"`, lokiEndpoint),
		Background: true,
		WorkingDir: "/home/ubuntu",
		Env: map[string]string{
			"CUDA_VISIBLE_DEVICES": "0",
		},
	}

	// Deploy tasks to all instances (both llama server and alloy)
	for _, task := range []remote.Task{llama8BTask} {
		fmt.Printf("Deploying %s to all instances...\n", task.Name)

		// Run task asynchronously on all instances
		resultChan := make(chan map[string]error, 1)
		go func(task remote.Task) {
			results := cluster.RunTaskOnCluster(task)
			resultChan <- results
		}(task)

		// Wait for completion with timeout
		select {
		case results := <-resultChan:
			successCount := 0
			for instanceID, err := range results {
				if err != nil {
					fmt.Printf("❌ Task %s failed on instance %s: %v\n", task.Name, instanceID, err)
				} else {
					fmt.Printf("✅ Task %s completed successfully on instance %s\n", task.Name, instanceID)
					successCount++
				}
			}
			fmt.Printf("Task %s deployed on %d/%d instances\n", task.Name, successCount, len(cluster.Instances))
		case <-time.After(5 * time.Minute):
			fmt.Printf("⏰ Task %s timed out after 5 minutes\n", task.Name)
		}
		fmt.Println()
	}

	// Deploy Alloy task to all instances with instance-specific metadata
	fmt.Println("Deploying grafana-alloy-docker-logs to all instances...")

	// Create a dynamic Alloy task that gets instance metadata from cluster context
	alloyTask = remote.Task{
		Name: "grafana-alloy-docker-logs",
		Command: fmt.Sprintf(`#!/bin/bash
		# Check if Alloy container is already running
		if sudo docker ps --filter name=alloy-docker-logs --filter status=running | grep -q alloy-docker-logs; then
			echo "Alloy container alloy-docker-logs is already running"
			exit 0
		fi

		# Check if Docker image exists, pull if not
		if ! sudo docker images grafana/alloy | grep -q alloy; then
			echo "Pulling grafana/alloy:latest image..."
			sudo docker pull grafana/alloy:latest
		fi

		# Use Orgo Loki endpoint passed from test setup
		LOKI_ENDPOINT="%s"
		echo "Using Orgo Loki endpoint: $LOKI_ENDPOINT"

		# Try to detect instance metadata (this is a simplified approach)
		# In a production system, this would be passed via environment variables
		INSTANCE_ID="${INSTANCE_ID:-unknown}"
		INSTANCE_IP="${INSTANCE_IP:-unknown}"
		INSTANCE_NAME="${INSTANCE_NAME:-unknown}"

		# Try to get instance info from hostname or other sources
		if [ "$INSTANCE_ID" = "unknown" ]; then
			# Try to get instance ID from hostname or metadata
			INSTANCE_ID=$(hostname | cut -d'-' -f1)
			INSTANCE_IP=$(hostname -I | awk '{print $1}')
			INSTANCE_NAME=$(hostname)
		fi

		echo "Detected instance metadata - ID: $INSTANCE_ID, IP: $INSTANCE_IP, Name: $INSTANCE_NAME"

		# Create loki directory and generate config with endpoint and instance metadata
		mkdir -p /home/ubuntu/loki
		cat > /home/ubuntu/loki/config.alloy << EOF
		logging {
		level  = "info"
		format = "logfmt"
		}

		// Discover Docker containers
		discovery.docker "docker_containers" {
		host = "unix:///var/run/docker.sock"
		}

		// Extract logs from Docker containers with instance metadata
		loki.source.docker "docker_logs" {
		host = "unix:///var/run/docker.sock"
		targets = discovery.docker.docker_containers.targets
		forward_to = [loki.process.add_instance_labels.receiver]
		}

		// Add instance metadata labels to all log entries
		loki.process "add_instance_labels" {
		stage.match {
			selector = "{container_name=~\".*\"}"
			stage.label_drop {
			values = ["container_id", "image_name", "image_id"]
			}
		}

		stage.labels {
			values = {
			instance_id   = "$INSTANCE_ID",
			instance_ip   = "$INSTANCE_IP",
			instance_name = "$INSTANCE_NAME",
			host          = "$INSTANCE_NAME",
			}
		}

		forward_to = [loki.write.http.receiver]
		}

		// Write logs to Loki endpoint
		loki.write "http" {
		endpoint {
			url = "$LOKI_ENDPOINT"
		}
		}
		EOF

		# Remove any stopped containers with the same name
		sudo docker rm -f alloy-docker-logs 2>/dev/null || true

		# Start the Alloy container with Docker socket access
		sudo docker run -d --name alloy-docker-logs \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v /home/ubuntu/loki/config.alloy:/etc/alloy/config.alloy \
		-p 12345:12345 \
		grafana/alloy:latest \
		run --server.http.listen-addr=0.0.0.0:12345 \
		--storage.path=/var/lib/alloy/data \
		/etc/alloy/config.alloy

		echo "Alloy container started successfully with endpoint: $LOKI_ENDPOINT"
		`, lokiEndpoint),
		Background: true,
		WorkingDir: "/home/ubuntu",
		Env: map[string]string{
			"CUDA_VISIBLE_DEVICES": "0",
		},
	}

	// Run Alloy task on all instances
	resultChan := make(chan map[string]error, 1)
	go func() {
		results := cluster.RunTaskOnCluster(alloyTask)
		resultChan <- results
	}()

	// Wait for completion with timeout
	select {
	case results := <-resultChan:
		successCount := 0
		for instanceID, err := range results {
			if err != nil {
				fmt.Printf("❌ Task grafana-alloy-docker-logs failed on instance %s: %v\n", instanceID, err)
			} else {
				fmt.Printf("✅ Task grafana-alloy-docker-logs completed successfully on instance %s\n", instanceID)
				successCount++
			}
		}
		fmt.Printf("Alloy deployed on %d/%d instances\n", successCount, len(cluster.Instances))
	case <-time.After(3 * time.Minute):
		fmt.Printf("⏰ Task grafana-alloy-docker-logs timed out after 3 minutes\n")
	}
	fmt.Println()

	// 8. Check task health (background processes)
	fmt.Println("8. Checking task health...")
	time.Sleep(10 * time.Second) // Give tasks time to start

	taskHealth := cluster.CheckTaskHealth()
	fmt.Printf("Task Health Summary:\n")
	fmt.Printf("Running tasks: %d/%d\n", taskHealth.RunningTasks, taskHealth.TotalTasks)
	fmt.Printf("Checked at: %s\n\n", taskHealth.CheckedAt.Format(time.RFC3339))

	// Display results for each task
	for _, result := range taskHealth.Results {
		status := "✅ RUNNING"
		if !result.IsRunning {
			status = "❌ STOPPED"
		}

		fmt.Printf("%s Task '%s' on %s (%s):\n", status, result.TaskName, result.InstanceID, result.InstanceIP)
		if result.SessionName != "" {
			fmt.Printf("  Session: %s\n", result.SessionName)
		}
		if result.Error != nil {
			fmt.Printf("  Error: %v\n", result.Error)
		}
		fmt.Println()
	}

	// Check specific tasks
	llamaRunning := cluster.IsTaskRunning("llama-3.1-8b-docker-server")
	alloyRunning := cluster.IsTaskRunning("grafana-alloy-docker-logs")

	if llamaRunning {
		fmt.Println("🎉 llama-3.1-8b-docker-server is running!")
	} else {
		fmt.Println("⚠️  llama-3.1-8b-docker-server is not running")
	}

	if alloyRunning {
		fmt.Println("🎉 grafana-alloy-docker-logs is running!")
	} else {
		fmt.Println("⚠️  grafana-alloy-docker-logs is not running")
	}

	// 9. Test cluster heartbeat
	fmt.Println("9. Test cluster heartbeat...")
	testClusterHeartbeat(t, cluster)

	// 10. Test API endpoints (if services are accessible)
	fmt.Println("10. Testing llama.cpp API endpoints...")
	testAPIEndpoints(cluster, cluster.Instances)

	// start load balancing processes
	tasks := setupGotoniBinaryOnInstance(cluster)
	if tasks == nil {
		fmt.Println("❌ Failed to setup gotoni binary on instances")
		return
	}

	for i, task := range tasks {
		err := remote.ExecuteTask(cluster.sshMgr, cluster.Instances[i].IP, task, make(map[string]bool))
		if err != nil {
			fmt.Printf("❌ Failed to start gotoni load balancer on %s: %v\n", cluster.Instances[i].IP, err)
		} else {
			fmt.Printf("✅ Gotoni load balancer started on %s\n", cluster.Instances[i].IP)
		}
	}

	// Test load balancers on cluster using the gotoni binary we already have
	fmt.Println("Testing remote load balancer status...")

	// Build gotoni binary from project root (../../ from pkg/cluster/)
	fmt.Println("🏗️  Building gotoni binary for test...")
	buildCmd := exec.Command("go", "build", "-o", "../../gotoni", "../../.")
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		fmt.Printf("❌ Failed to build gotoni binary: %v\n", err)
		return
	}

	// Run lb-remote-status from the built binary
	statusCmd := exec.Command("../../gotoni", "lb-remote-status", "--all")
	statusCmd.Stdout = os.Stdout
	statusCmd.Stderr = os.Stderr
	if err := statusCmd.Run(); err != nil {
		fmt.Printf("❌ Failed to test remote load balancers: %v\n", err)
		return
	}
	fmt.Println("✅ Remote load balancers tested successfully")
	fmt.Println("=== Cluster Llama.cpp Test Complete ===")
}

// testAPIEndpoints tests the llama.cpp server endpoints
func testAPIEndpoints(cluster *Cluster, instances []remote.RunningInstance) {
	testClient := &http.Client{Timeout: 10 * time.Second}

	// All servers run on port 8080
	port := 8080

	for _, instance := range instances {
		url := fmt.Sprintf("http://%s:%d/health", instance.IP, port)

		resp, err := testClient.Get(url)
		if err != nil {
			fmt.Printf("❌ API test failed for %s:%d - %v\n", instance.IP, port, err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == 200 {
			fmt.Printf("✅ API endpoint %s:%d is responding\n", instance.IP, port)
		} else {
			fmt.Printf("⚠️  API endpoint %s:%d returned status %d\n", instance.IP, port, resp.StatusCode)
		}
	}
}

// ExampleLlamaCppDeployment shows a complete deployment workflow
func exampleLlamaCppDeployment() {
	fmt.Println("=== Example: Complete Llama.cpp Cluster Deployment ===")

	// This would typically be called from main() or a CLI command

	// 1. Setup
	httpClient := remote.NewHTTPClient()
	apiToken := os.Getenv("LAMBDA_API_KEY")

	// 2. Launch cluster with GPU instances optimized for llama.cpp
	cluster := LaunchCluster(httpClient, apiToken)

	// 3. Wait for instances to be fully ready
	fmt.Println("Waiting for instances to be ready...")
	time.Sleep(2 * time.Minute) // In practice, implement proper polling

	// 4. Cluster is already initialized
	defer cluster.Disconnect()

	// 5. Setup each instance (install dependencies, download models, etc.)
	setupTasks := []remote.Task{
		NewForegroundTask("install-deps", "apt update && apt install -y wget git build-essential", "/home/ubuntu", nil),
		NewForegroundTask("download-llama", "git clone https://github.com/ggerganov/llama.cpp.git && cd llama.cpp && make", "/home/ubuntu", nil),
		NewForegroundTask("download-model", "wget -O llama-7b.gguf https://example.com/model.gguf", "/home/ubuntu/models", nil),
	}

	fmt.Println("Running setup tasks...")
	results := cluster.RunTasksOnCluster(setupTasks)
	for instanceID, err := range results {
		if err != nil {
			fmt.Printf("Setup failed on %s: %v\n", instanceID, err)
			return
		}
	}

	// 6. Connect to cluster
	if err := cluster.Connect(); err != nil {
		fmt.Printf("Failed to connect: %v\n", err)
		return
	}

	// 7. Deploy llama.cpp servers using background execution (tmux)
	llamaServer := remote.Task{
		Name:       "llama-server",
		Command:    "./llama-server -m llama-7b.gguf --port 8080 --ctx-size 4096 --host 0.0.0.0",
		Background: true,
		WorkingDir: "/home/ubuntu/models",
		Env: map[string]string{
			"CUDA_VISIBLE_DEVICES": "0",
		},
	}

	fmt.Println("Deploying llama.cpp servers in background...")
	serverResults := cluster.RunTaskOnCluster(llamaServer)

	// 8. Report results
	successCount := 0
	for instanceID, err := range serverResults {
		if err != nil {
			fmt.Printf("❌ Deployment failed on %s: %v\n", instanceID, err)
		} else {
			fmt.Printf("✅ Llama.cpp server running on %s (tmux session)\n", instanceID)
			successCount++
		}
	}

	fmt.Printf("Successfully deployed llama.cpp servers on %d/%d instances\n", successCount, len(cluster.Instances))

	// 9. Check task health
	fmt.Println("\n=== Task Health Check ===")
	time.Sleep(5 * time.Second) // Give tasks time to start

	taskSummary := cluster.CheckTaskHealth()
	fmt.Printf("Tasks running: %d/%d\n", taskSummary.RunningTasks, taskSummary.TotalTasks)

	runningTasks := cluster.GetRunningTasks()
	fmt.Printf("Running task names: %v\n", runningTasks)
}

// waitForHealthyCluster waits for the specified number of healthy instances
func waitForHealthyCluster(cluster *Cluster, minHealthy int, timeout time.Duration) error {
	startTime := time.Now()
	pollInterval := 15 * time.Second

	for {
		heartbeat := cluster.Heartbeat()
		healthyCount := heartbeat.HealthyInstances

		if healthyCount >= minHealthy {
			fmt.Printf("Found %d healthy instances!\n", healthyCount)
			return nil
		}

		if time.Since(startTime) > timeout {
			return fmt.Errorf("timeout: only %d/%d healthy instances after %v", healthyCount, heartbeat.TotalInstances, timeout)
		}

		fmt.Printf("Currently %d healthy instances. Waiting for more to become available...\n", healthyCount)
		time.Sleep(pollInterval)
	}
}

// testClusterHeartbeat tests the heartbeat functionality (helper function)
func testClusterHeartbeat(t *testing.T, cluster *Cluster) {
	fmt.Println("=== Testing Cluster Heartbeat ===")

	// Perform heartbeat check
	heartbeat := cluster.Heartbeat()

	fmt.Printf("Cluster Health Summary:\n")
	fmt.Printf("Total instances: %d\n", heartbeat.TotalInstances)
	fmt.Printf("Healthy instances: %d\n", heartbeat.HealthyInstances)
	fmt.Printf("Checked at: %s\n\n", heartbeat.CheckedAt.Format(time.RFC3339))

	// Display results for each instance
	for _, result := range heartbeat.Results {
		status := "✅ HEALTHY"
		if !result.Healthy {
			status = "❌ UNHEALTHY"
		}

		fmt.Printf("%s Instance %s (%s):\n", status, result.InstanceID, result.InstanceIP)
		fmt.Printf("  Response time: %v\n", result.ResponseTime)

		if result.Error != nil {
			fmt.Printf("  Error: %v\n", result.Error)
		}
		fmt.Println()
	}

	// Check overall cluster health
	if cluster.IsHealthy() {
		fmt.Println("🎉 All cluster instances are healthy!")
	} else {
		fmt.Printf("⚠️  Only %d/%d instances are healthy\n", heartbeat.HealthyInstances, heartbeat.TotalInstances)
	}

	// Get list of healthy instances
	healthyIPs := cluster.GetHealthyInstances()
	fmt.Printf("Healthy instance IPs: %v\n", healthyIPs)
}

// ExampleClusterSpecs demonstrates different cluster configurations
func exampleClusterSpecs() {
	fmt.Println("=== Example Cluster Specifications ===")

	httpClient := remote.NewHTTPClient()
	apiToken := os.Getenv("LAMBDA_API_KEY")

	// First, let's explore available instance types
	fmt.Println("Available GPU Instance Types:")
	gpuTypes, err := FilterInstanceTypesByGPU(httpClient, apiToken)
	if err != nil {
		fmt.Printf("Failed to get GPU types: %v\n", err)
	} else {
		for _, gpuType := range gpuTypes {
			fmt.Printf("  %s\n", gpuType.FormatInstanceType())
		}
	}

	fmt.Println("\nBudget Instance Types (under $2/hour):")
	budgetTypes, err := GetInstanceTypesByPriceRange(httpClient, apiToken, 0, 200)
	if err != nil {
		fmt.Printf("Failed to get budget types: %v\n", err)
	} else {
		for _, budgetType := range budgetTypes {
			fmt.Printf("  %s\n", budgetType.FormatInstanceType())
		}
	}

	// Example 1: GPU cluster for AI/ML workloads
	gpuClusterSpec := &ClusterSpec{
		Name: "gpu-cluster",
		Replicas: []ClusterReplicaSpec{
			{
				InstanceType: "gpu_1x_a100_sxm4",
				Region:       "us-east-1",
				Quantity:     2,
				Name:         "gpu-east",
			},
			{
				InstanceType: "gpu_1x_a100_sxm4",
				Region:       "us-west-1",
				Quantity:     2,
				Name:         "gpu-west",
			},
		},
	}

	// Example 2: CPU cluster for web services
	cpuClusterSpec := &ClusterSpec{
		Name: "cpu-cluster",
		Replicas: []ClusterReplicaSpec{
			{
				InstanceType: "cpu_4x", // Assuming this exists
				Region:       "us-east-1",
				Quantity:     3,
				Name:         "web-east",
			},
			{
				InstanceType: "cpu_4x",
				Region:       "eu-west-1",
				Quantity:     3,
				Name:         "web-eu",
			},
		},
	}

	// Example 3: Mixed cluster with different instance types
	mixedClusterSpec := &ClusterSpec{
		Name: "mixed-cluster",
		Replicas: []ClusterReplicaSpec{
			{
				InstanceType: "gpu_1x_a100_sxm4",
				Region:       "us-east-1",
				Quantity:     1,
				Name:         "gpu-head",
			},
			{
				InstanceType: "cpu_8x",
				Region:       "us-east-1",
				Quantity:     4,
				Name:         "cpu-worker",
			},
		},
	}

	fmt.Printf("\nGPU Cluster Spec: %+v\n\n", gpuClusterSpec)
	fmt.Printf("CPU Cluster Spec: %+v\n\n", cpuClusterSpec)
	fmt.Printf("Mixed Cluster Spec: %+v\n\n", mixedClusterSpec)

	fmt.Println("To launch any of these clusters:")
	fmt.Println("cluster, err := LaunchClusterFromSpec(httpClient, apiToken, gpuClusterSpec)")

	// Demonstrate getting instance type details
	if len(gpuTypes) > 0 {
		firstGPU := gpuTypes[0]
		fmt.Printf("\nDetailed info for %s:\n", firstGPU.Name)
		fmt.Printf("  Description: %s\n", firstGPU.Description)
		fmt.Printf("  GPU: %s\n", firstGPU.GPUDescription)
		fmt.Printf("  Specs: %d vCPUs, %d GB RAM, %d GB storage, %d GPUs\n",
			firstGPU.Specs.VCPUs, firstGPU.Specs.MemoryGib, firstGPU.Specs.StorageGib, firstGPU.Specs.GPUs)
		fmt.Printf("  Price: $%.2f/hour ($%.2f/month est.)\n",
			float64(firstGPU.PriceCentsPerHour)/100.0, firstGPU.GetEstimatedMonthlyCost())
		fmt.Printf("  Is GPU instance: %t\n", firstGPU.IsGPUInstance())
	}
}

// setupLlamaCppDocker sets up Docker-based llama.cpp deployment (parallel)
func setupLlamaCppDocker(cluster *Cluster, hfToken string) {
	fmt.Println("Checking llama.cpp installation on all instances...")

	// Check if llama.cpp is installed
	checkCmd := "test -f /home/ubuntu/llama.cpp/llama-server && echo 'installed' || echo 'not_installed'"
	results := cluster.ExecuteOnCluster(checkCmd)

	// Collect instances that need installation
	var needsInstall []string
	for instanceID, result := range results {
		if result.Error != nil {
			fmt.Printf("❌ Failed to check llama.cpp on %s: %v\n", instanceID[:16], result.Error)
			continue
		}

		output := strings.TrimSpace(result.Output)
		if output == "installed" {
			fmt.Printf("✅ llama.cpp already installed on %s\n", instanceID[:16])
		} else {
			needsInstall = append(needsInstall, instanceID)
		}
	}

	// Install llama.cpp in parallel on all instances that need it
	if len(needsInstall) > 0 {
		fmt.Printf("\n📦 Installing llama.cpp on %d instances in parallel...\n", len(needsInstall))
		var wg sync.WaitGroup
		for _, instanceID := range needsInstall {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				fmt.Printf("  → Starting install on %s...\n", id[:16])
				installLlamaCpp(cluster, id)
			}(instanceID)
		}
		wg.Wait()
		fmt.Println("✅ All llama.cpp installations complete!")
	}

	// Check if model exists
	fmt.Println("\nChecking model file on all instances...")
	modelCheckCmd := "test -f /home/ubuntu/models/llama-3.1-8b.gguf && echo 'exists' || echo 'not_found'"
	modelResults := cluster.ExecuteOnCluster(modelCheckCmd)

	// Collect instances that need model download
	var needsModel []string
	for instanceID, result := range modelResults {
		if result.Error != nil {
			fmt.Printf("❌ Failed to check model on %s: %v\n", instanceID[:16], result.Error)
			continue
		}

		output := strings.TrimSpace(result.Output)
		if output == "exists" {
			fmt.Printf("✅ Model already exists on %s\n", instanceID[:16])
		} else {
			needsModel = append(needsModel, instanceID)
		}
	}

	// Download model in parallel on all instances that need it
	if len(needsModel) > 0 {
		fmt.Printf("\n📥 Downloading model on %d instances in parallel...\n", len(needsModel))
		var wg sync.WaitGroup
		for _, instanceID := range needsModel {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				fmt.Printf("  → Starting download on %s...\n", id[:16])
				downloadModel(cluster, id, hfToken)
			}(instanceID)
		}
		wg.Wait()
		fmt.Println("✅ All model downloads complete!")
	}
}

func setupGotoniBinaryOnInstance(cluster *Cluster) []remote.Task {
	return setupGotoniBinaryOnInstanceWithStrategy(cluster, "least-loaded")
}

// setupGotoniBinaryOnInstanceWithStrategy builds gotoni with CGO directly on Linux instances
// This enables the tokenizer for accurate GORGO routing
func setupGotoniBinaryOnInstanceWithStrategy(cluster *Cluster, strategy string) []remote.Task {
	fmt.Printf("Setting up gotoni binary on all instances (strategy: %s)...\n", strategy)
	fmt.Println("Building DIRECTLY on Linux instances to enable CGO tokenizer")

	out := []remote.Task{}
	projectRoot := "../../"

	// Create tarball of source code
	fmt.Println("Creating source tarball...")
	tarballPath := "/tmp/gotoni-src.tar.gz"

	// Create tarball excluding .git, vendor, and binaries
	tarCmd := exec.Command("tar",
		"--exclude=.git",
		"--exclude=vendor",
		"--exclude=gotoni",
		"--exclude=gotoni-amd64",
		"--exclude=gotoni-arm64",
		"--exclude=*.tar.gz",
		"-czf", tarballPath,
		"-C", projectRoot,
		".",
	)
	tarCmd.Stdout = os.Stdout
	tarCmd.Stderr = os.Stderr
	if err := tarCmd.Run(); err != nil {
		fmt.Printf("❌ Failed to create tarball: %v\n", err)
		return nil
	}
	fmt.Printf("✅ Source tarball created: %s\n", tarballPath)

	// Build script that installs dependencies and builds gotoni with CGO
	buildScript := `#!/bin/bash
set -e

echo "=== Setting up gotoni build environment ==="

# Install Go if not present
if ! command -v go &> /dev/null; then
    echo "Installing Go..."
    wget -q https://go.dev/dl/go1.21.5.linux-amd64.tar.gz -O /tmp/go.tar.gz
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
fi
export PATH=$PATH:/usr/local/go/bin
export GOPATH=/home/ubuntu/go
export PATH=$PATH:$GOPATH/bin

echo "Go version: $(go version)"

# Install Rust if not present (needed for tokenizer library)
if ! command -v cargo &> /dev/null; then
    echo "Installing Rust..."
    curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
fi
source $HOME/.cargo/env 2>/dev/null || true

echo "Rust version: $(rustc --version 2>/dev/null || echo 'installing...')"

# Install build dependencies
echo "Installing build dependencies..."
sudo apt-get update -qq
sudo apt-get install -y -qq build-essential pkg-config libssl-dev git

# Build tokenizer library if not present
TOKENIZER_LIB="/usr/local/lib/libtokenizers.a"
if [ ! -f "$TOKENIZER_LIB" ]; then
    echo "Building tokenizer library (this may take a few minutes)..."
    rm -rf /tmp/tokenizers-build
    git clone --depth 1 https://github.com/daulet/tokenizers.git /tmp/tokenizers-build
    cd /tmp/tokenizers-build
    source $HOME/.cargo/env
    make build
    sudo cp libtokenizers.a /usr/local/lib/
    echo "✅ Tokenizer library installed"
else
    echo "✅ Tokenizer library already installed"
fi

# Extract source code
echo "Extracting gotoni source..."
rm -rf /home/ubuntu/gotoni-src
mkdir -p /home/ubuntu/gotoni-src
tar -xzf /home/ubuntu/gotoni-src.tar.gz -C /home/ubuntu/gotoni-src

# Build gotoni with CGO enabled
echo "Building gotoni with CGO (tokenizer enabled)..."
cd /home/ubuntu/gotoni-src
export CGO_ENABLED=1
export CGO_LDFLAGS="-L/usr/local/lib"

# Add tokenizer Go module dependency (not in go.mod since local builds use CGO_ENABLED=0)
go get github.com/daulet/tokenizers

go build -o /home/ubuntu/gotoni .

# Verify build
if [ -f /home/ubuntu/gotoni ]; then
    echo "✅ gotoni built successfully!"
    file /home/ubuntu/gotoni
    # Test tokenizer
    echo "Testing tokenizer..."
    /home/ubuntu/gotoni lb status 2>&1 || echo "(lb not running, expected)"
else
    echo "❌ gotoni build failed"
    exit 1
fi
`

	// Track which instances we've set up
	var wg sync.WaitGroup
	resultsMu := sync.Mutex{}
	results := make(map[string]bool)

	for _, inst := range cluster.Instances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()

			fmt.Printf("  → Setting up gotoni on %s (%s)...\n", instance.Name, instance.IP)

			// Get SSH key for SCP
			var sshKeyPath string
			if len(instance.SSHKeyNames) > 0 {
				sshKey, err := cluster.database.GetSSHKey(instance.SSHKeyNames[0])
				if err != nil {
					fmt.Printf("  ❌ Failed to get SSH key for %s: %v\n", instance.Name, err)
					resultsMu.Lock()
					results[instance.ID] = false
					resultsMu.Unlock()
					return
				}
				sshKeyPath = sshKey.PrivateKey
			} else {
				fmt.Printf("  ❌ No SSH key found for %s\n", instance.Name)
				resultsMu.Lock()
				results[instance.ID] = false
				resultsMu.Unlock()
				return
			}

			// Copy tarball to instance
			fmt.Printf("  → Copying source to %s...\n", instance.Name)
			scpCmd := exec.Command("scp",
				"-i", sshKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				tarballPath,
				fmt.Sprintf("ubuntu@%s:/home/ubuntu/gotoni-src.tar.gz", instance.IP),
			)
			if err := scpCmd.Run(); err != nil {
				fmt.Printf("  ❌ Failed to copy source to %s: %v\n", instance.Name, err)
				resultsMu.Lock()
				results[instance.ID] = false
				resultsMu.Unlock()
				return
			}

			// Execute build script on instance
			fmt.Printf("  → Building on %s (this may take 5-10 minutes on first run)...\n", instance.Name)
			output, err := cluster.sshMgr.ExecuteCommandWithTimeout(
				instance.IP,
				buildScript,
				20*time.Minute, // Rust compilation can take a while
			)

			resultsMu.Lock()
			if err != nil {
				fmt.Printf("  ❌ Build failed on %s: %v\n", instance.Name, err)
				if len(output) > 500 {
					output = output[len(output)-500:]
				}
				fmt.Printf("     Last output: %s\n", output)
				results[instance.ID] = false
			} else {
				fmt.Printf("  ✅ gotoni built on %s\n", instance.Name)
				results[instance.ID] = true
			}
			resultsMu.Unlock()
		}(inst)
	}

	wg.Wait()

	// Create tasks for successful builds
	for _, inst := range cluster.Instances {
		if results[inst.ID] {
			out = append(out, remote.Task{
				Name:       "start gotoni load balancer",
				Command:    fmt.Sprintf("/home/ubuntu/gotoni lb start --listen-port 8000 --local-port 8080 --strategy %s", strategy),
				Background: true,
				WorkingDir: "/home/ubuntu",
			})
		}
	}

	// Open port 8000 for load balancer
	fmt.Println("Opening port 8000 in firewall for load balancer...")
	provider, _ := remote.GetCloudProvider()
	httpClient := remote.NewHTTPClient()
	apiToken := remote.GetAPIToken()
	if err := provider.EnsurePortOpen(httpClient, apiToken, 8000, "tcp", "Allow Load Balancer (port 8000)"); err != nil {
		fmt.Printf("⚠️  Failed to open port 8000: %v\n", err)
	} else {
		fmt.Println("✅ Port 8000 opened in firewall")
	}

	successCount := 0
	for _, ok := range results {
		if ok {
			successCount++
		}
	}
	fmt.Printf("✅ gotoni binary built on %d/%d instances\n", successCount, len(cluster.Instances))

	return out
}

// installLlamaCpp installs llama.cpp on a specific instance
func installLlamaCpp(cluster *Cluster, instanceID string) {
	// Find the instance IP
	var instanceIP string
	for _, inst := range cluster.Instances {
		if inst.ID == instanceID {
			instanceIP = inst.IP
			break
		}
	}
	if instanceIP == "" {
		fmt.Printf("❌ Instance %s not found\n", instanceID[:16])
		return
	}

	// Install dependencies and build llama.cpp
	installScript := `
set -e
echo "Installing build dependencies..."
sudo apt-get update -qq
sudo apt-get install -y -qq build-essential cmake git libcurl4-openssl-dev

echo "Cloning llama.cpp..."
cd /home/ubuntu
if [ ! -d "llama.cpp" ]; then
    git clone https://github.com/ggerganov/llama.cpp.git
fi
cd llama.cpp

echo "Building llama.cpp with CUDA support..."
cmake -B build -DGGML_CUDA=ON
cmake --build build --config Release -j$(nproc)

echo "Creating symlinks..."
ln -sf build/bin/llama-server llama-server
ln -sf build/bin/llama-cli llama-cli

echo "Creating models directory..."
mkdir -p /home/ubuntu/models

echo "llama.cpp installation complete!"
`
	output, err := cluster.sshMgr.ExecuteCommandWithTimeout(instanceIP, installScript, 15*time.Minute)
	if err != nil {
		fmt.Printf("❌ Failed to install llama.cpp on %s: %v\n", instanceID[:16], err)
		fmt.Printf("Output: %s\n", output)
		return
	}
	fmt.Printf("✅ llama.cpp installed on %s\n", instanceID[:16])
}

// installLokiLocally installs Loki on the local test machine using Docker
func installLokiLocally() {
	fmt.Println("Setting up Loki in Docker container on local machine...")

	// Check if Docker is available
	dockerCheck := exec.Command("docker", "--version")
	if err := dockerCheck.Run(); err != nil {
		fmt.Println("❌ Docker is not available. Please install Docker first.")
		fmt.Println("Visit: https://docs.docker.com/get-docker/")
		return
	}

	// Create Loki configuration directory
	lokiConfigDir := "/tmp/loki-config"
	if err := os.MkdirAll(lokiConfigDir, 0755); err != nil {
		fmt.Printf("Failed to create Loki config directory: %v\n", err)
		return
	}

	// Create Loki configuration file with 0.0.0.0 binding
	lokiConfig := `auth_enabled: false

server:
  http_listen_address: 0.0.0.0
  http_listen_port: 3100
  grpc_listen_address: 0.0.0.0
  grpc_listen_port: 9096

common:
  instance_addr: 0.0.0.0
  path_prefix: /loki
  storage:
    filesystem:
      chunks_directory: /loki/chunks
      rules_directory: /loki/rules
  replication_factor: 1
  ring:
    kvstore:
      store: inmemory

query_range:
  results_cache:
    cache:
      embedded_cache:
        enabled: true
        max_size_mb: 100

schema_config:
  configs:
    - from: 2020-10-24
      store: tsdb
      object_store: filesystem
      schema: v13
      index:
        prefix: index_
        period: 24h

ruler:
  alertmanager_url: ""

analytics:
  reporting_enabled: false
`

	configFile := lokiConfigDir + "/loki-config.yaml"
	if err := os.WriteFile(configFile, []byte(lokiConfig), 0644); err != nil {
		fmt.Printf("Failed to write Loki config file: %v\n", err)
		return
	}

	fmt.Printf("✅ Created Loki configuration at %s\n", configFile)

	// Check if Loki container is already running
	checkCmd := exec.Command("docker", "ps", "--filter", "name=loki-local", "--format", "{{.Names}}")
	if output, err := checkCmd.Output(); err == nil && strings.TrimSpace(string(output)) == "loki-local" {
		fmt.Println("✅ Loki container is already running")
		// Verify it's accessible
		testLokiEndpoint("http://localhost:3100")
		return
	}

	// Remove any stopped container with the same name
	fmt.Println("Cleaning up any existing Loki containers...")
	rmCmd := exec.Command("docker", "rm", "-f", "loki-local")
	_ = rmCmd.Run() // Ignore errors if container doesn't exist

	// Start Loki container with 0.0.0.0 binding
	fmt.Println("Starting Loki container (binding to 0.0.0.0:3100)...")
	runCmd := exec.Command("docker", "run", "-d",
		"--name", "loki-local",
		"-p", "3100:3100",
		"-p", "9096:9096",
		"-v", configFile+":/etc/loki/local-config.yaml",
		"-v", "/tmp/loki-data:/loki",
		"grafana/loki:latest",
		"-config.file=/etc/loki/local-config.yaml",
	)

	output, err := runCmd.CombinedOutput()
	if err != nil {
		fmt.Printf("❌ Failed to start Loki container: %v\n", err)
		fmt.Printf("Output: %s\n", string(output))
		return
	}

	fmt.Printf("✅ Loki container started successfully\n")
	fmt.Println("Waiting for Loki to be ready...")
	time.Sleep(5 * time.Second)

	// Test the endpoint
	testLokiEndpoint("http://localhost:3100")
}

// testLokiEndpoint tests if Loki is accessible at the given endpoint
func testLokiEndpoint(endpoint string) {
	client := &http.Client{Timeout: 5 * time.Second}
	readyURL := strings.TrimSuffix(endpoint, "/loki/api/v1/push") + "/ready"

	resp, err := client.Get(readyURL)
	if err != nil {
		fmt.Printf("⚠️  Loki health check failed: %v\n", err)
		fmt.Printf("Endpoint: %s\n", readyURL)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		fmt.Printf("✅ Loki is ready at %s\n", endpoint)
	} else {
		fmt.Printf("⚠️  Loki returned status %d\n", resp.StatusCode)
	}
}

// downloadModel downloads the model from HuggingFace
func downloadModel(cluster *Cluster, instanceID string, hfToken string) {
	// Find the instance IP
	var instanceIP string
	for _, inst := range cluster.Instances {
		if inst.ID == instanceID {
			instanceIP = inst.IP
			break
		}
	}
	if instanceIP == "" {
		fmt.Printf("❌ Instance %s not found\n", instanceID[:16])
		return
	}

	// Download from HuggingFace using huggingface-cli or wget
	// Using bartowski's quantized GGUF which is publicly available
	downloadScript := fmt.Sprintf(`
set -e
cd /home/ubuntu/models

echo "Installing huggingface_hub..."
pip install -q huggingface_hub

echo "Downloading Llama 3.1 8B GGUF model..."
export HF_TOKEN="%s"
python3 -c "
from huggingface_hub import hf_hub_download
import os
token = os.environ.get('HF_TOKEN')
# Using bartowski's quantized version which is publicly available
hf_hub_download(
    repo_id='bartowski/Meta-Llama-3.1-8B-Instruct-GGUF',
    filename='Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf',
    local_dir='/home/ubuntu/models',
    token=token
)
"

# Rename to expected filename
mv -f Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf llama-3.1-8b.gguf 2>/dev/null || true

echo "Model download complete!"
ls -la /home/ubuntu/models/
`, hfToken)

	output, err := cluster.sshMgr.ExecuteCommandWithTimeout(instanceIP, downloadScript, 30*time.Minute)
	if err != nil {
		fmt.Printf("❌ Failed to download model on %s: %v\n", instanceID[:16], err)
		fmt.Printf("Output: %s\n", output)
		return
	}
	fmt.Printf("✅ Model downloaded on %s\n", instanceID[:16])
}

// cleanupAlloyContainers stops any running alloy-docker-logs containers on all instances in the cluster
func cleanupAlloyContainers(cluster *Cluster) {
	fmt.Println("🧹 Cleaning up any running alloy-docker-logs containers on existing instances...")

	for _, inst := range cluster.Instances {
		fmt.Printf("Checking instance %s (%s)...\n", inst.Name, inst.ID[:16])

		// Check if alloy container is running
		checkCommand := "sudo docker ps --filter name=alloy-docker-logs --filter status=running --format '{{.Names}}' 2>/dev/null || true"

		output, err := cluster.sshMgr.ExecuteCommand(inst.IP, checkCommand)
		if err != nil {
			fmt.Printf("⚠️  Failed to check alloy containers on %s: %v\n", inst.Name, err)
			continue
		}

		if strings.TrimSpace(output) != "" {
			fmt.Printf("Found running alloy container on %s, stopping it...\n", inst.Name)

			// Stop the alloy container
			stopCommand := "sudo docker stop alloy-docker-logs 2>/dev/null || true"
			_, stopErr := cluster.sshMgr.ExecuteCommand(inst.IP, stopCommand)
			if stopErr != nil {
				fmt.Printf("⚠️  Failed to stop alloy container on %s: %v\n", inst.Name, stopErr)
			} else {
				fmt.Printf("✅ Stopped alloy container on %s\n", inst.Name)
			}

			// Remove the stopped container
			rmCommand := "sudo docker rm -f alloy-docker-logs 2>/dev/null || true"
			_, rmErr := cluster.sshMgr.ExecuteCommand(inst.IP, rmCommand)
			if rmErr != nil {
				fmt.Printf("⚠️  Failed to remove alloy container on %s: %v\n", inst.Name, rmErr)
			}
		} else {
			fmt.Printf("No running alloy containers found on %s\n", inst.Name)
		}
	}

	fmt.Println("✅ Alloy container cleanup completed")
}

// setupLokiOnInstance sets up Loki Docker container on a specific cluster instance
func setupLokiOnInstance(cluster *Cluster, instanceIP string) {
	fmt.Printf("Setting up Loki in Docker container on instance %s...\n", instanceIP)

	// Setup Loki using Docker with 0.0.0.0 binding
	installScript := `#!/bin/bash
set -e

echo "Checking if Docker is available..."
if ! command -v docker &> /dev/null; then
    echo "Docker not found, installing..."
    curl -fsSL https://get.docker.com -o get-docker.sh
    sudo sh get-docker.sh
    rm get-docker.sh
fi

echo "Creating Loki configuration and data directories..."
sudo mkdir -p /home/ubuntu/loki-config /home/ubuntu/loki-data
# Loki runs as user 10001 in the container, ensure it can write to data directory
sudo chown -R 10001:10001 /home/ubuntu/loki-data
sudo chmod -R 755 /home/ubuntu/loki-data

echo "Creating Loki configuration file with 0.0.0.0 binding..."
sudo tee /home/ubuntu/loki-config/loki-config.yaml > /dev/null << 'EOF'
auth_enabled: false

server:
  http_listen_address: 0.0.0.0
  http_listen_port: 3100
  grpc_listen_address: 0.0.0.0
  grpc_listen_port: 9096

common:
  instance_addr: 0.0.0.0
  path_prefix: /loki
  storage:
    filesystem:
      chunks_directory: /loki/chunks
      rules_directory: /loki/rules
  replication_factor: 1
  ring:
    kvstore:
      store: inmemory

query_range:
  results_cache:
    cache:
      embedded_cache:
        enabled: true
        max_size_mb: 100

schema_config:
  configs:
    - from: 2020-10-24
      store: tsdb
      object_store: filesystem
      schema: v13
      index:
        prefix: index_
        period: 24h

ruler:
  alertmanager_url: ""

analytics:
  reporting_enabled: false
EOF

echo "Checking if Loki container already exists..."
if sudo docker ps -a --filter name=loki-cluster --format '{{.Names}}' | grep -q loki-cluster; then
    echo "Stopping and removing existing Loki container..."
    sudo docker stop loki-cluster 2>/dev/null || true
    sudo docker rm loki-cluster 2>/dev/null || true
fi

echo "Pulling Grafana Loki Docker image..."
sudo docker pull grafana/loki:latest

echo "Starting Loki container (binding to 0.0.0.0:3100)..."
sudo docker run -d \
  --name loki-cluster \
  --restart unless-stopped \
  --user 10001:10001 \
  -p 3100:3100 \
  -p 9096:9096 \
  -v /home/ubuntu/loki-config/loki-config.yaml:/etc/loki/local-config.yaml:ro \
  -v /home/ubuntu/loki-data:/loki \
  grafana/loki:latest \
  -config.file=/etc/loki/local-config.yaml

echo "Waiting for Loki to start (15 seconds)..."
sleep 15

echo "Checking Loki container status..."
sudo docker ps --filter name=loki-cluster

echo "Checking container logs..."
sudo docker logs loki-cluster 2>&1 | tail -20

echo "Testing Loki endpoint..."
for i in 1 2 3 4 5; do
    if curl -sf http://localhost:3100/ready; then
        echo "Loki is ready!"
        break
    fi
    echo "Loki not ready yet, attempt $i/5..."
    sleep 5
done

echo "Loki setup complete!"
`

	output, err := cluster.sshMgr.ExecuteCommandWithTimeout(instanceIP, installScript, 10*time.Minute)
	if err != nil {
		fmt.Printf("❌ Failed to setup Loki on instance: %v\n", err)
		fmt.Printf("Output: %s\n", output)
		return
	}

	fmt.Printf("✅ Loki Docker setup output:\n%s\n", output)
	fmt.Println("✅ Loki is now running in Docker container with 0.0.0.0 binding")
}

// getLokiEndpointViaIpify gets the public IP of an instance using ipify service
func getLokiEndpointViaIpify(cluster *Cluster, instanceIP string) (string, error) {
	fmt.Println("Getting public IP via ipify service...")

	// Use ipify to get the public IP
	ipifyScript := `curl -s https://api.ipify.org`

	output, err := cluster.sshMgr.ExecuteCommand(instanceIP, ipifyScript)
	if err != nil {
		return "", fmt.Errorf("failed to get public IP via ipify: %w", err)
	}

	publicIP := strings.TrimSpace(output)
	if publicIP == "" {
		return "", fmt.Errorf("ipify returned empty IP")
	}

	fmt.Printf("Got public IP from ipify: %s\n", publicIP)

	// Construct the Loki endpoint
	lokiEndpoint := fmt.Sprintf("http://%s:3100/loki/api/v1/push", publicIP)

	// Verify Loki is accessible on the public IP
	verifyScript := fmt.Sprintf(`curl -s -o /dev/null -w "%%{http_code}" http://%s:3100/ready || echo "000"`, publicIP)
	statusOutput, _ := cluster.sshMgr.ExecuteCommand(instanceIP, verifyScript)
	statusCode := strings.TrimSpace(statusOutput)

	if statusCode == "200" {
		fmt.Printf("✅ Loki is accessible at %s\n", lokiEndpoint)
	} else {
		fmt.Printf("⚠️  Loki returned status %s (may need firewall adjustment)\n", statusCode)
	}

	return lokiEndpoint, nil
}

// ensureLokiFirewallRule ensures that port 3100 is open in the firewall for Loki
func ensureLokiFirewallRule(httpClient *http.Client, apiToken string) error {
	fmt.Println("Checking current firewall rules...")

	// Get current firewall rules
	ruleset, err := remote.GetGlobalFirewallRules(httpClient, apiToken)
	if err != nil {
		return fmt.Errorf("failed to get firewall rules: %w", err)
	}

	// Check if port 3100 is already open
	lokiPort := 3100
	for _, rule := range ruleset.Rules {
		if rule.Protocol == "tcp" && len(rule.PortRange) == 2 {
			if rule.PortRange[0] <= lokiPort && rule.PortRange[1] >= lokiPort {
				fmt.Printf("✅ Port %d already open in firewall (rule: %s)\n", lokiPort, rule.Description)
				return nil
			}
		}
	}

	// Port 3100 not open, add it
	fmt.Printf("Adding firewall rule for Loki port %d...\n", lokiPort)

	// Create new rules list with existing rules plus Loki rule
	newRules := append(ruleset.Rules, remote.FirewallRule{
		Protocol:      "tcp",
		PortRange:     []int{lokiPort, lokiPort},
		SourceNetwork: "0.0.0.0/0",
		Description:   "Allow Loki logging (port 3100)",
	})

	// Update firewall rules
	_, err = remote.UpdateGlobalFirewallRules(httpClient, apiToken, newRules)
	if err != nil {
		return fmt.Errorf("failed to update firewall rules: %w", err)
	}

	fmt.Printf("✅ Firewall rule added for port %d\n", lokiPort)
	return nil
}

// setupSGLangCluster discovers running instances, creates/loads a cluster, connects via SSH,
// sets up SGLang Docker, deploys SGLang servers, and waits for them to be healthy.
// Returns a connected cluster with SGLang running on all instances.
// Caller is responsible for calling cluster.Disconnect() when done.
func setupSGLangCluster(httpClient *http.Client, apiToken, clusterName, hfToken string) (*Cluster, error) {
	fmt.Println("=== Setting up SGLang Cluster ===")

	// 1. Discover all running instances
	fmt.Println("1. Discovering all running instances...")
	provider, _ := remote.GetCloudProvider()
	allInstances, err := provider.ListRunningInstances(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to list running instances: %w", err)
	}
	if len(allInstances) == 0 {
		return nil, fmt.Errorf("no running instances found")
	}

	fmt.Printf("Found %d running instance(s):\n", len(allInstances))
	for _, inst := range allInstances {
		fmt.Printf("  - %s (%s) @ %s [%s]\n", inst.Name, inst.ID[:16], inst.IP, inst.Region.Name)
	}

	// 2. Create or load cluster with all running instances
	cluster, err := GetCluster(httpClient, apiToken, clusterName)
	if err != nil {
		fmt.Printf("\nCreating new cluster '%s' with all %d instances...\n", clusterName, len(allInstances))
		database, dbErr := db.InitDB()
		if dbErr != nil {
			return nil, fmt.Errorf("failed to initialize database: %w", dbErr)
		}
		cluster = &Cluster{
			Name:      clusterName,
			Instances: allInstances,
			sshMgr:    remote.NewSSHClientManager(),
			database:  database,
		}

		// Persist cluster and instance associations to the database so that
		// external tools (e.g. benchmark/wildchat.py) can discover the cluster.
		dbCluster := &db.Cluster{
			Name:      clusterName,
			Status:    "running",
			CreatedAt: time.Now(),
		}
		clusterID, saveErr := database.SaveCluster(dbCluster)
		if saveErr != nil {
			return nil, fmt.Errorf("failed to save cluster to database: %w", saveErr)
		}
		cluster.ID = clusterID

		for _, inst := range allInstances {
			dbInst := &db.Instance{
				ID:        inst.ID,
				Name:      inst.Name,
				Region:    inst.Region.Name,
				Status:    inst.Status,
				IPAddress: inst.IP,
				CreatedAt: time.Now(),
			}
			if len(inst.SSHKeyNames) > 0 {
				dbInst.SSHKeyName = inst.SSHKeyNames[0]
			}
			if saveErr := database.SaveInstance(dbInst); saveErr != nil {
				fmt.Printf("Warning: failed to save instance %s to database: %v\n", inst.ID, saveErr)
			}
			if saveErr := database.AddInstanceToCluster(clusterID, inst.ID); saveErr != nil {
				fmt.Printf("Warning: failed to link instance %s to cluster: %v\n", inst.ID, saveErr)
			}
		}
		fmt.Printf("Cluster '%s' saved to database with %d instances\n", clusterName, len(allInstances))
	} else {
		fmt.Printf("\nSyncing cluster '%s' with all running instances...\n", clusterName)
		existingIDs := make(map[string]bool)
		for _, inst := range cluster.Instances {
			existingIDs[inst.ID] = true
		}
		for _, inst := range allInstances {
			if !existingIDs[inst.ID] {
				fmt.Printf("  Adding instance: %s (%s) @ %s\n", inst.Name, inst.ID[:16], inst.IP)
				if addErr := cluster.AddInstance(inst); addErr != nil {
					fmt.Printf("    Failed to add: %v\n", addErr)
				}
			}
		}
		cluster.Instances = allInstances
	}
	fmt.Printf("Cluster '%s' has %d instances\n", clusterName, len(cluster.Instances))

	// 3. Connect SSH to all instances
	fmt.Println("2. Connecting to cluster instances...")
	if err := cluster.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to cluster: %w", err)
	}
	if err := waitForHealthyCluster(cluster, len(cluster.Instances), 10*time.Minute); err != nil {
		cluster.Disconnect()
		return nil, fmt.Errorf("failed to get healthy cluster: %w", err)
	}
	fmt.Printf("Connected to %d instances in cluster\n", len(cluster.Instances))

	// 4. Setup SGLang Docker on all instances
	fmt.Println("\n3. Setting up SGLang on cluster instances...")
	if hfToken == "" {
		fmt.Println("Warning: HF_TOKEN not set, model download may fail for gated models")
	}
	setupSGLangDocker(cluster, hfToken)

	// 5. Deploy SGLang server task
	fmt.Println("\n4. Deploying SGLang servers to cluster instances...")
	sglangServerTask := remote.Task{
		Name: "sglang-server-docker",
		Command: `#!/bin/bash
# Check if container is already running AND healthy
if sudo docker ps --filter name=sglang-server --filter status=running | grep -q sglang-server; then
    if curl -s --connect-timeout 5 http://localhost:8080/health > /dev/null 2>&1; then
        echo "Container sglang-server is already running and healthy"
        exit 0
    else
        echo "Container exists but not healthy, restarting..."
        sudo docker rm -f sglang-server 2>/dev/null || true
    fi
fi

if ! sudo docker images lmsysorg/sglang | grep -q sglang; then
    echo "Pulling lmsysorg/sglang:latest image..."
    sudo docker pull lmsysorg/sglang:latest
fi

sudo docker rm -f sglang-server 2>/dev/null || true
sudo docker rm -f llama-server 2>/dev/null || true

HF_TOKEN_FILE="/home/ubuntu/.cache/huggingface/token"
HF_TOKEN_ENV=""
if [ -f "$HF_TOKEN_FILE" ]; then
    HF_TOKEN=$(cat "$HF_TOKEN_FILE")
    if [ -n "$HF_TOKEN" ]; then
        HF_TOKEN_ENV="-e HF_TOKEN=$HF_TOKEN -e HUGGING_FACE_HUB_TOKEN=$HF_TOKEN"
        echo "Using HF_TOKEN from cache file"
    fi
fi

sudo docker run --gpus all \
    -v /home/ubuntu/.cache/huggingface:/root/.cache/huggingface \
    -p 8080:8080 \
    $HF_TOKEN_ENV \
    --name sglang-server \
    --restart unless-stopped \
    -d lmsysorg/sglang:latest \
    python -m sglang.launch_server \
    --model-path mistralai/Mistral-7B-Instruct-v0.3 \
    --port 8080 \
    --host 0.0.0.0

echo 'SGLang container started successfully'
echo 'Note: Model loading can take 2-5 minutes on first run'
`,
		Background: true,
		WorkingDir: "/home/ubuntu",
		Env: map[string]string{
			"CUDA_VISIBLE_DEVICES": "0",
		},
	}

	resultChan := make(chan map[string]error, 1)
	go func() {
		resultChan <- cluster.RunTaskOnCluster(sglangServerTask)
	}()

	select {
	case results := <-resultChan:
		successCount := 0
		for instanceID, err := range results {
			if err != nil {
				fmt.Printf("Task sglang-server-docker failed on instance %s: %v\n", instanceID, err)
			} else {
				fmt.Printf("Task sglang-server-docker completed on instance %s\n", instanceID)
				successCount++
			}
		}
		fmt.Printf("SGLang deployed on %d/%d instances\n", successCount, len(cluster.Instances))
	case <-time.After(5 * time.Minute):
		fmt.Println("SGLang deployment timed out after 5 minutes")
	}

	// 6. Wait for SGLang to initialize and verify health
	fmt.Println("\n5. Waiting for SGLang to initialize...")
	time.Sleep(60 * time.Second)

	fmt.Println("Running SGLang diagnostics...")
	diagnoseSGLangOnCluster(cluster)

	taskHealth := cluster.CheckTaskHealth()
	fmt.Printf("Task Health: %d/%d running\n", taskHealth.RunningTasks, taskHealth.TotalTasks)

	if !cluster.IsTaskRunning("sglang-server-docker") {
		fmt.Println("Warning: sglang-server-docker is not running on all instances")
	}

	// 7. Verify SGLang API endpoints
	fmt.Println("\n6. Verifying SGLang API endpoints...")
	testSGLangAPIEndpoints(cluster, cluster.Instances)

	fmt.Println("=== SGLang Cluster Ready ===")
	return cluster, nil
}

// buildGotoniOnCluster builds the gotoni binary with CGO on all cluster instances.
// This only needs to be done once per cluster. The binary is built directly on the
// remote Linux instances to enable the CGO tokenizer for GORGO routing.
func buildGotoniOnCluster(cluster *Cluster) error {
	fmt.Println("=== Building Gotoni on Cluster ===")
	fmt.Println("Building DIRECTLY on Linux instances to enable CGO tokenizer")

	projectRoot := "../../"

	// Create tarball of source code
	fmt.Println("Creating source tarball...")
	tarballPath := "/tmp/gotoni-src.tar.gz"
	tarCmd := exec.Command("tar",
		"--exclude=.git",
		"--exclude=vendor",
		"--exclude=gotoni",
		"--exclude=gotoni-amd64",
		"--exclude=gotoni-arm64",
		"--exclude=*.tar.gz",
		"-czf", tarballPath,
		"-C", projectRoot,
		".",
	)
	tarCmd.Stdout = os.Stdout
	tarCmd.Stderr = os.Stderr
	if err := tarCmd.Run(); err != nil {
		return fmt.Errorf("failed to create tarball: %w", err)
	}
	fmt.Printf("Source tarball created: %s\n", tarballPath)

	buildScript := `#!/bin/bash
set -e

echo "=== Setting up gotoni build environment ==="

if ! command -v go &> /dev/null; then
    echo "Installing Go..."
    wget -q https://go.dev/dl/go1.21.5.linux-amd64.tar.gz -O /tmp/go.tar.gz
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
fi
export PATH=$PATH:/usr/local/go/bin
export GOPATH=/home/ubuntu/go
export PATH=$PATH:$GOPATH/bin

echo "Go version: $(go version)"

if ! command -v cargo &> /dev/null; then
    echo "Installing Rust..."
    curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
fi
source $HOME/.cargo/env 2>/dev/null || true

echo "Rust version: $(rustc --version 2>/dev/null || echo 'installing...')"

echo "Installing build dependencies..."
sudo apt-get update -qq
sudo apt-get install -y -qq build-essential pkg-config libssl-dev git

TOKENIZER_LIB="/usr/local/lib/libtokenizers.a"
if [ ! -f "$TOKENIZER_LIB" ]; then
    echo "Building tokenizer library..."
    rm -rf /tmp/tokenizers-build
    git clone --depth 1 https://github.com/daulet/tokenizers.git /tmp/tokenizers-build
    cd /tmp/tokenizers-build
    source $HOME/.cargo/env
    make build
    sudo cp libtokenizers.a /usr/local/lib/
    echo "Tokenizer library installed"
else
    echo "Tokenizer library already installed"
fi

echo "Extracting gotoni source..."
rm -rf /home/ubuntu/gotoni-src
mkdir -p /home/ubuntu/gotoni-src
tar -xzf /home/ubuntu/gotoni-src.tar.gz -C /home/ubuntu/gotoni-src

echo "Building gotoni with CGO (tokenizer enabled)..."
cd /home/ubuntu/gotoni-src
export CGO_ENABLED=1
export CGO_LDFLAGS="-L/usr/local/lib"

# Add tokenizer Go module dependency (not in go.mod since local builds use CGO_ENABLED=0)
go get github.com/daulet/tokenizers

go build -o /home/ubuntu/gotoni .

if [ -f /home/ubuntu/gotoni ]; then
    echo "gotoni built successfully!"
    file /home/ubuntu/gotoni
else
    echo "gotoni build failed"
    exit 1
fi
`

	// Build on all instances in parallel
	var wg sync.WaitGroup
	resultsMu := sync.Mutex{}
	results := make(map[string]bool)

	for _, inst := range cluster.Instances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()

			fmt.Printf("  Setting up gotoni on %s (%s)...\n", instance.Name, instance.IP)

			var sshKeyPath string
			if len(instance.SSHKeyNames) > 0 {
				sshKey, err := cluster.database.GetSSHKey(instance.SSHKeyNames[0])
				if err != nil {
					fmt.Printf("  Failed to get SSH key for %s: %v\n", instance.Name, err)
					resultsMu.Lock()
					results[instance.ID] = false
					resultsMu.Unlock()
					return
				}
				sshKeyPath = sshKey.PrivateKey
			} else {
				fmt.Printf("  No SSH key found for %s\n", instance.Name)
				resultsMu.Lock()
				results[instance.ID] = false
				resultsMu.Unlock()
				return
			}

			fmt.Printf("  Copying source to %s...\n", instance.Name)
			scpCmd := exec.Command("scp",
				"-i", sshKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				tarballPath,
				fmt.Sprintf("ubuntu@%s:/home/ubuntu/gotoni-src.tar.gz", instance.IP),
			)
			if err := scpCmd.Run(); err != nil {
				fmt.Printf("  Failed to copy source to %s: %v\n", instance.Name, err)
				resultsMu.Lock()
				results[instance.ID] = false
				resultsMu.Unlock()
				return
			}

			fmt.Printf("  Building on %s...\n", instance.Name)
			output, err := cluster.sshMgr.ExecuteCommandWithTimeout(
				instance.IP,
				buildScript,
				20*time.Minute,
			)

			resultsMu.Lock()
			if err != nil {
				fmt.Printf("  Build failed on %s: %v\n", instance.Name, err)
				if len(output) > 500 {
					output = output[len(output)-500:]
				}
				fmt.Printf("     Last output: %s\n", output)
				results[instance.ID] = false
			} else {
				fmt.Printf("  gotoni built on %s\n", instance.Name)
				results[instance.ID] = true
			}
			resultsMu.Unlock()
		}(inst)
	}

	wg.Wait()

	// Open port 8000 for load balancer
	fmt.Println("Opening port 8000 in firewall for load balancer...")
	provider, _ := remote.GetCloudProvider()
	httpClient := remote.NewHTTPClient()
	apiToken := remote.GetAPIToken()
	if err := provider.EnsurePortOpen(httpClient, apiToken, 8000, "tcp", "Allow Load Balancer (port 8000)"); err != nil {
		fmt.Printf("Warning: Failed to open port 8000: %v\n", err)
	} else {
		fmt.Println("Port 8000 opened in firewall")
	}

	successCount := 0
	for _, ok := range results {
		if ok {
			successCount++
		}
	}
	fmt.Printf("gotoni binary built on %d/%d instances\n", successCount, len(cluster.Instances))

	if successCount == 0 {
		return fmt.Errorf("gotoni build failed on all instances")
	}

	fmt.Println("=== Gotoni Build Complete ===")
	return nil
}

// deployLBStrategy stops any existing load balancer and starts a new one with the given strategy
// on all cluster instances. This is lightweight and can be called multiple times with different
// strategies on the same cluster.
func deployLBStrategy(cluster *Cluster, strategy string) error {
	fmt.Printf("=== Deploying LB Strategy: %s ===\n", strategy)

	// Kill existing load balancer tmux sessions on all instances
	fmt.Println("Stopping existing load balancers...")
	killCmd := "tmux kill-session -t gotoni-start_gotoni_load_balancer 2>/dev/null || true"
	cluster.ExecuteOnCluster(killCmd)

	// Brief pause to let processes clean up
	time.Sleep(2 * time.Second)

	// Start load balancer with new strategy on each instance
	lbTask := remote.Task{
		Name:       "start gotoni load balancer",
		Command:    fmt.Sprintf("/home/ubuntu/gotoni lb start --listen-port 8000 --local-port 8080 --strategy %s", strategy),
		Background: true,
		WorkingDir: "/home/ubuntu",
	}

	failCount := 0
	for _, inst := range cluster.Instances {
		err := remote.ExecuteTask(cluster.sshMgr, inst.IP, lbTask, make(map[string]bool))
		if err != nil {
			fmt.Printf("Failed to start LB on %s: %v\n", inst.IP, err)
			failCount++
		} else {
			fmt.Printf("LB (%s) started on %s\n", strategy, inst.IP)
		}
	}

	if failCount == len(cluster.Instances) {
		return fmt.Errorf("failed to start load balancer on all instances")
	}

	// Wait for LB to start accepting connections
	time.Sleep(3 * time.Second)

	fmt.Printf("=== LB Strategy %s Deployed ===\n", strategy)
	return nil
}

// verifyLBHealthy checks that the load balancer is responding on port 8000 for all cluster instances.
func verifyLBHealthy(t *testing.T, cluster *Cluster) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}

	for _, inst := range cluster.Instances {
		healthURL := fmt.Sprintf("http://%s:8000/lb/health", inst.IP)
		resp, err := client.Get(healthURL)
		if err != nil {
			t.Errorf("LB health check failed for %s: %v", inst.IP, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("LB health check returned %d for %s", resp.StatusCode, inst.IP)
		} else {
			fmt.Printf("LB healthy on %s\n", inst.IP)
		}
	}
}

// TestClusterSGLang tests SGLang server deployment and load balancer strategies on a cluster.
// The cluster is set up once and reused across strategy subtests.
func TestClusterSGLang(t *testing.T) {
	fmt.Println("=== Testing Cluster SGLang Server Deployment ===")

	if err := godotenv.Load("../../.env"); err != nil {
		t.Logf("Warning: Could not load .env file: %v", err)
	}

	httpClient := remote.NewHTTPClient()
	apiToken := os.Getenv("LAMBDA_API_KEY")
	if apiToken == "" {
		t.Skip("LAMBDA_API_KEY not set, skipping integration test")
	}

	// Setup cluster with SGLang running (one-time)
	cluster, err := setupSGLangCluster(httpClient, apiToken, "sglang-auto-cluster", os.Getenv("HF_TOKEN"))
	if err != nil {
		t.Fatalf("Failed to setup SGLang cluster: %v", err)
	}
	defer cluster.Disconnect()

	// Verify local tokenizer (for GORGO policy)
	testTokenCount := GetTokenCount("Hello, world!")
	if testTokenCount > 0 {
		fmt.Printf("Local tokenizer working: 'Hello, world!' = %d tokens\n", testTokenCount)
	} else {
		fmt.Println("Local tokenizer not available, GORGO will use character-based fallback")
	}

	// Build gotoni binary on all instances (one-time)
	if err := buildGotoniOnCluster(cluster); err != nil {
		t.Fatalf("Failed to build gotoni on cluster: %v", err)
	}

	// Test each LB strategy on the same running cluster
	for _, strategy := range []string{"gorgo", "least-loaded"} {
		t.Run("strategy-"+strategy, func(t *testing.T) {
			if err := deployLBStrategy(cluster, strategy); err != nil {
				t.Fatalf("Failed to deploy %s: %v", strategy, err)
			}
			verifyLBHealthy(t, cluster)
		})
	}

	fmt.Println("=== Cluster SGLang Test Complete ===")
}

// testSGLangAPIEndpoints tests the SGLang server endpoints using /get_server_info
func testSGLangAPIEndpoints(cluster *Cluster, instances []remote.RunningInstance) {
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
			var serverInfo SGLangServerInfo
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

// setupSGLangDocker sets up Docker-based SGLang deployment (parallel)
func setupSGLangDocker(cluster *Cluster, hfToken string) {
	fmt.Println("Checking SGLang Docker setup on all instances...")

	// Check if SGLang container is already running
	checkCmd := "sudo docker ps --filter name=sglang-server --filter status=running --format '{{.Names}}' 2>/dev/null | grep -q sglang-server && echo 'running' || echo 'not_running'"
	results := cluster.ExecuteOnCluster(checkCmd)

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
				setupSGLangOnInstance(cluster, id, hfToken)
			}(instanceID)
		}
		wg.Wait()
		fmt.Println("✅ All SGLang setups complete!")
	}
}

// setupSGLangOnInstance sets up SGLang on a specific instance
func setupSGLangOnInstance(cluster *Cluster, instanceID string, hfToken string) {
	// Find the instance IP
	var instanceIP string
	for _, inst := range cluster.Instances {
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

	output, err := cluster.sshMgr.ExecuteCommandWithTimeout(instanceIP, setupScript, 15*time.Minute)
	if err != nil {
		fmt.Printf("❌ Failed to setup SGLang on %s: %v\n", instanceID[:16], err)
		fmt.Printf("Output: %s\n", output)
		return
	}
	fmt.Printf("✅ SGLang setup complete on %s\n", instanceID[:16])
}

// diagnoseSGLangOnCluster runs diagnostic commands on all instances to identify connectivity issues
func diagnoseSGLangOnCluster(cluster *Cluster) {
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

	results := cluster.ExecuteOnCluster(diagScript)

	for instanceID, result := range results {
		// Find instance IP for display
		var instanceIP string
		for _, inst := range cluster.Instances {
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

// measureTTFT sends a chat completion request and returns the duration and prompt token count
func measureTTFT(url, content string, client *http.Client) (duration time.Duration, promptTokens int, err error) {
	payload := fmt.Sprintf(`{"model": "mistralai/Mistral-7B-Instruct-v0.3", "messages": [{"role": "user", "content": %q}], "max_tokens": 1}`, content)

	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	if err != nil {
		return 0, 0, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	duration = time.Since(start)

	if err != nil {
		return duration, 0, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse prompt_tokens from response
	var result struct {
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if jsonErr := json.Unmarshal(body, &result); jsonErr == nil {
		promptTokens = result.Usage.PromptTokens
	}

	return duration, promptTokens, nil
}

// wildchatPrompt represents a prompt from the WildChat dataset
type wildchatPrompt struct {
	Article       string `json:"article"`
	SummaryTokens int    `json:"summary_tokens"`
}

func TestMeasureGORGO_MS_PER_CHAR(t *testing.T) {
	url := "http://151.145.83.16:8080/v1/chat/completions"
	client := &http.Client{Timeout: 30 * time.Second}

	// Load prompts from WildChat dataset
	wildchatPath := "../../benchmark/wildchat_flat.jsonl"
	t.Logf("Loading prompts from %s...", wildchatPath)

	file, err := os.Open(wildchatPath)
	if err != nil {
		t.Fatalf("Failed to open wildchat file: %v", err)
	}
	defer file.Close()

	var allPrompts []wildchatPrompt
	scanner := bufio.NewScanner(file)
	// Increase scanner buffer for long lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var prompt wildchatPrompt
		if err := json.Unmarshal(scanner.Bytes(), &prompt); err != nil {
			continue // skip malformed lines
		}
		// Filter out very short or very long prompts
		if len(prompt.Article) >= 10 && len(prompt.Article) <= 10000 {
			allPrompts = append(allPrompts, prompt)
		}
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("Error reading wildchat file: %v", err)
	}

	t.Logf("Loaded %d prompts from WildChat", len(allPrompts))

	// First, measure token counts for all prompts (one-time cost)
	t.Log("Measuring token counts for prompts...")
	type promptWithTokens struct {
		content string
		tokens  int
	}
	var promptsWithTokens []promptWithTokens

	// Sample every Nth prompt to get token counts faster
	sampleStep := max(1, len(allPrompts)/50)
	for i := 0; i < len(allPrompts); i += sampleStep {
		// Quick request just to get token count
		_, tokens, err := measureTTFT(url, allPrompts[i].Article, client)
		if err != nil {
			continue
		}
		promptsWithTokens = append(promptsWithTokens, promptWithTokens{
			content: allPrompts[i].Article,
			tokens:  tokens,
		})
	}

	// Sort by actual token count
	sort.Slice(promptsWithTokens, func(i, j int) bool {
		return promptsWithTokens[i].tokens < promptsWithTokens[j].tokens
	})

	t.Logf("Got token counts for %d prompts", len(promptsWithTokens))

	// Helper to check if two strings share a prefix (min 20 chars to be meaningful)
	sharesPrefix := func(a, b string, minLen int) bool {
		maxCheck := min(len(a), len(b), 100) // check up to first 100 chars
		if maxCheck < minLen {
			return false
		}
		for i := minLen; i <= maxCheck; i++ {
			if a[:i] == b[:i] {
				return true
			}
		}
		return false
	}

	// Select 10 evenly-spaced prompts by token count, avoiding prefix overlaps
	numSamples := 10
	if len(promptsWithTokens) < numSamples {
		numSamples = len(promptsWithTokens)
	}

	var selectedPrompts []promptWithTokens
	targetIndices := make([]int, numSamples)
	for i := 0; i < numSamples; i++ {
		targetIndices[i] = i * (len(promptsWithTokens) - 1) / (numSamples - 1)
	}

	for _, targetIdx := range targetIndices {
		// Try to find a prompt near targetIdx that doesn't share prefix with selected ones
		found := false
		for offset := 0; offset < len(promptsWithTokens) && !found; offset++ {
			// Alternate checking above and below target
			for _, delta := range []int{offset, -offset} {
				idx := targetIdx + delta
				if idx < 0 || idx >= len(promptsWithTokens) {
					continue
				}
				candidate := promptsWithTokens[idx]

				// Check for prefix overlap with all selected prompts
				hasOverlap := false
				for _, selected := range selectedPrompts {
					if sharesPrefix(candidate.content, selected.content, 20) {
						hasOverlap = true
						break
					}
				}

				if !hasOverlap {
					selectedPrompts = append(selectedPrompts, candidate)
					found = true
					break
				}
			}
		}
	}

	t.Logf("Selected %d prompts with no overlapping prefixes", len(selectedPrompts))

	// Build test cases from selected prompts
	testCases := make([]struct {
		name    string
		content string
	}, len(selectedPrompts))

	for i, p := range selectedPrompts {
		testCases[i] = struct {
			name    string
			content string
		}{
			name:    fmt.Sprintf("wc_%d", i+1),
			content: p.content,
		}
		// Show first 40 chars of each prompt to verify no prefix overlap
		preview := p.content
		if len(preview) > 40 {
			preview = preview[:40] + "..."
		}
		t.Logf("  %s: %d tokens, prefix: %q", testCases[i].name, p.tokens, preview)
	}

	t.Logf("Selected %d prompts with lengths: %v chars",
		len(selectedPrompts),
		func() []int {
			lengths := make([]int, len(selectedPrompts))
			for i, tc := range testCases {
				lengths[i] = len(tc.content)
			}
			return lengths
		}())

	// Warmup request (not timed)
	t.Log("Sending warmup requests...")
	for i := 0; i < 3; i++ {
		_, _, err := measureTTFT(url, "warmup", client)
		if err != nil {
			t.Fatalf("Warmup failed: %v", err)
		}
	}
	t.Log("Warmup complete")

	t.Log("\n=== TTFT Measurements (WildChat prompts) ===")
	t.Logf("%-8s | %-6s | %-8s | %-12s", "Sample", "Tokens", "Chars", "TTFT")
	t.Log(strings.Repeat("-", 50))

	var results []struct {
		tokens   int
		duration time.Duration
		chars    int
	}

	for _, tc := range testCases {
		// Run 3 times and take the median to reduce noise
		var durations []time.Duration
		var tokens int

		for i := 0; i < 3; i++ {
			d, tok, err := measureTTFT(url, tc.content, client)
			if err != nil {
				t.Fatalf("Request failed for %s: %v", tc.name, err)
			}
			durations = append(durations, d)
			tokens = tok
			time.Sleep(50 * time.Millisecond) // small gap between requests
		}

		// Sort and take median
		sort.Slice(durations, func(i, j int) bool {
			return durations[i] < durations[j]
		})
		median := durations[len(durations)/2]

		t.Logf("%-8s | %-6d | %-8d | %v", tc.name, tokens, len(tc.content), median)
		results = append(results, struct {
			tokens   int
			duration time.Duration
			chars    int
		}{tokens, median, len(tc.content)})
	}

	// Calculate prefill rate using linear regression
	// TTFT = base_latency + (prefill_rate * tokens)
	// slope = prefill_rate (ms/token)
	// intercept = base_latency (network + overhead)
	if len(results) >= 2 {
		t.Log(strings.Repeat("-", 50))

		// Convert to float64 for regression
		n := float64(len(results))
		var sumX, sumY, sumXY, sumX2, sumY2 float64

		for _, r := range results {
			x := float64(r.tokens)
			y := float64(r.duration.Microseconds()) / 1000.0 // convert to ms
			sumX += x
			sumY += y
			sumXY += x * y
			sumX2 += x * x
			sumY2 += y * y
		}

		// Linear regression: y = intercept + slope * x
		// slope = (n*Σxy - Σx*Σy) / (n*Σx² - (Σx)²)
		// intercept = (Σy - slope*Σx) / n
		denominator := n*sumX2 - sumX*sumX
		if denominator != 0 {
			slope := (n*sumXY - sumX*sumY) / denominator
			intercept := (sumY - slope*sumX) / n

			// Calculate R² (coefficient of determination)
			// R² = 1 - (SS_res / SS_tot)
			meanY := sumY / n
			var ssRes, ssTot float64
			for _, r := range results {
				x := float64(r.tokens)
				y := float64(r.duration.Microseconds()) / 1000.0
				predicted := intercept + slope*x
				ssRes += (y - predicted) * (y - predicted)
				ssTot += (y - meanY) * (y - meanY)
			}
			r2 := 1.0 - (ssRes / ssTot)

			t.Logf("Linear regression: TTFT = %.2f ms + %.4f ms/token × tokens", intercept, slope)
			t.Logf("  Base latency (intercept): %.2f ms", intercept)
			t.Logf("  Prefill rate (slope):     %.4f ms/token", slope)
			t.Logf("  R² (goodness of fit):     %.4f", r2)

			// Also show neighbor deltas for comparison
			t.Log("")
			t.Log("Neighbor deltas:")
			var deltaSum float64
			for i := 1; i < len(results); i++ {
				tokenDelta := results[i].tokens - results[i-1].tokens
				timeDelta := results[i].duration - results[i-1].duration
				msPerToken := float64(timeDelta.Microseconds()) / float64(tokenDelta) / 1000.0
				deltaSum += msPerToken
				t.Logf("  %d→%d tokens: %+.3f ms/token", results[i-1].tokens, results[i].tokens, msPerToken)
			}
			avgDelta := deltaSum / float64(len(results)-1)
			t.Logf("  Average delta: %.4f ms/token", avgDelta)
		}
	}
}

// TestMeasureGORGO_MS_PER_TOKEN measures prefill rate using local tokenizer
// This uses GetTokenCount() which is what the load balancer will use for routing
//
// IMPORTANT: For accurate measurements, this test:
// 1. Flushes KV cache before each measurement to ensure cold-start TTFT
// 2. Uses ALL prompts from WildChat for maximum statistical power
// 3. Filters prompts to avoid prefix overlaps that could cause cache hits
func TestMeasureGORGO_MS_PER_TOKEN(t *testing.T) {
	serverURL := "http://151.145.83.16:8080"
	apiURL := serverURL + "/v1/chat/completions"
	client := &http.Client{Timeout: 60 * time.Second}

	t.Log("=== Flushing KV Cache for Accurate Measurements ===")
	if err := FlushSGLangCache(serverURL, 10*time.Second); err != nil {
		t.Logf("Warning: Could not flush KV cache: %v", err)
		t.Log("Measurements may include cache hits. For most accurate results,")
		t.Log("restart SGLang with --disable-radix-cache")
	} else {
		t.Log("✅ KV cache flushed successfully")
	}
	wildchatPath := "../../benchmark/wildchat_flat.jsonl"
	t.Logf("Loading ALL prompts from %s...", wildchatPath)

	file, err := os.Open(wildchatPath)
	if err != nil {
		t.Fatalf("Failed to open wildchat file: %v", err)
	}
	defer file.Close()

	var allPrompts []string
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var prompt wildchatPrompt
		if err := json.Unmarshal(scanner.Bytes(), &prompt); err != nil {
			continue
		}
		// Accept all valid prompts (no length filtering)
		if len(prompt.Article) >= 10 {
			allPrompts = append(allPrompts, prompt.Article)
		}
	}

	t.Logf("Loaded %d prompts from WildChat", len(allPrompts))

	// Use LOCAL tokenizer to get token counts (this is what the load balancer will use)
	t.Log("Computing token counts using local tokenizer (GetTokenCount)...")

	type promptWithTokens struct {
		content      string
		localTokens  int
		serverTokens int
	}

	var promptsWithTokens []promptWithTokens

	// Process ALL prompts (no sampling)
	for _, content := range allPrompts {
		localTokens := GetTokenCount(content)
		promptsWithTokens = append(promptsWithTokens, promptWithTokens{
			content:     content,
			localTokens: localTokens,
		})
	}

	// Sort by LOCAL token count (what the load balancer sees)
	sort.Slice(promptsWithTokens, func(i, j int) bool {
		return promptsWithTokens[i].localTokens < promptsWithTokens[j].localTokens
	})

	t.Logf("Computed local token counts for ALL %d prompts", len(promptsWithTokens))

	// Log token count distribution
	if len(promptsWithTokens) > 0 {
		minTokens := promptsWithTokens[0].localTokens
		maxTokens := promptsWithTokens[len(promptsWithTokens)-1].localTokens
		medianTokens := promptsWithTokens[len(promptsWithTokens)/2].localTokens
		t.Logf("Token distribution: min=%d, median=%d, max=%d", minTokens, medianTokens, maxTokens)
	}

	// Helper to check prefix overlap
	sharesPrefix := func(a, b string, minLen int) bool {
		maxCheck := min(len(a), len(b), 100)
		if maxCheck < minLen {
			return false
		}
		for i := minLen; i <= maxCheck; i++ {
			if a[:i] == b[:i] {
				return true
			}
		}
		return false
	}

	// Select ALL non-overlapping prompts (use all available data)
	var selected []promptWithTokens
	for _, candidate := range promptsWithTokens {
		hasOverlap := false
		for _, sel := range selected {
			if sharesPrefix(candidate.content, sel.content, 20) {
				hasOverlap = true
				break
			}
		}
		if !hasOverlap {
			selected = append(selected, candidate)
		}
	}

	t.Logf("Selected %d prompts with no overlapping prefixes (from %d total)",
		len(selected), len(promptsWithTokens))

	// Warmup
	t.Log("Sending warmup requests...")
	for i := 0; i < 3; i++ {
		measureTTFT(apiURL, "warmup", client)
	}
	t.Log("Warmup complete")

	// Flush cache AGAIN after warmup to ensure cold measurements
	t.Log("Flushing KV cache again after warmup...")
	if err := FlushSGLangCache(serverURL, 10*time.Second); err != nil {
		t.Logf("Warning: Could not flush KV cache after warmup: %v", err)
	} else {
		t.Log("✅ KV cache flushed after warmup")
	}

	t.Log("\n=== TTFT Measurements (Local Tokenizer) - ALL PROMPTS ===")
	t.Logf("%-8s | %-10s | %-10s | %-5s | %-12s", "Sample", "LocalToks", "ServerToks", "Diff", "TTFT")
	t.Log(strings.Repeat("-", 65))

	var results []struct {
		localTokens  int
		serverTokens int
		duration     time.Duration
	}

	// Process all selected prompts, flushing cache AFTER EACH request for cold-start TTFT
	for i, p := range selected {
		// Measure TTFT
		d, serverTok, err := measureTTFT(apiURL, p.content, client)
		if err != nil {
			t.Logf("Request %d failed: %v (skipping)", i+1, err)
			continue
		}

		diff := p.localTokens - serverTok
		diffStr := fmt.Sprintf("%+d", diff)

		// Log every 10th result to avoid overwhelming output
		if i < 10 || i%10 == 0 || i == len(selected)-1 {
			t.Logf("wc_%-5d | %-10d | %-10d | %-5s | %v",
				i+1, p.localTokens, serverTok, diffStr, d)
		}

		results = append(results, struct {
			localTokens  int
			serverTokens int
			duration     time.Duration
		}{p.localTokens, serverTok, d})

		// Flush cache AFTER each request to ensure next request is cold-start
		if err := FlushSGLangCache(serverURL, 5*time.Second); err != nil {
			t.Logf("Warning: Cache flush after prompt %d failed: %v", i+1, err)
		}
	}

	t.Logf("\nMeasured %d prompts successfully", len(results))
	t.Log(strings.Repeat("-", 65))

	// Calculate correlation between local and server token counts
	var sumLocal, sumServer float64
	for _, r := range results {
		sumLocal += float64(r.localTokens)
		sumServer += float64(r.serverTokens)
	}
	avgLocal := sumLocal / float64(len(results))
	avgServer := sumServer / float64(len(results))

	var covLS, varL, varS float64
	for _, r := range results {
		dL := float64(r.localTokens) - avgLocal
		dS := float64(r.serverTokens) - avgServer
		covLS += dL * dS
		varL += dL * dL
		varS += dS * dS
	}
	correlation := covLS / (math.Sqrt(varL) * math.Sqrt(varS))

	t.Logf("Token count correlation (local vs server): %.4f", correlation)
	t.Logf("Average difference: %.1f tokens", (sumLocal-sumServer)/float64(len(results)))

	// Linear regression using LOCAL token counts
	if len(results) >= 2 {
		t.Log("\n=== Linear Regression (using LOCAL token counts) ===")

		n := float64(len(results))
		var sumX, sumY, sumXY, sumX2 float64

		for _, r := range results {
			x := float64(r.localTokens) // Use LOCAL tokens
			y := float64(r.duration.Microseconds()) / 1000.0
			sumX += x
			sumY += y
			sumXY += x * y
			sumX2 += x * x
		}

		denominator := n*sumX2 - sumX*sumX
		if denominator != 0 {
			slope := (n*sumXY - sumX*sumY) / denominator
			intercept := (sumY - slope*sumX) / n

			// R²
			meanY := sumY / n
			var ssRes, ssTot float64
			for _, r := range results {
				x := float64(r.localTokens)
				y := float64(r.duration.Microseconds()) / 1000.0
				predicted := intercept + slope*x
				ssRes += (y - predicted) * (y - predicted)
				ssTot += (y - meanY) * (y - meanY)
			}
			r2 := 1.0 - (ssRes / ssTot)

			t.Log("")
			t.Logf("GORGO_MS_PER_TOKEN = %.4f ms/token", slope)
			t.Log("")
			t.Logf("Linear regression: TTFT = %.2f ms + %.4f ms/token × tokens", intercept, slope)
			t.Logf("  Base latency (intercept): %.2f ms", intercept)
			t.Logf("  Prefill rate (slope):     %.4f ms/token", slope)
			t.Logf("  R² (goodness of fit):     %.4f", r2)

			// Recommended constant for loadbalancer.go
			t.Log("")
			t.Log("=== RECOMMENDED UPDATE ===")
			t.Logf("In loadbalancer.go, update:")
			t.Logf("  const GORGO_MS_PER_TOKEN = %.4f", slope)
		}
	}
}

// setupTokenizerOnCluster installs the daulet/tokenizers library on all cluster instances
// This enables fast, accurate token counting for GORGO routing decisions
func setupTokenizerOnCluster(cluster *Cluster) error {
	fmt.Println("=== Setting up Tokenizer Library on Cluster ===")

	// Installation script for Linux (Ubuntu)
	installScript := `#!/bin/bash
set -e

echo "=== Tokenizer Library Installation ==="

# Check if already installed
if [ -f /usr/local/lib/libtokenizers.a ]; then
    echo "✅ libtokenizers.a already installed"
    exit 0
fi

echo "1. Installing Rust (if not present)..."
if ! command -v cargo &> /dev/null; then
    curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
    source $HOME/.cargo/env
fi

echo "2. Installing build dependencies..."
sudo apt-get update -qq
sudo apt-get install -y -qq build-essential pkg-config libssl-dev git

echo "3. Cloning tokenizers repository..."
rm -rf /tmp/tokenizers-build
git clone --depth 1 https://github.com/daulet/tokenizers.git /tmp/tokenizers-build

echo "4. Building tokenizers library..."
cd /tmp/tokenizers-build
source $HOME/.cargo/env
make build

echo "5. Installing library to /usr/local/lib..."
sudo cp libtokenizers.a /usr/local/lib/
sudo cp tokenizers.h /usr/local/include/ 2>/dev/null || true

echo "6. Verifying installation..."
ls -la /usr/local/lib/libtokenizers.a

echo "✅ Tokenizer library installed successfully!"
`

	// Run installation on all instances in parallel
	fmt.Printf("Installing tokenizer library on %d instances...\n", len(cluster.Instances))

	var wg sync.WaitGroup
	results := make(map[string]error)
	var resultsMu sync.Mutex

	for _, inst := range cluster.Instances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()

			fmt.Printf("  → Installing on %s (%s)...\n", instance.Name, instance.IP)

			output, err := cluster.sshMgr.ExecuteCommandWithTimeout(
				instance.IP,
				installScript,
				15*time.Minute, // Rust compilation can take a while
			)

			resultsMu.Lock()
			if err != nil {
				results[instance.ID] = fmt.Errorf("installation failed: %w\nOutput: %s", err, output)
				fmt.Printf("  ❌ Failed on %s: %v\n", instance.Name, err)
			} else {
				results[instance.ID] = nil
				fmt.Printf("  ✅ Installed on %s\n", instance.Name)
			}
			resultsMu.Unlock()
		}(inst)
	}

	wg.Wait()

	// Count successes and failures
	successCount := 0
	for _, err := range results {
		if err == nil {
			successCount++
		}
	}

	fmt.Printf("\nTokenizer installation complete: %d/%d instances successful\n",
		successCount, len(cluster.Instances))

	if successCount < len(cluster.Instances) {
		return fmt.Errorf("tokenizer installation failed on %d instances",
			len(cluster.Instances)-successCount)
	}

	return nil
}

// verifyTokenizerOnCluster verifies that the tokenizer is working on all cluster instances
func verifyTokenizerOnCluster(cluster *Cluster) error {
	fmt.Println("=== Verifying Tokenizer on Cluster ===")

	// Test script that mimics the Go tokenizer test
	testScript := `#!/bin/bash
set -e

echo "Verifying tokenizer library..."

# Check library exists
if [ ! -f /usr/local/lib/libtokenizers.a ]; then
    echo "❌ libtokenizers.a not found"
    exit 1
fi

echo "Library file exists: $(ls -la /usr/local/lib/libtokenizers.a)"

# Try to compile and run a simple Go test
cat > /tmp/tokenizer_test.go << 'GOEOF'
package main

import (
    "fmt"
    "os"
    "github.com/daulet/tokenizers"
)

func main() {
    tk, err := tokenizers.FromPretrained("mistralai/Mistral-7B-Instruct-v0.3")
    if err != nil {
        fmt.Printf("Failed to load tokenizer: %v\n", err)
        os.Exit(1)
    }
    defer tk.Close()

    // Test tokenization
    ids, _ := tk.Encode("Hello, world!", false)
    fmt.Printf("Token count for 'Hello, world!': %d\n", len(ids))

    ids2, _ := tk.Encode("The quick brown fox jumps over the lazy dog.", false)
    fmt.Printf("Token count for long text: %d\n", len(ids2))

    if len(ids) < 1 || len(ids) > 10 {
        fmt.Printf("Unexpected token count: %d\n", len(ids))
        os.Exit(1)
    }

    fmt.Println("✅ Tokenizer verification passed!")
}
GOEOF

# Check if Go is installed
if ! command -v go &> /dev/null; then
    echo "Installing Go..."
    wget -q https://go.dev/dl/go1.21.5.linux-amd64.tar.gz -O /tmp/go.tar.gz
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf /tmp/go.tar.gz
    export PATH=$PATH:/usr/local/go/bin
fi

export PATH=$PATH:/usr/local/go/bin

# Initialize Go module and run test
cd /tmp
rm -rf tokenizer_verify && mkdir tokenizer_verify && cd tokenizer_verify
go mod init tokenizer_verify
go get github.com/daulet/tokenizers

# Compile with library path
CGO_LDFLAGS="-L/usr/local/lib" go run /tmp/tokenizer_test.go
`

	var wg sync.WaitGroup
	results := make(map[string]error)
	var resultsMu sync.Mutex

	for _, inst := range cluster.Instances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()

			fmt.Printf("  → Verifying on %s (%s)...\n", instance.Name, instance.IP)

			output, err := cluster.sshMgr.ExecuteCommandWithTimeout(
				instance.IP,
				testScript,
				5*time.Minute,
			)

			resultsMu.Lock()
			if err != nil {
				results[instance.ID] = fmt.Errorf("verification failed: %w\nOutput: %s", err, output)
				fmt.Printf("  ❌ Failed on %s: %v\n", instance.Name, err)
			} else {
				results[instance.ID] = nil
				fmt.Printf("  ✅ Verified on %s\n", instance.Name)
				// Print the token counts from the output
				if strings.Contains(output, "Token count") {
					lines := strings.Split(output, "\n")
					for _, line := range lines {
						if strings.Contains(line, "Token count") {
							fmt.Printf("    %s\n", strings.TrimSpace(line))
						}
					}
				}
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

	fmt.Printf("\nTokenizer verification complete: %d/%d instances passed\n",
		successCount, len(cluster.Instances))

	if successCount < len(cluster.Instances) {
		return fmt.Errorf("tokenizer verification failed on %d instances",
			len(cluster.Instances)-successCount)
	}

	return nil
}

// TestSetupTokenizerOnCluster is an integration test that sets up and verifies
// the tokenizer library on all cluster instances
func TestSetupTokenizerOnCluster(t *testing.T) {
	fmt.Println("=== Testing Tokenizer Setup on Cluster ===")

	// Load environment variables
	if err := godotenv.Load("../../.env"); err != nil {
		t.Logf("Warning: Could not load .env file: %v", err)
	}

	httpClient := remote.NewHTTPClient()
	apiToken := os.Getenv("LAMBDA_API_KEY")

	if apiToken == "" {
		t.Skip("LAMBDA_API_KEY not set, skipping integration test")
	}

	// Get running instances
	provider, _ := remote.GetCloudProvider()
	allInstances, err := provider.ListRunningInstances(httpClient, apiToken)
	if err != nil {
		t.Fatalf("Failed to list running instances: %v", err)
	}

	if len(allInstances) == 0 {
		t.Fatal("No running instances found")
	}

	t.Logf("Found %d running instances", len(allInstances))

	// Initialize database for SSH key lookups
	database, dbErr := db.InitDB()
	if dbErr != nil {
		t.Fatalf("Failed to initialize database: %v", dbErr)
	}

	// Create cluster
	cluster := &Cluster{
		Name:      "tokenizer-test-cluster",
		Instances: allInstances,
		sshMgr:    remote.NewSSHClientManager(),
		database:  database,
	}

	// Connect to cluster
	if err := cluster.Connect(); err != nil {
		t.Fatalf("Failed to connect to cluster: %v", err)
	}
	defer cluster.Disconnect()

	// Setup tokenizer on all instances
	if err := setupTokenizerOnCluster(cluster); err != nil {
		t.Fatalf("Failed to setup tokenizer: %v", err)
	}

	// Verify tokenizer works
	if err := verifyTokenizerOnCluster(cluster); err != nil {
		t.Fatalf("Failed to verify tokenizer: %v", err)
	}

	t.Log("✅ Tokenizer setup and verification complete!")
}
