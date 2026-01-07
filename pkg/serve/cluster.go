/*
Copyright © 2025 ALESSIO TONIOLO

cluster.go contains cluster management and load balancing logic
*/
package serve

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/atoniolo76/gotoni/pkg/db"
	"github.com/atoniolo76/gotoni/pkg/remote"
)

// InstanceType represents the specification of an instance type
// Borrowed from pkg/remote/client.go for local use
type InstanceType struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	GPUDescription    string `json:"gpu_description"`
	PriceCentsPerHour int    `json:"price_cents_per_hour"`
	Specs             struct {
		VCPUs      int `json:"vcpus"`
		MemoryGib  int `json:"memory_gib"`
		StorageGib int `json:"storage_gib"`
		GPUs       int `json:"gpus"`
	} `json:"specs"`
}

type Cluster struct {
	Instances []remote.RunningInstance
	sshMgr    *remote.SSHClientManager
	database  *db.DB
	mu        sync.RWMutex
	connected bool
}

// ClusterReplicaSpec defines the configuration for a single replica in the cluster
type ClusterReplicaSpec struct {
	InstanceType string `json:"instance_type"`  // Instance type name (e.g., "gpu_1x_a100_sxm4")
	Region       string `json:"region"`         // Region name (e.g., "us-east-1")
	Quantity     int    `json:"quantity"`       // Number of instances of this type to launch
	Name         string `json:"name,omitempty"` // Optional name prefix for instances
}

// ClusterSpec defines the complete cluster configuration
type ClusterSpec struct {
	Name           string               `json:"name"`                      // Cluster name
	Replicas       []ClusterReplicaSpec `json:"replicas"`                  // Replica specifications
	SSHKeyName     string               `json:"ssh_key_name,omitempty"`    // Optional SSH key to use
	FilesystemName string               `json:"filesystem_name,omitempty"` // Optional filesystem to mount
}

// LaunchClusterFromSpec creates a cluster according to the provided specification
func LaunchClusterFromSpec(httpClient *http.Client, apiToken string, spec *ClusterSpec) (*Cluster, error) {
	provider, _ := remote.GetCloudProvider()

	// Get available instance types to validate the spec
	availableInstances, err := provider.GetAvailableInstanceTypes(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get available instance types: %w", err)
	}

	// Validate the cluster spec
	if err := validateClusterSpec(spec, availableInstances); err != nil {
		return nil, fmt.Errorf("invalid cluster spec: %w", err)
	}

	// Initialize cluster with required components
	database, err := db.InitDB()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	cluster := &Cluster{
		Instances: []remote.RunningInstance{}, // Will be populated after instances are ready
		sshMgr:    remote.NewSSHClientManager(),
		database:  database,
	}
	var allLaunchedInstances []remote.LaunchedInstance

	// Launch instances according to spec
	for _, replica := range spec.Replicas {
		log.Printf("Launching %d instances of type %s in region %s", replica.Quantity, replica.InstanceType, replica.Region)

		// Use custom name if provided, otherwise use cluster name
		instanceName := replica.Name
		if instanceName == "" {
			instanceName = fmt.Sprintf("%s-%s", spec.Name, strings.ToLower(strings.ReplaceAll(replica.InstanceType, "_", "-")))
		}

		launchedInstances, err := provider.LaunchInstance(
			httpClient,
			apiToken,
			replica.InstanceType,
			replica.Region,
			replica.Quantity,
			instanceName,
			spec.SSHKeyName,
			spec.FilesystemName,
		)
		if err != nil {
			log.Printf("Failed to launch %d instances of type %s in %s: %v", replica.Quantity, replica.InstanceType, replica.Region, err)
			continue // Continue with other replicas even if one fails
		}

		allLaunchedInstances = append(allLaunchedInstances, launchedInstances...)
		log.Printf("Successfully launched %d instances in %s", len(launchedInstances), replica.Region)
	}

	// Wait for instances to be running and get their details
	if len(allLaunchedInstances) > 0 {
		log.Println("Waiting for instances to become available...")
		runningInstances, err := waitForInstances(httpClient, apiToken, allLaunchedInstances, 10*time.Minute)
		if err != nil {
			log.Printf("Warning: some instances may not be fully ready: %v", err)
		}
		cluster.Instances = runningInstances
	}

	log.Printf("Cluster %s launched with %d running instances", spec.Name, len(cluster.Instances))
	return cluster, nil
}

