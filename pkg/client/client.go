package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Response struct {
	Data map[string]Instance `json:"data"`
}

type Instance struct {
	InstanceType                 InstanceType `json:"instance_type"`
	RegionsWithCapacityAvailable []Region     `json:"regions_with_capacity_available"`
}

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

type Region struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type InstanceLaunchRequest struct {
	RegionName       string   `json:"region_name"`
	InstanceTypeName string   `json:"instance_type_name"`
	SSHKeyNames      []string `json:"ssh_key_names"`
	Quantity         int      `json:"quantity,omitempty"`
	Name             string   `json:"name,omitempty"`
}

type InstanceLaunchResponse struct {
	InstanceIDs []string `json:"instance_ids"`
}

type InstanceTerminateRequest struct {
	InstanceIDs []string `json:"instance_ids"`
}

type InstanceTerminateResponse struct {
	TerminatedInstances []TerminatedInstance `json:"terminated_instances"`
}

type TerminatedInstance struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status"`
	// Add other fields as needed
}

type RunningInstance struct {
	ID              string       `json:"id"`
	Name            string       `json:"name,omitempty"`
	IP              string       `json:"ip"`
	PrivateIP       string       `json:"private_ip"`
	Status          string       `json:"status"`
	SSHKeyNames     []string     `json:"ssh_key_names"`
	FileSystemNames []string     `json:"file_system_names"`
	Region          Region       `json:"region"`
	InstanceType    InstanceType `json:"instance_type"`
	Hostname        string       `json:"hostname"`
	JupyterToken    string       `json:"jupyter_token"`
	JupyterURL      string       `json:"jupyter_url"`
	IsReserved      bool         `json:"is_reserved"`
}

type LaunchedInstance struct {
	ID         string
	SSHKeyName string
	SSHKeyFile string
}

type GeneratedSSHKey struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

var minCudaVersions = map[string]float64{
	"H100 (80 GB SXM5)":    12.0,
	"H100 (80 GB PCIe)":    12.0,
	"H100 (80 GB HBM3)":    12.0,
	"H100 NVL":             12.0,
	"A100 (40 GB PCIe)":    11.0,
	"A100 (80 GB PCIe)":    11.0,
	"A100 (40 GB SXM4)":    11.0,
	"A100 (80 GB SXM4)":    11.0,
	"A6000 (48 GB)":        11.0,
	"A5000 (24 GB)":        11.0,
	"A4000 (16 GB)":        11.0,
	"RTX 6000 Ada (48 GB)": 11.8,
	"RTX 5000 Ada (32 GB)": 11.8,
	"RTX 4000 Ada (20 GB)": 11.8,
	"RTX 6000 (24 GB)":     10.0,
	"A10 (24 GB PCIe)":     11.0,
	"L40 (48 GB)":          11.8,
	"L40S (48 GB)":         11.8,
	"B200 (180 GB SXM6)":   12.0,
	"GH200 (96 GB)":        12.0,
	"Tesla V100 (16 GB)":   9.0,
	"Tesla V100 (32 GB)":   9.0,
}

var MatchingInstanceTypes = map[string]string{
	"gpu_1x_gh200":        "GH200 (96 GB)",
	"gpu_1x_h100_sxm5":    "H100 (80 GB SXM5)",
	"gpu_1x_h100_pcie":    "H100 (80 GB PCIe)",
	"gpu_1x_a10_pcie":     "A10 (24 GB PCIe)",
	"gpu_1x_a100_sxm4":    "A100 (40 GB SXM4)",
	"gpu_8x_b200_sxm6":    "B200 (108 GB SXM6)",
	"gpu_8x_h100_sxm5":    "H100 (80 GB SXM5)",
	"gpu_4x_h100_sxm5":    "H100 (80 GB SXM5)",
	"gpu_2x_h100_sxm5":    "H100 (80 GB SXM5)",
	"gpu_8x_a100_sxm4":    "A100 (80 GB SXM4)",
	"gpu_1x_a100_pcie":    "A100 (40 GB PCIe)",
	"gpu_2x_a100_pcie":    "A100 (40 GB PCIe)",
	"gpu_4x_a100_pcie":    "A100 (40 GB PCIe)",
	"gpu_8x_a100_sxm4_v2": "A100 (80 GB SXM4) v2",
}

