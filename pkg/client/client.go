package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// NewHTTPClient creates a new HTTP client with secure defaults
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{
			// Use default TLS config which verifies certificates
		},
	}
}

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
	FileSystemNames  []string `json:"file_system_names,omitempty"`
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

type SSHKey struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

type FirewallRule struct {
	Protocol      string `json:"protocol"`       // "tcp", "udp", "icmp", or "all"
	PortRange     []int  `json:"port_range"`     // [min, max] - required for non-icmp protocols
	SourceNetwork string `json:"source_network"` // CIDR notation, e.g., "0.0.0.0/0"
	Description   string `json:"description,omitempty"`
}

type GlobalFirewallRuleset struct {
	ID    string         `json:"id"`   // Always "global"
	Name  string         `json:"name"` // Always "Global Firewall Rules"
	Rules []FirewallRule `json:"rules"`
}

type GlobalFirewallRulesetResponse struct {
	Data GlobalFirewallRuleset `json:"data"`
}

type GlobalFirewallRulesetPatchRequest struct {
	Rules []FirewallRule `json:"rules"`
}

type FilesystemCreateRequest struct {
	Name   string `json:"name"`
	Region string `json:"region"`
}

type Filesystem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	MountPoint string `json:"mount_point"`
	Created    string `json:"created"`
	CreatedBy  *User  `json:"created_by,omitempty"`
	IsInUse    bool   `json:"is_in_use"`
	Region     Region `json:"region"`
	BytesUsed  *int   `json:"bytes_used,omitempty"`
}

type User struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Status string `json:"status"`
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
type FilesystemInfo struct {
	ID     string `yaml:"id"`
	Region string `yaml:"region"`
}

type Config struct {
	Instances   map[string]string         `yaml:"instances,omitempty"`   // instance-id -> ssh-key-name
	SSHKeys     map[string]string         `yaml:"ssh_keys,omitempty"`    // ssh-key-name -> private-key-file
	Filesystems map[string]FilesystemInfo `yaml:"filesystems,omitempty"` // filesystem-name -> filesystem-info
	Tasks       []Task                    `yaml:"tasks,omitempty"`       // tasks/playbooks to run
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
	// systemd service options (only applies to service type tasks or background commands)
	Restart    string `yaml:"restart,omitempty"`     // Restart policy: "always", "on-failure", "on-success", "no" (default: "always")
	RestartSec int    `yaml:"restart_sec,omitempty"` // Seconds to wait before restarting (default: 10)
}

