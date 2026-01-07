package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/atoniolo76/gotoni/pkg/db"
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

// Helper to get DB instance
func getDB() (*db.DB, error) {
	return db.InitDB()
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
	RegionName       string                          `json:"region_name"`
	InstanceTypeName string                          `json:"instance_type_name"`
	SSHKeyNames      []string                        `json:"ssh_key_names"`
	FileSystemNames  []string                        `json:"file_system_names,omitempty"`
	FileSystemMounts []RequestedFilesystemMountEntry `json:"file_system_mounts,omitempty"`
	Quantity         int                             `json:"quantity,omitempty"`
	Hostname         string                          `json:"hostname,omitempty"`
	Name             string                          `json:"name,omitempty"`
	Image            interface{}                     `json:"image,omitempty"` // ImageSpecificationID or ImageSpecificationFamily
	UserData         string                          `json:"user_data,omitempty"`
	Tags             []RequestedTagEntry             `json:"tags,omitempty"`
	FirewallRulesets []FirewallRulesetEntry          `json:"firewall_rulesets,omitempty"`
}

type InstanceLaunchResponse struct {
	InstanceIDs []string `json:"instance_ids"`
}

type InstanceRestartRequest struct {
	InstanceIDs []string `json:"instance_ids"`
}

type InstanceRestartResponse struct {
	RestartedInstances []RunningInstance `json:"restarted_instances"`
}

type InstanceTerminateRequest struct {
	InstanceIDs []string `json:"instance_ids"`
}

type InstanceTerminateResponse struct {
	TerminatedInstances []RunningInstance `json:"terminated_instances"`
}