// Config represents the application configuration
type Config struct {
	APIKey    string            `yaml:"api_key,omitempty"`   // Lambda Cloud API key
	Instances map[string]string `yaml:"instances,omitempty"` // instance-id -> ssh-key-name
	SSHKeys   map[string]string `yaml:"ssh_keys,omitempty"`  // ssh-key-name -> private-key-file
	Tasks     []Task            `yaml:"tasks,omitempty"`     // tasks/playbooks to run
}

// Task represents a task or playbook step
type Task struct {
	Name       string            `yaml:"name"`                  // Task name/description
	Type       string            `yaml:"type"`                  // "command" or "service"
	Command    string            `yaml:"command,omitempty"`     // Shell command to run
	Background bool              `yaml:"background,omitempty"`  // Run in background (for services)
	WorkingDir string            `yaml:"working_dir,omitempty"` // Working directory for command
	Env        map[string]string `yaml:"env,omitempty"`         // Environment variables
	DependsOn  []string          `yaml:"depends_on,omitempty"`  // Task dependencies (by name)
	When       string            `yaml:"when,omitempty"`        // Condition to run (e.g., "instance_type == 'gpu_1x_a100_sxm4'")
}

// DefaultConfig returns an empty config
func DefaultConfig() *Config {
	return &Config{
		Instances: make(map[string]string),
		SSHKeys:   make(map[string]string),
	}
}

// LoadConfig loads the configuration from file
func LoadConfig() (*Config, error) {
	configPath := ".gotoni/config.yaml"

	// Check if file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return DefaultConfig(), nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Initialize maps if they're nil
	if config.Instances == nil {
		config.Instances = make(map[string]string)
	}
	if config.SSHKeys == nil {
		config.SSHKeys = make(map[string]string)
	}

	return &config, nil
}