// validateClusterSpec checks if the cluster specification is valid
func validateClusterSpec(spec *ClusterSpec, availableInstances []remote.Instance) error {
	if spec.Name == "" {
		return fmt.Errorf("cluster name is required")
	}

	if len(spec.Replicas) == 0 {
		return fmt.Errorf("at least one replica specification is required")
	}

	// Create a map of available instance types for quick lookup
	availableTypes := make(map[string]bool)
	regionAvailability := make(map[string]map[string]bool)

	for _, instance := range availableInstances {
		instanceTypeName := instance.InstanceType.Name
		availableTypes[instanceTypeName] = true

		if regionAvailability[instanceTypeName] == nil {
			regionAvailability[instanceTypeName] = make(map[string]bool)
		}

		for _, region := range instance.RegionsWithCapacityAvailable {
			regionAvailability[instanceTypeName][region.Name] = true
		}
	}

	// Validate each replica
	for i, replica := range spec.Replicas {
		if replica.InstanceType == "" {
			return fmt.Errorf("replica %d: instance type is required", i)
		}

		if !availableTypes[replica.InstanceType] {
			return fmt.Errorf("replica %d: instance type %s is not available", i, replica.InstanceType)
		}

		if replica.Region == "" {
			return fmt.Errorf("replica %d: region is required", i)
		}

		if !regionAvailability[replica.InstanceType][replica.Region] {
			return fmt.Errorf("replica %d: instance type %s is not available in region %s", i, replica.InstanceType, replica.Region)
		}

		if replica.Quantity <= 0 {
			return fmt.Errorf("replica %d: quantity must be greater than 0", i)
		}
	}

	return nil
}

// waitForInstances polls for running instances until they are ready or timeout
func waitForInstances(httpClient *http.Client, apiToken string, launchedInstances []remote.LaunchedInstance, timeout time.Duration) ([]remote.RunningInstance, error) {
	provider, _ := remote.GetCloudProvider()
	startTime := time.Now()

	for {
		allRunning := true
		var runningInstances []remote.RunningInstance

		// Get current list of running instances
		instances, err := provider.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			return nil, fmt.Errorf("failed to list instances: %w", err)
		}

		// Check if all launched instances are running
		for _, launched := range launchedInstances {
			found := false
			for _, instance := range instances {
				if instance.ID == launched.ID {
					found = true
					runningInstances = append(runningInstances, instance)
					break
				}
			}
			if !found {
				allRunning = false
				break
			}
		}

		if allRunning {
			return runningInstances, nil
		}

		if time.Since(startTime) > timeout {
			return runningInstances, fmt.Errorf("timeout waiting for instances to be ready")
		}

		log.Printf("Waiting for %d instances to be ready...", len(launchedInstances)-len(runningInstances))
		time.Sleep(15 * time.Second)
	}
}

// LaunchCluster is deprecated. Use LaunchClusterFromSpec instead.
// This function maintains backward compatibility by launching instances in hardcoded regions.
func LaunchCluster(httpClient *http.Client, apiToken string) *Cluster {
	// Create a default cluster spec for backward compatibility
	spec := &ClusterSpec{
		Name: "gotoni-cluster",
		Replicas: []ClusterReplicaSpec{
			{InstanceType: "", Region: "us-west-1", Quantity: 1}, // Will be filled with first available
			{InstanceType: "", Region: "us-east-1", Quantity: 1},
			{InstanceType: "", Region: "us-south-1", Quantity: 1},
		},
	}

	// Get available instances to fill in instance types
	provider, _ := remote.GetCloudProvider()
	availableInstances, err := provider.GetAvailableInstanceTypes(httpClient, apiToken)
	if err != nil {
		log.Printf("Failed to get available instance types: %v", err)
		return &Cluster{}
	}

	// Fill in instance types with first available for each region
	for i := range spec.Replicas {
		replica := &spec.Replicas[i]
		for _, instance := range availableInstances {
			for _, region := range instance.RegionsWithCapacityAvailable {
				if region.Name == replica.Region {
					replica.InstanceType = instance.InstanceType.Name
					break
				}
			}
			if replica.InstanceType != "" {
				break
			}
		}
	}

	cluster, err := LaunchClusterFromSpec(httpClient, apiToken, spec)
	if err != nil {
		log.Printf("Failed to launch cluster: %v", err)
		return &Cluster{}
	}

	return cluster
}