// DefaultConfig returns an empty config
func DefaultConfig() *Config {
	return &Config{
		Instances:   make(map[string]string),
		SSHKeys:     make(map[string]string),
		Filesystems: make(map[string]FilesystemInfo),
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
	if config.Filesystems == nil {
		config.Filesystems = make(map[string]FilesystemInfo)
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
	filesystemName string, // optional: if provided, mount this filesystem to the instance
) ([]LaunchedInstance, error) {
	provider, _ := GetCloudProvider()
	return provider.LaunchInstance(httpClient, apiToken, instanceType, region, quantity, name, sshKeyName, filesystemName)
}

// WaitForInstanceReady waits for an instance to become active
func WaitForInstanceReady(httpClient *http.Client, apiToken, instanceID string, timeout time.Duration) error {
	provider, _ := GetCloudProvider()
	return provider.WaitForInstanceReady(httpClient, apiToken, instanceID, timeout)
}

// GetInstance retrieves details for a specific instance
func GetInstance(httpClient *http.Client, apiToken, instanceID string) (*RunningInstance, error) {
	provider, _ := GetCloudProvider()
	return provider.GetInstance(httpClient, apiToken, instanceID)
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
	filesystemName string, // optional: if provided, mount this filesystem to the instance
) ([]LaunchedInstance, error) {
	provider, _ := GetCloudProvider()
	return provider.LaunchAndWait(httpClient, apiToken, instanceType, region, quantity, name, sshKeyName, timeout, filesystemName)
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
	provider, _ := GetCloudProvider()
	return provider.ListRunningInstances(httpClient, apiToken)
}

func TerminateInstance(
	httpClient *http.Client,
	apiToken string,
	instanceIDs []string,
) (*InstanceTerminateResponse, error) {
	provider, _ := GetCloudProvider()
	return provider.TerminateInstance(httpClient, apiToken, instanceIDs)
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
	provider, _ := GetCloudProvider()
	return provider.CreateSSHKeyForProject(httpClient, apiToken)
}

// ListSSHKeys retrieves a list of SSH keys from the cloud provider
func ListSSHKeys(httpClient *http.Client, apiToken string) ([]SSHKey, error) {
	provider, _ := GetCloudProvider()
	return provider.ListSSHKeys(httpClient, apiToken)
}

// AddExistingSSHKey adds an existing SSH key file to the gotoni configuration
// Returns the key name and target path
func AddExistingSSHKey(keyPath string, keyName string) (string, string, error) {
	// Validate the key file exists
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		return "", "", fmt.Errorf("SSH key file does not exist: %s", keyPath)
	}

	// Generate key name if not provided
	if keyName == "" {
		baseName := filepath.Base(keyPath)
		// Remove extension if present
		keyName = strings.TrimSuffix(baseName, filepath.Ext(baseName))
		// Ensure it's a valid name
		if keyName == "" {
			keyName = "imported-key"
		}
	}

	// Create ssh directory if it doesn't exist
	sshDir := "ssh"
	if err := os.MkdirAll(sshDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed to create ssh directory: %w", err)
	}

	// Copy the key file to ssh directory
	targetPath := filepath.Join(sshDir, keyName+".pem")

	// Read the source file
	sourceData, err := os.ReadFile(keyPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to read SSH key file: %w", err)
	}

	// Write to target location with proper permissions
	if err := os.WriteFile(targetPath, sourceData, 0600); err != nil {
		return "", "", fmt.Errorf("failed to copy SSH key file: %w", err)
	}

	// Load config
	config, err := LoadConfig()
	if err != nil {
		return "", "", fmt.Errorf("failed to load config: %w", err)
	}

	// Add to config
	config.SSHKeys[keyName] = targetPath

	// Save config
	if err := SaveConfig(config); err != nil {
		return "", "", fmt.Errorf("failed to save config: %w", err)
	}

	return keyName, targetPath, nil
}

// DeleteSSHKey deletes an SSH key from Lambda Cloud by ID
func DeleteSSHKey(httpClient *http.Client, apiToken string, sshKeyID string) error {
	provider, _ := GetCloudProvider()
	return provider.DeleteSSHKey(httpClient, apiToken, sshKeyID)
}

func GetAvailableInstanceTypes(httpClient *http.Client, apiToken string) ([]Instance, error) {
	provider, _ := GetCloudProvider()
	return provider.GetAvailableInstanceTypes(httpClient, apiToken)
}

// CheckInstanceTypeAvailability checks if a specific instance type is available and returns the regions with capacity
func CheckInstanceTypeAvailability(httpClient *http.Client, apiToken string, instanceTypeName string) ([]Region, error) {
	provider, _ := GetCloudProvider()
	return provider.CheckInstanceTypeAvailability(httpClient, apiToken, instanceTypeName)
}

// CreateFilesystem creates a new filesystem in the specified region and saves it to config
func CreateFilesystem(httpClient *http.Client, apiToken string, name string, region string) (*Filesystem, error) {
	requestBody := FilesystemCreateRequest{
		Name:   name,
		Region: region,
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", "https://cloud.lambda.ai/api/v1/filesystems", strings.NewReader(string(jsonBody)))
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
		Data Filesystem `json:"data"`
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Save filesystem to config
	config, err := LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	config.Filesystems[name] = FilesystemInfo{
		ID:     apiResponse.Data.ID,
		Region: apiResponse.Data.Region.Name,
	}

	if err := SaveConfig(config); err != nil {
		return nil, fmt.Errorf("failed to save config: %w", err)
	}

	return &apiResponse.Data, nil
}

// GetFilesystemInfo returns filesystem info from config by name
func GetFilesystemInfo(filesystemName string) (*FilesystemInfo, error) {
	config, err := LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	fsInfo, exists := config.Filesystems[filesystemName]
	if !exists {
		return nil, fmt.Errorf("filesystem %s not found in config", filesystemName)
	}

	return &fsInfo, nil
}

// ListFilesystems retrieves a list of filesystems from Lambda Cloud
func ListFilesystems(httpClient *http.Client, apiToken string) ([]Filesystem, error) {
	req, err := http.NewRequest("GET", "https://cloud.lambda.ai/api/v1/file-systems", nil)
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
		Data []Filesystem `json:"data"`
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return apiResponse.Data, nil
}

// DeleteFilesystem deletes a filesystem from Lambda Cloud by ID
func DeleteFilesystem(httpClient *http.Client, apiToken string, filesystemID string) error {
	if filesystemID == "" {
		return fmt.Errorf("filesystem ID is required")
	}

	url := fmt.Sprintf("https://cloud.lambda.ai/api/v1/filesystems/%s", filesystemID)

	// Use a longer timeout for filesystem deletion (can take time for large filesystems)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
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
			// Run in background using systemd
			serviceName := fmt.Sprintf("gotoni-%s", strings.ReplaceAll(task.Name, " ", "_"))
			// Stop existing service if it exists
			manager.StopSystemdService(instanceIP, serviceName)
			// Create systemd service file
			workingDir := task.WorkingDir
			if workingDir == "" {
				workingDir = "/home/ubuntu"
			}
			if err := manager.CreateSystemdService(instanceIP, serviceName, cmd, workingDir, task.Env, &task); err != nil {
				return fmt.Errorf("task %s failed to create systemd service: %w", task.Name, err)
			}
			// Start the service
			if err := manager.StartSystemdService(instanceIP, serviceName); err != nil {
				return fmt.Errorf("task %s failed to start systemd service: %w", task.Name, err)
			}
			fmt.Printf("Task %s started in background (systemd service: %s)\n", task.Name, serviceName)
			return nil
		}

		output, err := manager.ExecuteCommand(instanceIP, cmd)
		if err != nil {
			return fmt.Errorf("task %s failed: %w\nOutput: %s", task.Name, err, output)
		}
		fmt.Printf("Task %s completed. Output:\n%s\n", task.Name, output)

	case "service":
		// Service is like command but always runs in background using systemd
		if task.Command == "" {
			return fmt.Errorf("task %s: service requires command", task.Name)
		}
		cmd = task.Command
		// Note: we don't prepend env vars here as they'll be set in the systemd service file
		// Environment variables are handled via CreateSystemdService

		// Use systemd to run service in background
		serviceName := fmt.Sprintf("gotoni-%s", strings.ReplaceAll(task.Name, " ", "_"))
		// Stop existing service if it exists
		manager.StopSystemdService(instanceIP, serviceName)
		// Create systemd service file
		workingDir := task.WorkingDir
		if workingDir == "" {
			workingDir = "/home/ubuntu"
		}
		if err := manager.CreateSystemdService(instanceIP, serviceName, cmd, workingDir, task.Env, &task); err != nil {
			return fmt.Errorf("task %s failed to create systemd service: %w", task.Name, err)
		}
		// Start the service
		if err := manager.StartSystemdService(instanceIP, serviceName); err != nil {
			return fmt.Errorf("task %s failed to start systemd service: %w", task.Name, err)
		}
		fmt.Printf("Task %s started as systemd service '%s'\n", task.Name, serviceName)

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

// GetGlobalFirewallRules retrieves the current global firewall ruleset
func GetGlobalFirewallRules(httpClient *http.Client, apiToken string) (*GlobalFirewallRuleset, error) {
	url := "https://cloud.lambda.ai/api/v1/firewall-rulesets/global"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))
	req.Header.Set("Accept", "application/json")

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

	var apiResponse GlobalFirewallRulesetResponse
	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &apiResponse.Data, nil
}