// SaveConfig saves the configuration to file
func SaveConfig(config *Config) error {
	configPath := ".gotoni/config.yaml"

	// Create directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// LaunchInstance creates a new SSH key (or uses provided existing key), saves it to config, and launches an instance
func LaunchInstance(
	httpClient *http.Client,
	apiToken string,
	instanceType string,
	region string,
	quantity int,
	name string,
	sshKeyName string, // optional: if provided, use this existing SSH key name instead of generating new one
) ([]LaunchedInstance, error) {
	if quantity <= 0 {
		quantity = 1 // Default to 1
	}
	if name == "" {
		name = "default"
	}

	var finalSSHKeyName, sshKeyFile string
	var err error

	if sshKeyName != "" {
		// Use the provided existing SSH key name
		finalSSHKeyName = sshKeyName
		sshKeyFile = filepath.Join("ssh", sshKeyName+".pem")
		// Check if the private key file exists
		if _, err := os.Stat(sshKeyFile); os.IsNotExist(err) {
			return nil, fmt.Errorf("private key file %s does not exist for SSH key %s", sshKeyFile, sshKeyName)
		}
	} else {
		// Create a new SSH key for this instance
		finalSSHKeyName, sshKeyFile, err = CreateSSHKeyForProject(httpClient, apiToken)
		if err != nil {
			return nil, fmt.Errorf("failed to create SSH key: %w", err)
		}
	}

	// Load current config
	config, err := LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Save SSH key info to config
	config.SSHKeys[finalSSHKeyName] = sshKeyFile

	// Save config
	if err := SaveConfig(config); err != nil {
		return nil, fmt.Errorf("failed to save config: %w", err)
	}

	requestBody := InstanceLaunchRequest{
		RegionName:       region,
		InstanceTypeName: instanceType,
		SSHKeyNames:      []string{finalSSHKeyName},
		Quantity:         quantity,
		Name:             name,
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", "https://cloud.lambda.ai/api/v1/instance-operations/launch", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Body = io.NopCloser(strings.NewReader(string(jsonBody)))
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var apiResponse struct {
		Data InstanceLaunchResponse `json:"data"`
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Create LaunchedInstance structs with SSH key info
	instances := make([]LaunchedInstance, len(apiResponse.Data.InstanceIDs))
	for i, instanceID := range apiResponse.Data.InstanceIDs {
		instances[i] = LaunchedInstance{
			ID:         instanceID,
			SSHKeyName: finalSSHKeyName,
			SSHKeyFile: sshKeyFile,
		}

		// Save instance -> SSH key mapping
		config.Instances[instanceID] = finalSSHKeyName
	}

	// Save updated config with instance mappings
	if err := SaveConfig(config); err != nil {
		return nil, fmt.Errorf("failed to save config with instance mappings: %w", err)
	}

	return instances, nil
}

// WaitForInstanceReady waits for an instance to become active
func WaitForInstanceReady(httpClient *http.Client, apiToken, instanceID string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 10 * time.Minute // Default timeout
	}

	ticker := time.NewTicker(10 * time.Second) // Check every 10 seconds
	defer ticker.Stop()

	timeoutChan := time.After(timeout)

	for {
		select {
		case <-timeoutChan:
			return fmt.Errorf("timeout waiting for instance %s to become ready", instanceID)

		case <-ticker.C:
			instance, err := GetInstance(httpClient, apiToken, instanceID)
			if err != nil {
				return fmt.Errorf("failed to get instance status: %w", err)
			}

			switch instance.Status {
			case "active":
				return nil // Instance is ready!
			case "terminated", "terminating":
				return fmt.Errorf("instance %s terminated during startup", instanceID)
			case "unhealthy":
				return fmt.Errorf("instance %s became unhealthy", instanceID)
			case "preempted":
				return fmt.Errorf("instance %s was preempted", instanceID)
			case "booting":
				// Still booting, continue waiting
				continue
			default:
				return fmt.Errorf("unknown instance status: %s", instance.Status)
			}
		}
	}
}

// GetInstance retrieves details for a specific instance
func GetInstance(httpClient *http.Client, apiToken, instanceID string) (*RunningInstance, error) {
	url := fmt.Sprintf("https://cloud.lambda.ai/api/v1/instances/%s", instanceID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var apiResponse struct {
		Data RunningInstance `json:"data"`
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &apiResponse.Data, nil
}

// LaunchAndWait launches instances and waits for them to be ready
func LaunchAndWait(
	httpClient *http.Client,
	apiToken string,
	instanceType string,
	region string,
	quantity int,
	name string,
	sshKeyName string,
	timeout time.Duration,
) ([]LaunchedInstance, error) {
	// Launch the instances
	instances, err := LaunchInstance(httpClient, apiToken, instanceType, region, quantity, name, sshKeyName)
	if err != nil {
		return nil, fmt.Errorf("failed to launch instances: %w", err)
	}

	// Wait for each instance to become ready
	for _, instance := range instances {
		fmt.Printf("Waiting for instance %s to become ready...\n", instance.ID)
		if err := WaitForInstanceReady(httpClient, apiToken, instance.ID, timeout); err != nil {
			return nil, fmt.Errorf("instance %s failed to become ready: %w", instance.ID, err)
		}
		fmt.Printf("Instance %s is now ready!\n", instance.ID)
	}

	return instances, nil
}

// ConnectToInstance connects to a remote instance via SSH using the key from config
func ConnectToInstance(instanceIP string) error {
	// Load config
	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// For now, we'll use the most recent SSH key if we don't have instance-specific mapping
	// In the future, we could enhance this to accept instance ID instead of IP
	var sshKeyName string
	if len(config.SSHKeys) > 0 {
		// Get the most recently added SSH key (simple heuristic)
		for name := range config.SSHKeys {
			sshKeyName = name
			break // Just get first one for now
		}
	} else {
		return fmt.Errorf("no SSH keys found in config")
	}

	sshKeyFile, exists := config.SSHKeys[sshKeyName]
	if !exists {
		return fmt.Errorf("SSH key file not found for key: %s", sshKeyName)
	}

	if _, err := os.Stat(sshKeyFile); os.IsNotExist(err) {
		return fmt.Errorf("SSH key file %s does not exist", sshKeyFile)
	}

	// Use ssh command to connect
	cmd := exec.Command("ssh", "-i", sshKeyFile, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", fmt.Sprintf("ubuntu@%s", instanceIP))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// GetSSHKeyForInstance returns the SSH key file for a given instance ID
func GetSSHKeyForInstance(instanceID string) (string, error) {
	config, err := LoadConfig()
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}

	sshKeyName, exists := config.Instances[instanceID]
	if !exists {
		return "", fmt.Errorf("no SSH key mapping found for instance: %s", instanceID)
	}

	sshKeyFile, exists := config.SSHKeys[sshKeyName]
	if !exists {
		return "", fmt.Errorf("SSH key file not found for key: %s", sshKeyName)
	}

	return sshKeyFile, nil
}

// RemoveInstanceFromConfig removes an instance from the config
func RemoveInstanceFromConfig(instanceID string) error {
	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	delete(config.Instances, instanceID)

	return SaveConfig(config)
}

func ListRunningInstances(httpClient *http.Client, apiToken string) ([]RunningInstance, error) {
	req, err := http.NewRequest("GET", "https://cloud.lambda.ai/api/v1/instances", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var response struct {
		Data []RunningInstance `json:"data"`
	}
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return response.Data, nil
}

func TerminateInstance(
	httpClient *http.Client,
	apiToken string,
	instanceIDs []string,
) (*InstanceTerminateResponse, error) {
	if len(instanceIDs) == 0 {
		return nil, fmt.Errorf("at least one instance ID is required")
	}

	requestBody := InstanceTerminateRequest{
		InstanceIDs: instanceIDs,
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", "https://cloud.lambda.ai/api/v1/instance-operations/terminate", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Body = io.NopCloser(strings.NewReader(string(jsonBody)))
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var apiResponse struct {
		Data InstanceTerminateResponse `json:"data"`
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &apiResponse.Data, nil
}

// createSSHKey generates a new SSH key pair on Lambda and returns both keys
func createSSHKey(httpClient *http.Client, apiToken string, name string) (*GeneratedSSHKey, error) {
	requestBody := map[string]string{"name": name}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", "https://cloud.lambda.ai/api/v1/ssh-keys", strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var apiResponse struct {
		Data GeneratedSSHKey `json:"data"`
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &apiResponse.Data, nil
}

// CreateSSHKeyForProject creates a new SSH key and saves it in the ssh directory
func CreateSSHKeyForProject(httpClient *http.Client, apiToken string) (string, string, error) {
	// Create unique key name with timestamp
	timestamp := time.Now().Unix()
	keyName := "lambda-key-" + strconv.FormatInt(timestamp, 10)

	// Create the SSH key
	generatedKey, err := createSSHKey(httpClient, apiToken, keyName)
	if err != nil {
		return "", "", fmt.Errorf("failed to create SSH key: %w", err)
	}

	// Create ssh directory if it doesn't exist
	sshDir := "ssh"
	if err := os.MkdirAll(sshDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed to create ssh directory: %w", err)
	}

	// Save the private key in ssh directory
	privateKeyFile := filepath.Join(sshDir, keyName+".pem")
	err = os.WriteFile(privateKeyFile, []byte(generatedKey.PrivateKey), 0600)
	if err != nil {
		return "", "", fmt.Errorf("failed to save private key: %w", err)
	}

	fmt.Printf("Created new SSH key '%s' and saved private key to %s\n", keyName, privateKeyFile)
	fmt.Printf("Use: ssh -i %s ubuntu@<instance-ip>\n", privateKeyFile)
	return generatedKey.Name, privateKeyFile, nil
}

func GetAvailableInstanceTypes(httpClient *http.Client, apiToken string) ([]Instance, error) {
	req, err := http.NewRequest("GET", "https://cloud.lambda.ai/api/v1/instance-types", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var response Response
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	var instances []Instance
	for _, instance := range response.Data {
		if len(instance.RegionsWithCapacityAvailable) == 0 {
			continue
		}
		instances = append(instances, instance)
	}

	return instances, nil
}

// ExecuteTask executes a single task on a remote instance
func ExecuteTask(manager *SSHClientManager, instanceIP string, task Task, completedTasks map[string]bool) error {
	// Check dependencies
	for _, dep := range task.DependsOn {
		if !completedTasks[dep] {
			return fmt.Errorf("task %s depends on %s which hasn't been completed", task.Name, dep)
		}
	}

	fmt.Printf("Executing task: %s (type: %s)\n", task.Name, task.Type)

	var cmd string

	switch task.Type {
	case "command":
		// Build command with environment variables and working directory
		if len(task.Env) > 0 {
			envVars := []string{}
			for k, v := range task.Env {
				envVars = append(envVars, fmt.Sprintf("%s=%s", k, v))
			}
			cmd = fmt.Sprintf("%s %s", strings.Join(envVars, " "), task.Command)
		} else {
			cmd = task.Command
		}

		if task.WorkingDir != "" {
			cmd = fmt.Sprintf("cd %s && %s", task.WorkingDir, cmd)
		}

		if task.Background {
			// Run in background using tmux
			tmuxSessionName := strings.ReplaceAll(task.Name, " ", "_")
			// Check if tmux session already exists, kill it if so
			checkCmd := fmt.Sprintf("tmux has-session -t %s 2>/dev/null && tmux kill-session -t %s || true", tmuxSessionName, tmuxSessionName)
			manager.ExecuteCommand(instanceIP, checkCmd)
			// Start new tmux session with the command
			cmd = fmt.Sprintf("tmux new-session -d -s %s bash -c %s", tmuxSessionName, fmt.Sprintf("%q", cmd))
			output, err := manager.ExecuteCommand(instanceIP, cmd)
			if err != nil {
				return fmt.Errorf("task %s failed to start in background: %w\nOutput: %s", task.Name, err, output)
			}
			fmt.Printf("Task %s started in background (tmux session: %s)\n", task.Name, tmuxSessionName)
			return nil
		}

		output, err := manager.ExecuteCommand(instanceIP, cmd)
		if err != nil {
			return fmt.Errorf("task %s failed: %w\nOutput: %s", task.Name, err, output)
		}
		fmt.Printf("Task %s completed. Output:\n%s\n", task.Name, output)

	case "service":
		// Service is like command but always runs in background using tmux
		if task.Command == "" {
			return fmt.Errorf("task %s: service requires command", task.Name)
		}
		cmd = task.Command
		if len(task.Env) > 0 {
			envVars := []string{}
			for k, v := range task.Env {
				envVars = append(envVars, fmt.Sprintf("%s=%s", k, v))
			}
			cmd = fmt.Sprintf("%s %s", strings.Join(envVars, " "), cmd)
		}
		if task.WorkingDir != "" {
			cmd = fmt.Sprintf("cd %s && %s", task.WorkingDir, cmd)
		}
		// Use tmux to run service in background
		tmuxSessionName := strings.ReplaceAll(task.Name, " ", "_")
		// Check if tmux session already exists, kill it if so
		checkCmd := fmt.Sprintf("tmux has-session -t %s 2>/dev/null && tmux kill-session -t %s || true", tmuxSessionName, tmuxSessionName)
		manager.ExecuteCommand(instanceIP, checkCmd)
		// Start new tmux session with the service
		// Use printf with %q to properly escape the command
		tmuxCmd := fmt.Sprintf("tmux new-session -d -s %s bash -c %s", tmuxSessionName, fmt.Sprintf("%q", cmd))
		output, err := manager.ExecuteCommand(instanceIP, tmuxCmd)
		if err != nil {
			return fmt.Errorf("task %s failed to start service: %w\nOutput: %s", task.Name, err, output)
		}
		fmt.Printf("Task %s started as service in tmux session '%s'\n", task.Name, tmuxSessionName)

	default:
		return fmt.Errorf("unknown task type: %s (must be 'command' or 'service')", task.Type)
	}

	return nil
}

// ExecuteTasks executes all tasks in order, respecting dependencies
func ExecuteTasks(manager *SSHClientManager, instanceIP string, tasks []Task) error {
	completedTasks := make(map[string]bool)
	taskMap := make(map[string]Task)

	// Build task map
	for _, task := range tasks {
		taskMap[task.Name] = task
	}

	// Execute tasks respecting dependencies
	remaining := make(map[string]bool)
	for _, task := range tasks {
		remaining[task.Name] = true
	}

	for len(remaining) > 0 {
		progressMade := false
		for name := range remaining {
			task := taskMap[name]
			canRun := true
			for _, dep := range task.DependsOn {
				if !completedTasks[dep] {
					canRun = false
					break
				}
			}
			if canRun {
				err := ExecuteTask(manager, instanceIP, task, completedTasks)
				if err != nil {
					return err
				}
				completedTasks[task.Name] = true
				delete(remaining, task.Name)
				progressMade = true
			}
		}
		if !progressMade {
			return fmt.Errorf("circular dependency or missing dependency detected in tasks")
		}
	}

	return nil
}

// FilterTasksByType filters tasks by their type
func FilterTasksByType(tasks []Task, taskType string) []Task {
	var filtered []Task
	for _, task := range tasks {
		if task.Type == taskType {
			filtered = append(filtered, task)
		}
	}
	return filtered
}

// GetAPIToken returns the API token from config, environment, or empty string
func GetAPIToken() string {
	config, err := LoadConfig()
	if err == nil && config.APIKey != "" {
		return config.APIKey
	}
	return os.Getenv("LAMBDA_API_KEY")
}

// ListTmuxSessions lists all tmux sessions on a remote instance
func (scm *SSHClientManager) ListTmuxSessions(instanceIP string) ([]string, error) {
	output, err := scm.ExecuteCommand(instanceIP, "tmux ls -F '#{session_name}' 2>/dev/null || echo ''")
	if err != nil {
		return nil, err
	}

	sessions := []string{}
	if output != "" {
		lines := strings.Split(strings.TrimSpace(output), "\n")
		for _, line := range lines {
			if line != "" {
				sessions = append(sessions, strings.TrimSpace(line))
			}
		}
	}
	return sessions, nil
}

// GetTmuxLogs gets the output/logs from a tmux session
func (scm *SSHClientManager) GetTmuxLogs(instanceIP string, sessionName string, lines int) (string, error) {
	if lines <= 0 {
		lines = 100 // Default to last 100 lines
	}
	cmd := fmt.Sprintf("tmux capture-pane -t %s -p -S -%d 2>/dev/null || echo 'Session not found or no output'", sessionName, lines)
	return scm.ExecuteCommand(instanceIP, cmd)
}

// AttachToTmuxSession attaches to a tmux session (returns command to run)
func AttachToTmuxSessionCommand(instanceIP string, sessionName string, sshKeyFile string) string {
	return fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ubuntu@%s -t 'tmux attach -t %s'", sshKeyFile, instanceIP, sessionName)
}