type RunningInstance struct {
	ID               string                     `json:"id"`
	Name             string                     `json:"name,omitempty"`
	IP               string                     `json:"ip"`
	PrivateIP        string                     `json:"private_ip"`
	Status           string                     `json:"status"`
	SSHKeyNames      []string                   `json:"ssh_key_names"`
	FileSystemNames  []string                   `json:"file_system_names"`
	FileSystemMounts []FilesystemMountEntry     `json:"file_system_mounts,omitempty"`
	Region           Region                     `json:"region"`
	InstanceType     InstanceType               `json:"instance_type"`
	Hostname         string                     `json:"hostname"`
	Actions          InstanceActionAvailability `json:"actions"`
	Tags             []TagEntry                 `json:"tags,omitempty"`
	FirewallRulesets []FirewallRulesetEntry     `json:"firewall_rulesets,omitempty"`
	IsBusy           bool
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

// Instance action availability types
type InstanceActionUnavailableCode string

const (
	InstanceActionUnavailableVMHasNotLaunched InstanceActionUnavailableCode = "vm-has-not-launched"
	InstanceActionUnavailableVMIsTooOld       InstanceActionUnavailableCode = "vm-is-too-old"
	InstanceActionUnavailableVMIsTerminating  InstanceActionUnavailableCode = "vm-is-terminating"
)

type InstanceActionAvailabilityDetails struct {
	Available         bool                           `json:"available"`
	ReasonCode        *InstanceActionUnavailableCode `json:"reason_code,omitempty"`
	ReasonDescription string                         `json:"reason_description,omitempty"`
}

type InstanceActionAvailability struct {
	Migrate    InstanceActionAvailabilityDetails `json:"migrate"`
	Rebuild    InstanceActionAvailabilityDetails `json:"rebuild"`
	Restart    InstanceActionAvailabilityDetails `json:"restart"`
	ColdReboot InstanceActionAvailabilityDetails `json:"cold_reboot"`
	Terminate  InstanceActionAvailabilityDetails `json:"terminate"`
}

// Tag and filesystem mount types
type TagEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type RequestedTagEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type FilesystemMountEntry struct {
	MountPoint   string `json:"mount_point"`
	FileSystemID string `json:"file_system_id"`
}

type RequestedFilesystemMountEntry struct {
	MountPoint   string `json:"mount_point"`
	FileSystemID string `json:"file_system_id"`
}

// Firewall ruleset types
type FirewallRulesetEntry struct {
	ID string `json:"id"`
}

// Image specification types
type ImageSpecificationID struct {
	ID string `json:"id"`
}

type ImageSpecificationFamily struct {
	Family string `json:"family"`
}

// Firewall rule types
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

// Config replacement using DB
// We remove LoadConfig and SaveConfig entirely and use DB calls.

// LaunchInstance creates a new SSH key (or uses provided existing key), saves it to DB, and launches an instance
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

func RunCommandOnInstance(instanceIP string) error {
	db, err := getDB()
	if err != nil {
		return fmt.Errorf("failed to init db: %w", err)
	}
	defer db.Close()

	// TODO: Implement command execution logic
	return fmt.Errorf("RunCommandOnInstance not implemented")
}

// ConnectToInstance connects to a remote instance via SSH using the key from config
func ConnectToInstance(instanceIP string) error {
	db, err := getDB()
	if err != nil {
		return fmt.Errorf("failed to init db: %w", err)
	}
	defer db.Close()

	// Find instance by IP
	instance, err := db.GetInstanceByIP(instanceIP)
	if err != nil {
		// Fallback: Try to find ANY key? Or assume key name?
		// In original logic, we grabbed "first key".
		// Let's grab "first key" if no instance found.
		keys, kerr := db.ListSSHKeys()
		if kerr != nil || len(keys) == 0 {
			return fmt.Errorf("instance not found in db and no ssh keys available")
		}
		// Use first key
		sshKeyFile := keys[0].PrivateKey
		// Check existence
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

	// Found instance, get key name
	key, err := db.GetSSHKey(instance.SSHKeyName)
	if err != nil {
		return fmt.Errorf("ssh key %s not found for instance: %w", instance.SSHKeyName, err)
	}

	sshKeyFile := key.PrivateKey
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
	db, err := getDB()
	if err != nil {
		return "", fmt.Errorf("failed to init db: %w", err)
	}
	defer db.Close()

	instance, err := db.GetInstance(instanceID)
	if err != nil {
		return "", fmt.Errorf("instance %s not found in db: %w", instanceID, err)
	}

	key, err := db.GetSSHKey(instance.SSHKeyName)
	if err != nil {
		return "", fmt.Errorf("ssh key %s not found: %w", instance.SSHKeyName, err)
	}

	if _, err := os.Stat(key.PrivateKey); os.IsNotExist(err) {
		return "", fmt.Errorf("SSH key file %s does not exist", key.PrivateKey)
	}

	return key.PrivateKey, nil
}

// RemoveInstanceFromConfig removes an instance from the config
func RemoveInstanceFromConfig(instanceID string) error {
	db, err := getDB()
	if err != nil {
		return fmt.Errorf("failed to init db: %w", err)
	}
	defer db.Close()

	return db.DeleteInstance(instanceID)
}

func ListRunningInstances(httpClient *http.Client, apiToken string) ([]RunningInstance, error) {
	provider, _ := GetCloudProvider()
	return provider.ListRunningInstances(httpClient, apiToken)
}

// ResolveInstance resolves an instance name or ID to a RunningInstance
// If the input matches an instance name, returns that instance
// If the input matches an instance ID, returns that instance
// If no match is found, returns an error
func ResolveInstance(httpClient *http.Client, apiToken string, nameOrID string) (*RunningInstance, error) {
	runningInstances, err := ListRunningInstances(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to list running instances: %w", err)
	}

	for _, instance := range runningInstances {
		if instance.Name == nameOrID || instance.ID == nameOrID {
			return &instance, nil
		}
	}

	return nil, fmt.Errorf("instance '%s' not found among running instances", nameOrID)
}

func TerminateInstance(
	httpClient *http.Client,
	apiToken string,
	instanceIDs []string,
) (*InstanceTerminateResponse, error) {
	provider, _ := GetCloudProvider()
	return provider.TerminateInstance(httpClient, apiToken, instanceIDs)
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
	sshDir, err := getSSHDir()
	if err != nil {
		return "", "", fmt.Errorf("failed to get ssh directory: %w", err)
	}
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return "", "", fmt.Errorf("failed to create ssh directory: %w", err)
	}

	// Copy the key file to ssh directory
	targetPath := filepath.Join(sshDir, keyName+".pem")

	// Read the source file
	sourceData, err := os.ReadFile(keyPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to read SSH key file: %w", err)
	}

	// Write to target location with proper permissions (read-only by owner)
	if err := os.WriteFile(targetPath, sourceData, 0400); err != nil {
		return "", "", fmt.Errorf("failed to copy SSH key file: %w", err)
	}

	// Save to DB
	database, err := getDB()
	if err != nil {
		return "", "", fmt.Errorf("failed to init db: %w", err)
	}
	defer database.Close()

	if err := database.SaveSSHKey(&db.SSHKey{Name: keyName, PrivateKey: targetPath}); err != nil {
		return "", "", fmt.Errorf("failed to save key to db: %w", err)
	}

	return keyName, targetPath, nil
}

// getSSHDir returns the directory where SSH keys should be stored
// ~/.ssh on macOS/Linux, C:\Users\<User>\.ssh on Windows
func getSSHDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}
	return filepath.Join(home, ".ssh"), nil
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

	// Save filesystem to DB
	database, err := getDB()
	if err != nil {
		return nil, fmt.Errorf("failed to init db: %w", err)
	}
	defer database.Close()

	fs := &db.Filesystem{
		Name:   name,
		ID:     apiResponse.Data.ID,
		Region: apiResponse.Data.Region.Name,
	}

	if err := database.SaveFilesystem(fs); err != nil {
		return nil, fmt.Errorf("failed to save filesystem to db: %w", err)
	}

	return &apiResponse.Data, nil
}

