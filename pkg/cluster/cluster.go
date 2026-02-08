/*
Copyright © 2025 ALESSIO TONIOLO

cluster.go contains cluster management functionality.
A "cluster" is simply the set of all running instances from Lambda API.
SSH key paths are stored locally in the database (key name -> file path).
*/
package serve

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/atoniolo76/gotoni/pkg/config"
	"github.com/atoniolo76/gotoni/pkg/remote"
)

// Cluster represents a collection of running instances.
// The source of truth for instances is the Lambda API.
// SSH key file paths are looked up from the local database.
type Cluster struct {
	Name      string
	Instances []remote.RunningInstance
	sshMgr    *remote.SSHClientManager
	mu        sync.RWMutex
	connected bool
}

// LBDeployConfig holds configuration for load balancer deployment
type LBDeployConfig struct {
	Strategy             string
	ObservabilityEnabled bool
	LokiEndpoint         string
	ClusterName          string
	MaxConcurrent        int
	RunningThreshold     int
	Peers                []string
}

// NewCluster creates a new cluster with the given name and instances
func NewCluster(name string, instances []remote.RunningInstance) *Cluster {
	return &Cluster{
		Name:      name,
		Instances: instances,
		sshMgr:    remote.NewSSHClientManager(),
	}
}

// GetCluster fetches all running instances from Lambda API and returns them as a cluster.
// The name parameter is optional and used for display/logging purposes.
// This replaces the old database-based cluster lookup.
func GetCluster(httpClient *http.Client, apiToken string, name string) (*Cluster, error) {
	provider, _ := remote.GetCloudProvider()
	instances, err := provider.ListRunningInstances(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to list running instances from API: %w", err)
	}

	cluster := &Cluster{
		Name:      name,
		Instances: instances,
		sshMgr:    remote.NewSSHClientManager(),
	}

	return cluster, nil
}

// GetRunningInstances fetches all running instances from Lambda API.
// This is the primary way to get the current cluster state.
func GetRunningInstances(httpClient *http.Client, apiToken string) ([]remote.RunningInstance, error) {
	provider, _ := remote.GetCloudProvider()
	return provider.ListRunningInstances(httpClient, apiToken)
}

// TerminateInstances terminates the specified instances via Lambda API.
func TerminateInstances(httpClient *http.Client, apiToken string, instanceIDs []string) error {
	if len(instanceIDs) == 0 {
		return nil
	}
	_, err := remote.TerminateInstance(httpClient, apiToken, instanceIDs)
	return err
}

// TerminateAllInstances terminates all running instances.
func TerminateAllInstances(httpClient *http.Client, apiToken string) error {
	instances, err := GetRunningInstances(httpClient, apiToken)
	if err != nil {
		return fmt.Errorf("failed to get running instances: %w", err)
	}

	if len(instances) == 0 {
		return nil
	}

	var ids []string
	for _, inst := range instances {
		ids = append(ids, inst.ID)
	}

	return TerminateInstances(httpClient, apiToken, ids)
}

// Connect establishes SSH connections to all instances in the cluster.
// SSH key file paths are looked up from the local database using the key names from Lambda API.
func (c *Cluster) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected {
		return fmt.Errorf("already connected to cluster")
	}

	for _, instance := range c.Instances {
		if len(instance.SSHKeyNames) == 0 {
			log.Printf("Warning: instance %s has no SSH keys", instance.ID)
			continue
		}

		// Look up SSH key file path using the key name from Lambda API
		sshKeyPath, err := remote.GetSSHKeyFileForInstance(&instance)
		if err != nil {
			log.Printf("Failed to get SSH key for instance %s: %v", instance.ID, err)
			continue
		}

		err = c.sshMgr.ConnectToInstance(instance.IP, sshKeyPath)
		if err != nil {
			log.Printf("Failed to connect to instance %s (%s): %v", instance.ID, instance.IP, err)
			continue
		}

		log.Printf("Connected to instance %s (%s)", instance.ID, instance.IP)
	}

	c.connected = true
	return nil
}

