package serve

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atoniolo76/gotoni/pkg/remote"
)

func TestClusterLlamaCpp(t *testing.T) {
	fmt.Println("=== Testing Cluster Llama.cpp Server Deployment ===")

	httpClient := remote.NewHTTPClient()
	apiToken := os.Getenv("LAMBDA_API_KEY")

	if apiToken == "" {
		t.Skip("LAMBDA_API_KEY not set, skipping integration test")
	}

	// Define cluster specification for GPU instances across regions
	clusterSpec := &ClusterSpec{
		Name: "llama-test-cluster",
		Replicas: []ClusterReplicaSpec{
			{
				InstanceType: "gpu_8x_b200_sxm6",
				Region:       "australia-east-1",
				Quantity:     1,
				Name:         "llama-australia",
			},
			{
				InstanceType: "gpu_1x_h100_pcie",
				Region:       "us-west-3",
				Quantity:     1,
				Name:         "llama-west",
			},
			{
				InstanceType: "gpu_1x_a100_sxm4",
				Region:       "us-east-1",
				Quantity:     1,
				Name:         "llama-east",
			},
		},
	}

	fmt.Println("Launching cluster with specification...")
	cluster, err := LaunchClusterFromSpec(httpClient, apiToken, clusterSpec)
	if err != nil {
		t.Fatalf("Failed to launch cluster: %v", err)
	}

	if len(cluster.Instances) == 0 {
		t.Fatal("No instances launched in cluster")
	}
	fmt.Printf("Cluster launched with %d instances\n", len(cluster.Instances))

	// Wait for healthy instances
	if err := waitForHealthyCluster(cluster, 3, 10*time.Minute); err != nil {
		t.Fatalf("Failed to get healthy cluster: %v", err)
	}

	// 2. Connect to the cluster instances
	fmt.Println("2. Connecting to cluster instances...")

	if err := cluster.Connect(); err != nil {
		t.Fatalf("Failed to connect to cluster: %v", err)
	}
	defer cluster.Disconnect()

	fmt.Printf("Connected to %d instances in cluster\n", len(cluster.Instances))

	// 3. Define llama.cpp server tasks for different configurations
	fmt.Println("3. Defining llama.cpp server tasks...")

	// Task for a Llama 3.1 8B model with standard settings
	llama8BTask := remote.Task{
		Name:       "llama-3.1-8b-server",
		Command:    "./llama-server -m llama-3.1-8b.gguf --port 8080 --ctx-size 4096 --host 0.0.0.0",
		Background: true,
		WorkingDir: "/home/ubuntu/models",
		Env: map[string]string{
			"CUDA_VISIBLE_DEVICES": "0",
		},
	}

	// 4. Deploy tasks to different instances
	fmt.Println("4. Deploying llama.cpp servers to cluster instances...")

	// Deploy different models to different instances (if we have multiple instances)
	tasks := []remote.Task{llama8BTask}

	for i, task := range tasks {
		if i >= len(cluster.Instances) {
			fmt.Printf("Skipping task %s - not enough instances (%d available)\n", task.Name, len(cluster.Instances))
			continue
		}

		fmt.Printf("Deploying %s to instance %s...\n", task.Name, cluster.Instances[i].ID)

		// Run task asynchronously
		resultChan := make(chan map[string]error, 1)
		go func(task remote.Task, instanceIndex int) {
			results := cluster.RunTaskOnCluster(task)
			resultChan <- results
		}(task, i)

		// Wait for completion with timeout
		select {
		case results := <-resultChan:
			for instanceID, err := range results {
				if err != nil {
					fmt.Printf("❌ Task %s failed on instance %s: %v\n", task.Name, instanceID, err)
				} else {
					fmt.Printf("✅ Task %s completed successfully on instance %s\n", task.Name, instanceID)
				}
			}
		case <-time.After(5 * time.Minute):
			fmt.Printf("⏰ Task %s timed out after 5 minutes\n", task.Name)
		}
	}

	// 5. Check task health (background processes)
	fmt.Println("5. Checking task health...")
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

	// Check specific task
	if cluster.IsTaskRunning("llama-3.1-8b-server") {
		fmt.Println("🎉 llama-3.1-8b-server is running!")
	} else {
		fmt.Println("⚠️  llama-3.1-8b-server is not running")
	}

	// 6. Test cluster heartbeat
	fmt.Println("6. Test cluster heartbeat...")
	testClusterHeartbeat(t, cluster)

	// 7. Test API endpoints (if services are accessible)
	fmt.Println("7. Testing llama.cpp API endpoints...")
	testAPIEndpoints(cluster, cluster.Instances)

	fmt.Println("=== Cluster Llama.cpp Test Complete ===")
}

// testAPIEndpoints tests the llama.cpp server endpoints
func testAPIEndpoints(cluster *Cluster, instances []remote.RunningInstance) {
	testClient := &http.Client{Timeout: 10 * time.Second}

	ports := []int{8080, 8081, 8082} // Corresponding to our server ports

	for i, instance := range instances {
		if i >= len(ports) {
			break
		}

		port := ports[i]
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

// benchmarkClusterLoad demonstrates cluster load benchmarking
func benchmarkClusterLoad(cluster *Cluster, instances []remote.RunningInstance) {
	fmt.Println("=== Benchmarking Cluster Load ===")

	// First check cluster health
	if !cluster.IsHealthy() {
		fmt.Println("⚠️  Cluster is not fully healthy, benchmark may be incomplete")
	}

	// Test concurrent requests to all llama.cpp servers
	testClient := &http.Client{Timeout: 30 * time.Second}

	for _, instance := range instances {
		go func(ip string) {
			// Simple benchmark: test token generation speed
			url := fmt.Sprintf("http://%s:8080/completion", ip)
			payload := `{"prompt": "Hello, how are you?", "n_predict": 50}`

			start := time.Now()
			resp, err := testClient.Post(url, "application/json", strings.NewReader(payload))
			elapsed := time.Since(start)

			if err != nil {
				fmt.Printf("❌ Benchmark failed for %s: %v\n", ip, err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode == 200 {
				fmt.Printf("✅ %s responded in %v\n", ip, elapsed)
			} else {
				fmt.Printf("⚠️  %s returned status %d\n", ip, resp.StatusCode)
			}
		}(instance.IP)
	}

	time.Sleep(5 * time.Second) // Wait for benchmarks to complete
}