// Connect establishes SSH connections to all instances in the cluster
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

		// Use the first SSH key (assuming there's typically one primary key)
		sshKeyName := instance.SSHKeyNames[0]

		// Get the SSH key path from database
		sshKey, err := c.database.GetSSHKey(sshKeyName)
		if err != nil {
			log.Printf("Failed to get SSH key %s for instance %s: %v", sshKeyName, instance.ID, err)
			continue
		}

		// Connect to the instance
		err = c.sshMgr.ConnectToInstance(instance.IP, sshKey.PrivateKey)
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
			log.Printf("Command succeeded on %s", instance.ID)
		}

		results[instance.ID] = result
	}

	return results
}

// ExecuteOnClusterAsync runs a command on all instances asynchronously
func (c *Cluster) ExecuteOnClusterAsync(command string) chan map[string]*remote.ClusterCommandResult {
	resultChan := make(chan map[string]*remote.ClusterCommandResult, 1)

	go func() {
		results := c.ExecuteOnCluster(command)
		resultChan <- results
	}()

	return resultChan
}

// ClusterHeartbeatResult represents the result of a cluster heartbeat check
type ClusterHeartbeatResult struct {
	InstanceID   string        `json:"instance_id"`
	InstanceIP   string        `json:"instance_ip"`
	Healthy      bool          `json:"healthy"`
	ResponseTime time.Duration `json:"response_time"`
	Error        error         `json:"error,omitempty"`
}

// ClusterHealthSummary provides a summary of cluster health
type ClusterHealthSummary struct {
	TotalInstances   int                      `json:"total_instances"`
	HealthyInstances int                      `json:"healthy_instances"`
	Results          []ClusterHeartbeatResult `json:"results"`
	CheckedAt        time.Time                `json:"checked_at"`
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

		// Simple heartbeat command
		start := time.Now()
		output, err := c.sshMgr.ExecuteCommand(instance.IP, "echo 'heartbeat'")
		result.ResponseTime = time.Since(start)

		if err != nil {
			result.Healthy = false
			result.Error = err
			log.Printf("Heartbeat failed for instance %s (%s): %v", instance.ID, instance.IP, err)
		} else {
			// Check if we got the expected response
			expected := "heartbeat"
			if strings.TrimSpace(output) == expected {
				result.Healthy = true
				summary.HealthyInstances++
			} else {
				result.Healthy = false
				result.Error = fmt.Errorf("unexpected response: got %q, expected %q", strings.TrimSpace(output), expected)
			}
		}

		summary.Results = append(summary.Results, result)
	}

	return summary
}

// HeartbeatAsync performs heartbeat checks asynchronously
func (c *Cluster) HeartbeatAsync() chan *ClusterHealthSummary {
	resultChan := make(chan *ClusterHealthSummary, 1)

	go func() {
		result := c.Heartbeat()
		resultChan <- result
	}()

	return resultChan
}

// IsHealthy returns true if all instances are healthy
func (c *Cluster) IsHealthy() bool {
	summary := c.Heartbeat()
	return summary.HealthyInstances == summary.TotalInstances
}

// GetHealthyInstances returns a list of healthy instance IPs
func (c *Cluster) GetHealthyInstances() []string {
	summary := c.Heartbeat()
	healthy := make([]string, 0, summary.HealthyInstances)

	for _, result := range summary.Results {
		if result.Healthy {
			healthy = append(healthy, result.InstanceIP)
		}
	}

	return healthy
}