// ExecuteOnCluster runs a command on all connected instances
func (c *Cluster) ExecuteOnCluster(command string) map[string]*remote.ClusterCommandResult {
	c.mu.RLock()
	defer c.mu.RUnlock()

	results := make(map[string]*remote.ClusterCommandResult)

	for _, instance := range c.Instances {
		result := &remote.ClusterCommandResult{
			InstanceID: instance.ID,
			InstanceIP: instance.IP,
		}

		output, err := c.sshMgr.ExecuteCommand(instance.IP, command)
		if err != nil {
			result.Error = err
			log.Printf("Command failed on %s: %v", instance.ID, err)
		} else {
			result.Output = output
		}

		results[instance.ID] = result
	}

	return results
}

// Disconnect closes all SSH connections
func (c *Cluster) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sshMgr.CloseAllConnections()
	c.connected = false

	log.Println("Disconnected from all cluster instances")
}

// Close closes the cluster (alias for Disconnect for backwards compatibility)
func (c *Cluster) Close() {
	c.Disconnect()
}

// SSHMgr returns the SSH manager for this cluster
func (c *Cluster) SSHMgr() *remote.SSHClientManager {
	return c.sshMgr
}

// ExecuteCommandWithTimeout executes a command on a specific instance with a timeout
func (c *Cluster) ExecuteCommandWithTimeout(instanceIP string, command string, timeout time.Duration) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.connected {
		return "", fmt.Errorf("cluster not connected")
	}

	return c.sshMgr.ExecuteCommandWithTimeout(instanceIP, command, timeout)
}

// AddInstance adds an instance to the in-memory cluster list.
// Note: This only affects the local Cluster object - the Lambda API is the source of truth.
func (c *Cluster) AddInstance(instance remote.RunningInstance) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.Instances = append(c.Instances, instance)
	return nil
}

// Heartbeat checks the health of all cluster instances
func (c *Cluster) Heartbeat() *ClusterHealthSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	summary := &ClusterHealthSummary{
		TotalInstances: len(c.Instances),
		Results:        make([]ClusterHeartbeatResult, 0, len(c.Instances)),
		CheckedAt:      time.Now(),
	}

	for _, instance := range c.Instances {
		result := ClusterHeartbeatResult{
			InstanceID: instance.ID,
			InstanceIP: instance.IP,
		}

		start := time.Now()
		output, err := c.sshMgr.ExecuteCommand(instance.IP, "echo 'heartbeat'")
		result.ResponseTime = time.Since(start)

		if err != nil {
			result.Healthy = false
			result.Error = err
		} else {
			if strings.TrimSpace(output) == "heartbeat" {
				result.Healthy = true
				summary.HealthyInstances++
			} else {
				result.Healthy = false
				result.Error = fmt.Errorf("unexpected response: %q", strings.TrimSpace(output))
			}
		}

		summary.Results = append(summary.Results, result)
	}

	return summary
}

type ClusterHeartbeatResult struct {
	InstanceID   string        `json:"instance_id"`
	InstanceIP   string        `json:"instance_ip"`
	Healthy      bool          `json:"healthy"`
	ResponseTime time.Duration `json:"response_time"`
	Error        error         `json:"error,omitempty"`
}

type ClusterHealthSummary struct {
	TotalInstances   int                      `json:"total_instances"`
	HealthyInstances int                      `json:"healthy_instances"`
	Results          []ClusterHeartbeatResult `json:"results"`
	CheckedAt        time.Time                `json:"checked_at"`
}

// WaitForHealthyCluster waits for the specified number of healthy instances
func WaitForHealthyCluster(cluster *Cluster, minHealthy int, timeout time.Duration) error {
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
			return fmt.Errorf("timeout: only %d/%d healthy after %v", healthyCount, heartbeat.TotalInstances, timeout)
		}

		fmt.Printf("Currently %d healthy instances. Waiting...\n", healthyCount)
		time.Sleep(pollInterval)
	}
}