// UpdateGlobalFirewallRules updates the global firewall ruleset with new rules
func UpdateGlobalFirewallRules(httpClient *http.Client, apiToken string, rules []FirewallRule) (*GlobalFirewallRuleset, error) {
	url := "https://cloud.lambda.ai/api/v1/firewall-rulesets/global"

	patchRequest := GlobalFirewallRulesetPatchRequest{
		Rules: rules,
	}

	jsonData, err := json.Marshal(patchRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("PATCH", url, strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

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

	var apiResponse GlobalFirewallRulesetResponse
	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &apiResponse.Data, nil
}

// EnsurePortOpen ensures that the specified port is open in the global firewall rules
// It checks existing rules and adds the port if it doesn't exist
func EnsurePortOpen(httpClient *http.Client, apiToken string, port int, protocol string, description string) error {
	// Get current rules
	currentRuleset, err := GetGlobalFirewallRules(httpClient, apiToken)
	if err != nil {
		return fmt.Errorf("failed to get current firewall rules: %w", err)
	}

	// Check if port is already open
	portAlreadyOpen := false
	for _, rule := range currentRuleset.Rules {
		if rule.Protocol == protocol && len(rule.PortRange) == 2 {
			if rule.PortRange[0] <= port && port <= rule.PortRange[1] {
				portAlreadyOpen = true
				break
			}
		}
	}

	if portAlreadyOpen {
		// Port is already open, no need to update
		return nil
	}

	// Add new rule for the port
	newRule := FirewallRule{
		Protocol:      protocol,
		PortRange:     []int{port, port},
		SourceNetwork: "0.0.0.0/0",
		Description:   description,
	}

	// Merge with existing rules
	updatedRules := append(currentRuleset.Rules, newRule)

	// Update firewall rules
	_, err = UpdateGlobalFirewallRules(httpClient, apiToken, updatedRules)
	if err != nil {
		return fmt.Errorf("failed to update firewall rules: %w", err)
	}

	return nil
}

// CreateSystemdService creates a systemd user service file
func (scm *SSHClientManager) CreateSystemdService(instanceIP string, serviceName string, command string, workingDir string, envVars map[string]string, serviceOpts *Task) error {
	// Ensure systemd user directory exists
	serviceDirCmd := "mkdir -p ~/.config/systemd/user"
	if _, err := scm.ExecuteCommand(instanceIP, serviceDirCmd); err != nil {
		return fmt.Errorf("failed to create systemd user directory: %w", err)
	}

	// Build environment variables
	envVarsStr := ""
	for k, v := range envVars {
		// Escape quotes in environment variable values
		escapedV := strings.ReplaceAll(v, `"`, `\"`)
		envVarsStr += fmt.Sprintf("Environment=\"%s=%s\"\n", k, escapedV)
	}

	// Add PATH if not already set
	if _, hasPath := envVars["PATH"]; !hasPath {
		envVarsStr += "Environment=\"PATH=/home/ubuntu/.local/bin:/usr/local/bin:/usr/bin:/bin\"\n"
	}

	// Escape the command for the service file
	// Since ExecStart needs the full command, we'll use a shell wrapper
	execStart := fmt.Sprintf("/bin/bash -c %s", fmt.Sprintf("%q", command))

	// Set defaults for service options
	restart := "always"
	restartSec := 10

	if serviceOpts != nil {
		if serviceOpts.Restart != "" {
			restart = serviceOpts.Restart
		}
		if serviceOpts.RestartSec > 0 {
			restartSec = serviceOpts.RestartSec
		}
	}

	// Build service section
	serviceSection := fmt.Sprintf(`[Service]
Type=simple
WorkingDirectory=%s
%sExecStart=%s
Restart=%s
RestartSec=%d
StandardOutput=journal
StandardError=journal
`, workingDir, envVarsStr, execStart, restart, restartSec)

	// Create service file content
	serviceContent := fmt.Sprintf(`[Unit]
Description=%s
After=network.target

%s
[Install]
WantedBy=default.target
`, serviceName, serviceSection)

	// Write service file using base64 encoding for reliable transfer
	serviceFile := fmt.Sprintf("~/.config/systemd/user/%s.service", serviceName)

	// Base64 encode the content to avoid shell escaping issues
	encodedContent := base64.StdEncoding.EncodeToString([]byte(serviceContent))

	// Decode and write using base64 -d
	writeCmd := fmt.Sprintf("echo %s | base64 -d > %s", encodedContent, serviceFile)
	if _, err := scm.ExecuteCommand(instanceIP, writeCmd); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}

	// Reload systemd daemon
	reloadCmd := "systemctl --user daemon-reload"
	if _, err := scm.ExecuteCommand(instanceIP, reloadCmd); err != nil {
		return fmt.Errorf("failed to reload systemd daemon: %w", err)
	}

	return nil
}

// StartSystemdService starts a systemd user service
func (scm *SSHClientManager) StartSystemdService(instanceIP string, serviceName string) error {
	// Ensure lingering is enabled for user services to persist after logout
	enableCmd := "loginctl enable-linger $USER 2>/dev/null || true"
	scm.ExecuteCommand(instanceIP, enableCmd)

	// Start the service
	startCmd := fmt.Sprintf("systemctl --user start %s.service", serviceName)
	output, err := scm.ExecuteCommand(instanceIP, startCmd)
	if err != nil {
		return fmt.Errorf("failed to start service: %w\nOutput: %s", err, output)
	}
	return nil
}

// StopSystemdService stops a systemd user service
func (scm *SSHClientManager) StopSystemdService(instanceIP string, serviceName string) error {
	stopCmd := fmt.Sprintf("systemctl --user stop %s.service 2>/dev/null || true", serviceName)
	_, err := scm.ExecuteCommand(instanceIP, stopCmd)
	return err
}

// ListSystemdServices lists all gotoni-managed systemd services
func (scm *SSHClientManager) ListSystemdServices(instanceIP string) ([]string, error) {
	// List all user services and filter for gotoni-managed ones
	// We'll use a naming convention: gotoni-<service-name>
	cmd := "systemctl --user list-units --type=service --no-pager --no-legend 2>/dev/null | grep -E 'gotoni-' | awk '{print $1}' | sed 's/\\.service$//' | sed 's/^gotoni-//' || echo ''"
	output, err := scm.ExecuteCommand(instanceIP, cmd)
	if err != nil {
		return nil, err
	}

	services := []string{}
	if output != "" {
		lines := strings.Split(strings.TrimSpace(output), "\n")
		for _, line := range lines {
			if line != "" {
				services = append(services, strings.TrimSpace(line))
			}
		}
	}
	return services, nil
}

// GetSystemdServiceStatus gets the status of a systemd service
func (scm *SSHClientManager) GetSystemdServiceStatus(instanceIP string, serviceName string) (string, error) {
	cmd := fmt.Sprintf("systemctl --user is-active gotoni-%s.service 2>&1 || echo 'inactive'", serviceName)
	status, err := scm.ExecuteCommand(instanceIP, cmd)
	if err != nil {
		return "unknown", err
	}
	return strings.TrimSpace(status), nil
}

// GetSystemdLogs gets logs from a systemd service using journalctl
func (scm *SSHClientManager) GetSystemdLogs(instanceIP string, serviceName string, lines int) (string, error) {
	if lines <= 0 {
		lines = 100 // Default to last 100 lines
	}
	cmd := fmt.Sprintf("journalctl --user -u gotoni-%s.service -n %d --no-pager 2>&1 || echo 'Service not found or no logs'", serviceName, lines)
	return scm.ExecuteCommand(instanceIP, cmd)
}

// Legacy tmux functions (kept for backward compatibility, but deprecated)
// ListTmuxSessions lists all tmux sessions on a remote instance
func (scm *SSHClientManager) ListTmuxSessions(instanceIP string) ([]string, error) {
	// Now delegates to systemd
	return scm.ListSystemdServices(instanceIP)
}

// GetTmuxLogs gets the output/logs from a tmux session
func (scm *SSHClientManager) GetTmuxLogs(instanceIP string, sessionName string, lines int) (string, error) {
	// Now delegates to systemd
	return scm.GetSystemdLogs(instanceIP, sessionName, lines)
}

// AttachToTmuxSession attaches to a tmux session (returns command to run)
func AttachToTmuxSessionCommand(instanceIP string, sessionName string, sshKeyFile string) string {
	return fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ubuntu@%s -t 'tmux attach -t %s'", sshKeyFile, instanceIP, sessionName)
}