// GetFilesystemInfo returns filesystem info from config by name
func GetFilesystemInfo(filesystemName string) (*FilesystemInfo, error) {
	db, err := getDB()
	if err != nil {
		return nil, fmt.Errorf("failed to init db: %w", err)
	}
	defer db.Close()

	fs, err := db.GetFilesystem(filesystemName)
	if err != nil {
		return nil, fmt.Errorf("filesystem %s not found in db: %w", filesystemName, err)
	}

	return &FilesystemInfo{
		ID:     fs.ID,
		Region: fs.Region,
	}, nil
}

// SaveFilesystemInfo saves filesystem info to DB
func SaveFilesystemInfo(name, id, region string) error {
	database, err := getDB()
	if err != nil {
		return fmt.Errorf("failed to init db: %w", err)
	}
	defer database.Close()

	return database.SaveFilesystem(&db.Filesystem{
		Name:   name,
		ID:     id,
		Region: region,
	})
}

// ListTasks retrieves all tasks from DB
func ListTasks() ([]Task, error) {
	database, err := getDB()
	if err != nil {
		return nil, fmt.Errorf("failed to init db: %w", err)
	}
	defer database.Close()

	dbTasks, err := database.ListTasks()
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks from db: %w", err)
	}

	var tasks []Task
	for _, t := range dbTasks {
		var env map[string]string
		if t.Env != "" {
			if err := json.Unmarshal([]byte(t.Env), &env); err != nil {
				return nil, fmt.Errorf("failed to unmarshal env for task %s: %w", t.Name, err)
			}
		}

		var dependsOn []string
		if t.DependsOn != "" {
			if err := json.Unmarshal([]byte(t.DependsOn), &dependsOn); err != nil {
				return nil, fmt.Errorf("failed to unmarshal depends_on for task %s: %w", t.Name, err)
			}
		}

		tasks = append(tasks, Task{
			Name:       t.Name,
			Type:       t.Type,
			Command:    t.Command,
			Background: t.Background,
			WorkingDir: t.WorkingDir,
			Env:        env,
			DependsOn:  dependsOn,
			When:       t.WhenCondition,
			Restart:    t.Restart,
			RestartSec: t.RestartSec,
		})
	}

	return tasks, nil
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

// executeBackgroundCommand executes a command in the background using tmux
func executeBackgroundCommand(manager *SSHClientManager, instanceIP, cmd, workingDir string, task Task) error {
	sessionName := fmt.Sprintf("gotoni-%s", strings.ReplaceAll(task.Name, " ", "_"))

	// Build the full command with working directory
	if workingDir != "" {
		cmd = fmt.Sprintf("cd %s && %s", workingDir, cmd)
	}

	// Kill existing session if it exists
	manager.ExecuteCommand(instanceIP, fmt.Sprintf("tmux kill-session -t %s 2>/dev/null || true", sessionName))

	// Create new detached session with the command
	tmuxCmd := fmt.Sprintf("tmux new-session -d -s %s '%s'", sessionName, cmd)

	fmt.Printf("Starting task %s in background using tmux (session: %s)\n", task.Name, sessionName)

	output, err := manager.ExecuteCommand(instanceIP, tmuxCmd)
	if err != nil {
		return fmt.Errorf("failed to start background task: %w\nOutput: %s", err, output)
	}

	fmt.Printf("Task %s started successfully in background (session: %s)\n", task.Name, sessionName)
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

	fmt.Printf("Executing task: %s\n", task.Name)

	// Build the base command with environment variables
	var cmd string
	if len(task.Env) > 0 {
		envVars := []string{}
		for k, v := range task.Env {
			envVars = append(envVars, fmt.Sprintf("%s=%s", k, v))
		}
		cmd = fmt.Sprintf("%s %s", strings.Join(envVars, " "), task.Command)
	} else {
		cmd = task.Command
	}

	// Handle background execution
	if task.Background {
		workingDir := task.WorkingDir
		if workingDir == "" {
			workingDir = "/home/ubuntu"
		}

		return executeBackgroundCommand(manager, instanceIP, cmd, workingDir, task)
	}

	// Foreground execution
	if task.WorkingDir != "" {
		cmd = fmt.Sprintf("cd %s && %s", task.WorkingDir, cmd)
	}

	output, err := manager.ExecuteCommand(instanceIP, cmd)
	if err != nil {
		return fmt.Errorf("task %s failed: %w\nOutput: %s", task.Name, err, output)
	}

	fmt.Printf("Task %s completed. Output:\n%s\n", task.Name, output)
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