// CheckTaskHealth checks the health of background tasks
func (c *Cluster) CheckTaskHealth() *TaskHealthSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	summary := &TaskHealthSummary{
		Results:   make([]TaskHealthResult, 0),
		CheckedAt: time.Now(),
	}

	for _, instance := range c.Instances {
		tasks := checkTasksOnInstance(c.sshMgr, instance.IP, instance.ID)
		summary.Results = append(summary.Results, tasks...)
	}

	for _, result := range summary.Results {
		if result.IsRunning {
			summary.RunningTasks++
		}
	}
	summary.TotalTasks = len(summary.Results)

	return summary
}

type TaskHealthResult struct {
	TaskName    string `json:"task_name"`
	InstanceID  string `json:"instance_id"`
	InstanceIP  string `json:"instance_ip"`
	IsRunning   bool   `json:"is_running"`
	SessionName string `json:"session_name,omitempty"`
	Error       error  `json:"error,omitempty"`
}

type TaskHealthSummary struct {
	RunningTasks int                `json:"running_tasks"`
	TotalTasks   int                `json:"total_tasks"`
	Results      []TaskHealthResult `json:"results"`
	CheckedAt    time.Time          `json:"checked_at"`
}

func checkTasksOnInstance(manager *remote.SSHClientManager, instanceIP, instanceID string) []TaskHealthResult {
	var results []TaskHealthResult

	cmd := "tmux list-sessions 2>/dev/null | grep '^gotoni-' | cut -d: -f1 || true"
	output, err := manager.ExecuteCommand(instanceIP, cmd)
	if err != nil {
		results = append(results, TaskHealthResult{
			TaskName:   "unknown",
			InstanceID: instanceID,
			InstanceIP: instanceIP,
			IsRunning:  false,
			Error:      fmt.Errorf("failed to check tmux sessions: %w", err),
		})
		return results
	}

	if output == "" {
		return results
	}

	sessionNames := strings.Split(strings.TrimSpace(output), "\n")
	for _, sessionName := range sessionNames {
		sessionName = strings.TrimSpace(sessionName)
		if sessionName == "" {
			continue
		}

		taskName := strings.TrimPrefix(sessionName, "gotoni-")

		checkCmd := fmt.Sprintf("tmux list-panes -t %s 2>/dev/null | wc -l", sessionName)
		paneOutput, paneErr := manager.ExecuteCommand(instanceIP, checkCmd)

		isRunning := false
		if paneErr == nil {
			if paneCount := strings.TrimSpace(paneOutput); paneCount != "0" && paneCount != "" {
				isRunning = true
			}
		}

		results = append(results, TaskHealthResult{
			TaskName:    taskName,
			InstanceID:  instanceID,
			InstanceIP:  instanceIP,
			IsRunning:   isRunning,
			SessionName: sessionName,
		})
	}

	return results
}

// IsTaskRunning checks if a specific task is running on any instance
func (c *Cluster) IsTaskRunning(taskName string) bool {
	summary := c.CheckTaskHealth()
	for _, result := range summary.Results {
		if result.IsRunning && result.TaskName == taskName {
			return true
		}
	}
	return false
}

// RunTaskOnCluster executes a task on all cluster instances
func (c *Cluster) RunTaskOnCluster(task remote.Task) map[string]error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	results := make(map[string]error)

	for _, instance := range c.Instances {
		err := remote.ExecuteTask(c.sshMgr, instance.IP, task, make(map[string]bool))
		if err != nil {
			results[instance.ID] = fmt.Errorf("failed to execute task %s: %w", task.Name, err)
			log.Printf("Task %s failed on instance %s: %v", task.Name, instance.ID, err)
		} else {
			results[instance.ID] = nil
		}
	}

	return results
}

// =====================================================
// SGLANG SETUP HELPERS
// =====================================================