// TaskHealthResult represents the health status of a background task
type TaskHealthResult struct {
	TaskName    string `json:"task_name"`
	InstanceID  string `json:"instance_id"`
	InstanceIP  string `json:"instance_ip"`
	IsRunning   bool   `json:"is_running"`
	SessionName string `json:"session_name,omitempty"`
	Error       error  `json:"error,omitempty"`
}

// TaskHealthSummary provides a summary of task health across the cluster
type TaskHealthSummary struct {
	RunningTasks int                `json:"running_tasks"`
	TotalTasks   int                `json:"total_tasks"`
	Results      []TaskHealthResult `json:"results"`
	CheckedAt    time.Time          `json:"checked_at"`
}

// CheckTaskHealth checks the health of background tasks running on cluster instances
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

	// Count running tasks
	for _, result := range summary.Results {
		if result.IsRunning {
			summary.RunningTasks++
		}
	}
	summary.TotalTasks = len(summary.Results)

	return summary
}

// checkTasksOnInstance checks for running tmux sessions on a specific instance
func checkTasksOnInstance(manager *remote.SSHClientManager, instanceIP, instanceID string) []TaskHealthResult {
	var results []TaskHealthResult

	// Check for tmux sessions with our naming pattern
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
		// No tmux sessions found
		return results
	}

	// Parse session names
	sessionNames := strings.Split(strings.TrimSpace(output), "\n")
	for _, sessionName := range sessionNames {
		sessionName = strings.TrimSpace(sessionName)
		if sessionName == "" {
			continue
		}

		// Extract task name from session name (remove gotoni- prefix)
		taskName := strings.TrimPrefix(sessionName, "gotoni-")

		// Check if session is actually active by trying to list its panes
		checkCmd := fmt.Sprintf("tmux list-panes -t %s 2>/dev/null | wc -l", sessionName)
		paneOutput, paneErr := manager.ExecuteCommand(instanceIP, checkCmd)

		isRunning := false
		if paneErr == nil {
			// If we can list panes and get a number > 0, session is active
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

// GetRunningTasks returns a list of currently running task names
func (c *Cluster) GetRunningTasks() []string {
	summary := c.CheckTaskHealth()
	running := make([]string, 0, summary.RunningTasks)

	for _, result := range summary.Results {
		if result.IsRunning {
			running = append(running, result.TaskName)
		}
	}

	return running
}

// IsTaskRunning checks if a specific task is running on any instance
func (c *Cluster) IsTaskRunning(taskName string) bool {
	runningTasks := c.GetRunningTasks()
	for _, runningTask := range runningTasks {
		if runningTask == taskName {
			return true
		}
	}
	return false
}

// ListBackgroundSessions lists all running background sessions on cluster instances

// InstanceType utilities

// GetInstanceTypeByName finds an instance type by name from available types
func GetInstanceTypeByName(httpClient *http.Client, apiToken, name string) (*InstanceType, error) {
	instances, err := remote.GetAvailableInstanceTypes(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get available instance types: %w", err)
	}

	for _, instance := range instances {
		if instance.InstanceType.Name == name {
			// Convert remote.Instance to local InstanceType
			return &InstanceType{
				Name:              instance.InstanceType.Name,
				Description:       instance.InstanceType.Description,
				GPUDescription:    instance.InstanceType.GPUDescription,
				PriceCentsPerHour: instance.InstanceType.PriceCentsPerHour,
				Specs: struct {
					VCPUs      int `json:"vcpus"`
					MemoryGib  int `json:"memory_gib"`
					StorageGib int `json:"storage_gib"`
					GPUs       int `json:"gpus"`
				}{
					VCPUs:      instance.InstanceType.Specs.VCPUs,
					MemoryGib:  instance.InstanceType.Specs.MemoryGib,
					StorageGib: instance.InstanceType.Specs.StorageGib,
					GPUs:       instance.InstanceType.Specs.GPUs,
				},
			}, nil
		}
	}

	return nil, fmt.Errorf("instance type %s not found", name)
}

// FilterInstanceTypesByGPU filters instance types that have GPUs
func FilterInstanceTypesByGPU(httpClient *http.Client, apiToken string) ([]InstanceType, error) {
	instances, err := remote.GetAvailableInstanceTypes(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get available instance types: %w", err)
	}

	var gpuInstances []InstanceType
	for _, instance := range instances {
		if instance.InstanceType.Specs.GPUs > 0 {
			gpuInstances = append(gpuInstances, InstanceType{
				Name:              instance.InstanceType.Name,
				Description:       instance.InstanceType.Description,
				GPUDescription:    instance.InstanceType.GPUDescription,
				PriceCentsPerHour: instance.InstanceType.PriceCentsPerHour,
				Specs: struct {
					VCPUs      int `json:"vcpus"`
					MemoryGib  int `json:"memory_gib"`
					StorageGib int `json:"storage_gib"`
					GPUs       int `json:"gpus"`
				}{
					VCPUs:      instance.InstanceType.Specs.VCPUs,
					MemoryGib:  instance.InstanceType.Specs.MemoryGib,
					StorageGib: instance.InstanceType.Specs.StorageGib,
					GPUs:       instance.InstanceType.Specs.GPUs,
				},
			})
		}
	}

	return gpuInstances, nil
}

// GetInstanceTypesByPriceRange filters instance types by price range (in cents per hour)
func GetInstanceTypesByPriceRange(httpClient *http.Client, apiToken string, minPrice, maxPrice int) ([]InstanceType, error) {
	instances, err := remote.GetAvailableInstanceTypes(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get available instance types: %w", err)
	}

	var filteredInstances []InstanceType
	for _, instance := range instances {
		price := instance.InstanceType.PriceCentsPerHour
		if price >= minPrice && price <= maxPrice {
			filteredInstances = append(filteredInstances, InstanceType{
				Name:              instance.InstanceType.Name,
				Description:       instance.InstanceType.Description,
				GPUDescription:    instance.InstanceType.GPUDescription,
				PriceCentsPerHour: instance.InstanceType.PriceCentsPerHour,
				Specs: struct {
					VCPUs      int `json:"vcpus"`
					MemoryGib  int `json:"memory_gib"`
					StorageGib int `json:"storage_gib"`
					GPUs       int `json:"gpus"`
				}{
					VCPUs:      instance.InstanceType.Specs.VCPUs,
					MemoryGib:  instance.InstanceType.Specs.MemoryGib,
					StorageGib: instance.InstanceType.Specs.StorageGib,
					GPUs:       instance.InstanceType.Specs.GPUs,
				},
			})
		}
	}

	return filteredInstances, nil
}

// FormatInstanceType returns a formatted string representation of an instance type
func (it *InstanceType) FormatInstanceType() string {
	gpuInfo := ""
	if it.Specs.GPUs > 0 {
		gpuInfo = fmt.Sprintf(", %d GPU(s): %s", it.Specs.GPUs, it.GPUDescription)
	}

	return fmt.Sprintf("%s: %d vCPUs, %d GB RAM, %d GB storage%s - $%.2f/hour",
		it.Name,
		it.Specs.VCPUs,
		it.Specs.MemoryGib,
		it.Specs.StorageGib,
		gpuInfo,
		float64(it.PriceCentsPerHour)/100.0,
	)
}

// IsGPUInstance returns true if the instance type has GPUs
func (it *InstanceType) IsGPUInstance() bool {
	return it.Specs.GPUs > 0
}

// GetEstimatedMonthlyCost calculates estimated monthly cost (24/7 usage)
func (it *InstanceType) GetEstimatedMonthlyCost() float64 {
	hourlyRate := float64(it.PriceCentsPerHour) / 100.0
	return hourlyRate * 24 * 30 // Rough estimate
}

// RunTaskOnCluster executes a single task on all cluster instances
func (c *Cluster) RunTaskOnCluster(task remote.Task) map[string]error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	results := make(map[string]error)

	for _, instance := range c.Instances {
		// Execute the task on this instance
		err := remote.ExecuteTask(c.sshMgr, instance.IP, task, make(map[string]bool))
		if err != nil {
			results[instance.ID] = fmt.Errorf("failed to execute task %s: %w", task.Name, err)
			log.Printf("Task %s failed on instance %s: %v", task.Name, instance.ID, err)
		} else {
			results[instance.ID] = nil
			log.Printf("Task %s completed successfully on instance %s", task.Name, instance.ID)
		}
	}

	return results
}

// RunTaskOnClusterAsync executes a single task on all cluster instances asynchronously
func (c *Cluster) RunTaskOnClusterAsync(task remote.Task) chan map[string]error {
	resultChan := make(chan map[string]error, 1)

	go func() {
		results := c.RunTaskOnCluster(task)
		resultChan <- results
	}()

	return resultChan
}

// RunTasksOnCluster executes multiple tasks with dependency resolution on all cluster instances
func (c *Cluster) RunTasksOnCluster(tasks []remote.Task) map[string]error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	results := make(map[string]error)

	for _, instance := range c.Instances {
		// Execute tasks on this instance with dependency resolution
		err := remote.ExecuteTasks(c.sshMgr, instance.IP, tasks)
		if err != nil {
			results[instance.ID] = fmt.Errorf("failed to execute tasks on instance: %w", err)
			log.Printf("Tasks failed on instance %s: %v", instance.ID, err)
		} else {
			results[instance.ID] = nil
			log.Printf("All tasks completed successfully on instance %s", instance.ID)
		}
	}

	return results
}

