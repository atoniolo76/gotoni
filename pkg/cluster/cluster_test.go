package serve

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
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