// SetupSGLangCluster discovers running instances from Lambda API, connects via SSH,
// sets up SGLang Docker, deploys SGLang servers, and waits for them to be healthy.
func SetupSGLangCluster(httpClient *http.Client, apiToken, clusterName, hfToken string) (*Cluster, error) {
	fmt.Println("=== Setting up SGLang Cluster ===")

	// 1. Discover all running instances from Lambda API
	fmt.Println("1. Discovering running instances...")
	allInstances, err := GetRunningInstances(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to list running instances: %w", err)
	}
	if len(allInstances) == 0 {
		return nil, fmt.Errorf("no running instances found")
	}

	fmt.Printf("Found %d instance(s):\n", len(allInstances))
	for _, inst := range allInstances {
		fmt.Printf("  - %s (%s) @ %s\n", inst.Name, inst.ID[:16], inst.IP)
	}

	// 2. Create cluster with all running instances
	cl := NewCluster(clusterName, allInstances)

	// 3. Connect SSH
	fmt.Println("2. Connecting to cluster...")
	if err := cl.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to cluster: %w", err)
	}
	if err := WaitForHealthyCluster(cl, len(cl.Instances), 10*time.Minute); err != nil {
		cl.Disconnect()
		return nil, fmt.Errorf("failed to get healthy cluster: %w", err)
	}

	// 4. Setup SGLang Docker
	fmt.Println("3. Setting up SGLang Docker...")
	SetupSGLangDockerOnCluster(cl, hfToken)

	// 5. Deploy SGLang server with configuration from pkg/config/constants.go
	fmt.Println("4. Deploying SGLang servers...")
	sglangCmd := fmt.Sprintf(`#!/bin/bash
if sudo docker ps --filter name=%s --filter status=running | grep -q %s; then
    if curl -s --connect-timeout 5 http://localhost:%d/health > /dev/null 2>&1; then
        echo "Container already running and healthy"
        exit 0
    fi
    sudo docker rm -f %s 2>/dev/null || true
fi

HF_TOKEN_FILE="/home/ubuntu/.cache/huggingface/token"
HF_TOKEN_ENV=""
if [ -f "$HF_TOKEN_FILE" ]; then
    HF_TOKEN=$(cat "$HF_TOKEN_FILE")
    if [ -n "$HF_TOKEN" ]; then
        HF_TOKEN_ENV="-e HF_TOKEN=$HF_TOKEN -e HUGGING_FACE_HUB_TOKEN=$HF_TOKEN"
    fi
fi

sudo docker run --gpus all \
    -v /home/ubuntu/.cache/huggingface:/root/.cache/huggingface \
    -p %d:%d \
    $HF_TOKEN_ENV \
    --name %s \
    --restart unless-stopped \
    -d %s \
    python -m sglang.launch_server \
    --model-path %s \
    --port %d \
    --host 0.0.0.0 \
    --enable-metrics \
    --max-running-requests %d

`,
		config.DefaultSGLangContainerName, config.DefaultSGLangContainerName, config.DefaultSGLangPort,
		config.DefaultSGLangContainerName,
		config.DefaultSGLangPort, config.DefaultSGLangPort,
		config.DefaultSGLangContainerName,
		config.DefaultSGLangDockerImage,
		config.DefaultSGLangModel,
		config.DefaultSGLangPort,
		config.DefaultSGLangMaxRunningRequests)

	sglangServerTask := remote.Task{
		Name:       "sglang-server-docker",
		Command:    sglangCmd,
		Background: true,
		WorkingDir: "/home/ubuntu",
	}

	resultChan := make(chan map[string]error, 1)
	go func() {
		resultChan <- cl.RunTaskOnCluster(sglangServerTask)
	}()

	select {
	case results := <-resultChan:
		successCount := 0
		for instanceID, err := range results {
			if err != nil {
				fmt.Printf("Task failed on %s: %v\n", instanceID[:16], err)
			} else {
				successCount++
			}
		}
		fmt.Printf("SGLang deployed on %d/%d instances\n", successCount, len(cl.Instances))
	case <-time.After(5 * time.Minute):
		fmt.Println("SGLang deployment timed out")
	}

	// 6. Wait and verify
	fmt.Println("5. Waiting for SGLang to initialize...")
	time.Sleep(60 * time.Second)
	DiagnoseSGLangOnCluster(cl)

	fmt.Println("6. Testing API endpoints...")
	TestSGLangAPIEndpoints(cl, cl.Instances)

	fmt.Println("=== SGLang Cluster Ready ===")
	return cl, nil
}