// RunTasksOnClusterAsync executes multiple tasks on all cluster instances asynchronously
func (c *Cluster) RunTasksOnClusterAsync(tasks []remote.Task) chan map[string]error {
	resultChan := make(chan map[string]error, 1)

	go func() {
		results := c.RunTasksOnCluster(tasks)
		resultChan <- results
	}()

	return resultChan
}

// StartServiceOnCluster creates and starts a tmux session on all cluster instances
// Deprecated: Use RunTaskOnCluster with Background=true instead
func (c *Cluster) StartServiceOnCluster(serviceName, command, workingDir string, envVars map[string]string) map[string]error {
	// Create a background task (tmux session)
	task := remote.Task{
		Name:       serviceName,
		Type:       "command",
		Command:    command,
		Background: true,
		WorkingDir: workingDir,
		Env:        envVars,
		Restart:    "always",
	}

	return c.RunTaskOnCluster(task)
}

// StopService stops a systemd service on all cluster instances (deprecated - use tasks instead)
func (c *Cluster) StopService(serviceName string) map[string]error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	results := make(map[string]error)

	for _, instance := range c.Instances {
		err := c.sshMgr.StopSystemdService(instance.IP, serviceName)
		if err != nil {
			results[instance.ID] = err
			continue
		}

		log.Printf("Stopped service %s on instance %s", serviceName, instance.ID)
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

// Helper functions for creating common task types

// NewTask creates a new task with the specified parameters
func NewTask(name, command string, background bool) remote.Task {
	task := remote.Task{
		Name:       name,
		Type:       "command",
		Command:    command,
		Background: background,
		WorkingDir: "/home/ubuntu",
		Restart:    "always",
		RestartSec: 10,
	}

	if background {
		task.Restart = "always"
	}

	return task
}

// NewBackgroundTask creates a task that runs in the background (using tmux)
func NewBackgroundTask(name, command, workingDir string, envVars map[string]string) remote.Task {
	task := NewTask(name, command, true) // background = true
	task.WorkingDir = workingDir
	task.Env = envVars
	return task
}

// NewForegroundTask creates a task that runs once and completes
func NewForegroundTask(name, command, workingDir string, envVars map[string]string) remote.Task {
	task := NewTask(name, command, false) // background = false
	task.WorkingDir = workingDir
	task.Env = envVars
	task.Restart = "no" // Don't restart foreground tasks
	return task
}