// SetupSGLangDockerOnCluster sets up Docker-based SGLang deployment
func SetupSGLangDockerOnCluster(cl *Cluster, hfToken string) {
	checkCmd := "sudo docker ps --filter name=sglang-server --filter status=running --format '{{.Names}}' 2>/dev/null | grep -q sglang-server && echo 'running' || echo 'not_running'"
	results := cl.ExecuteOnCluster(checkCmd)

	var needsSetup []string
	for instanceID, result := range results {
		if result.Error != nil || strings.TrimSpace(result.Output) != "running" {
			needsSetup = append(needsSetup, instanceID)
		} else {
			fmt.Printf("✅ SGLang already running on %s\n", instanceID[:16])
		}
	}

	if len(needsSetup) > 0 {
		fmt.Printf("Setting up SGLang on %d instances...\n", len(needsSetup))
		var wg sync.WaitGroup
		for _, instanceID := range needsSetup {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				setupSGLangOnInstance(cl, id, hfToken)
			}(instanceID)
		}
		wg.Wait()
	}
}

func setupSGLangOnInstance(cl *Cluster, instanceID string, hfToken string) {
	var instanceIP string
	for _, inst := range cl.Instances {
		if inst.ID == instanceID {
			instanceIP = inst.IP
			break
		}
	}
	if instanceIP == "" {
		return
	}

	setupScript := fmt.Sprintf(`#!/bin/bash
set -e
mkdir -p /home/ubuntu/.cache/huggingface
if [ -n "%s" ]; then
    echo "%s" > /home/ubuntu/.cache/huggingface/token
fi
sudo docker pull lmsysorg/sglang:latest
`, hfToken, hfToken)

	_, err := cl.sshMgr.ExecuteCommandWithTimeout(instanceIP, setupScript, 15*time.Minute)
	if err != nil {
		fmt.Printf("❌ Failed on %s: %v\n", instanceID[:16], err)
		return
	}
	fmt.Printf("✅ Setup complete on %s\n", instanceID[:16])
}

// DiagnoseSGLangOnCluster runs diagnostic commands
func DiagnoseSGLangOnCluster(cl *Cluster) {
	diagScript := `#!/bin/bash
echo "Container status:"
sudo docker ps -a --filter name=sglang-server --format "table {{.Names}}\t{{.Status}}"
echo ""
echo "Health check:"
curl -s -o /dev/null -w "HTTP Status: %{http_code}\n" --connect-timeout 5 http://localhost:8080/health 2>/dev/null || echo "Failed"
`
	results := cl.ExecuteOnCluster(diagScript)

	for instanceID, result := range results {
		var instanceIP string
		for _, inst := range cl.Instances {
			if inst.ID == instanceID {
				instanceIP = inst.IP
				break
			}
		}
		fmt.Printf("\n=== %s (%s) ===\n", instanceID[:16], instanceIP)
		if result.Error != nil {
			fmt.Printf("❌ Diagnostic failed: %v\n", result.Error)
		} else {
			fmt.Println(result.Output)
		}
	}
}

// TestSGLangAPIEndpoints tests the SGLang server endpoints
func TestSGLangAPIEndpoints(cl *Cluster, instances []remote.RunningInstance) {
	testClient := &http.Client{Timeout: 10 * time.Second}

	for _, instance := range instances {
		healthURL := fmt.Sprintf("http://%s:8080/health", instance.IP)
		resp, err := testClient.Get(healthURL)
		if err != nil {
			fmt.Printf("❌ Health check failed for %s - %v\n", instance.IP, err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == 200 {
			fmt.Printf("✅ %s is healthy\n", instance.IP)
		} else {
			fmt.Printf("⚠️ %s returned status %d\n", instance.IP, resp.StatusCode)
		}

		metricsURL := fmt.Sprintf("http://%s:8080/get_server_info", instance.IP)
		resp, err = testClient.Get(metricsURL)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == 200 {
			var serverInfo struct {
				NumRunningReqs int `json:"num_running_reqs"`
				NumWaitingReqs int `json:"num_waiting_reqs"`
			}
			if json.NewDecoder(resp.Body).Decode(&serverInfo) == nil {
				fmt.Printf("   Running: %d, Waiting: %d\n", serverInfo.NumRunningReqs, serverInfo.NumWaitingReqs)
			}
		}
	}
}
