package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/atoniolo76/gotoni/pkg/db"
)

// LambdaProvider implements the CloudProvider interface for Lambda Cloud
type LambdaProvider struct{}

// NewLambdaProvider creates a new Lambda cloud provider
func NewLambdaProvider() *LambdaProvider {
	return &LambdaProvider{}
}

// LaunchInstance creates a new SSH key (or uses provided existing key), saves it to config, and launches an instance
func (p *LambdaProvider) LaunchInstance(httpClient *http.Client, apiToken string, instanceType string, region string, quantity int, name string, sshKeyName string, filesystemName string) ([]LaunchedInstance, error) {
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
		sshDir, err := getSSHDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get ssh directory: %w", err)
		}
		sshKeyFile = filepath.Join(sshDir, sshKeyName+".pem")
		// Check if the private key file exists
		if _, err := os.Stat(sshKeyFile); os.IsNotExist(err) {
			return nil, fmt.Errorf("private key file %s does not exist for SSH key %s", sshKeyFile, sshKeyName)
		}
	} else {
		// Create a new SSH key for this instance
		finalSSHKeyName, sshKeyFile, err = p.CreateSSHKeyForProject(httpClient, apiToken)
		if err != nil {
			return nil, fmt.Errorf("failed to create SSH key: %w", err)
		}
	}

	// Init DB
	database, err := db.InitDB()
	if err != nil {
		return nil, fmt.Errorf("failed to init db: %w", err)
	}
	defer database.Close()

	// Save SSH key info to DB
	// Note: CreateSSHKeyForProject already saves it if it's a new key.
	// But here we might be reusing an existing key name.
	// If we are reusing, we should ensure it's in the DB?
	// Logic: If sshKeyName provided, we verified file exists.
	// We should make sure DB has record of it.
	if sshKeyName != "" {
		if err := database.SaveSSHKey(&db.SSHKey{Name: sshKeyName, PrivateKey: sshKeyFile}); err != nil {
			return nil, fmt.Errorf("failed to save ssh key to db: %w", err)
		}
	}

	// Validate filesystem region if filesystem is provided
	var fileSystemNames []string
	if filesystemName != "" {
		fsInfo, err := GetFilesystemInfo(filesystemName)
		if err != nil {
			return nil, fmt.Errorf("failed to get filesystem info: %w", err)
		}

		// Validate that filesystem region matches instance region
		if fsInfo.Region != region {
			return nil, fmt.Errorf("filesystem %s is in region %s, but instance is being launched in region %s (regions must match)", filesystemName, fsInfo.Region, region)
		}

		fileSystemNames = []string{filesystemName}
	}

	requestBody := InstanceLaunchRequest{
		RegionName:       region,
		InstanceTypeName: instanceType,
		SSHKeyNames:      []string{finalSSHKeyName},
		FileSystemNames:  fileSystemNames,
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

		// Save instance info to DB
		// We only have ID and key info right now. Detailed info comes later or via GetInstance.
		// But we need to store the mapping instanceID -> sshKeyName
		inst := &db.Instance{
			ID:         instanceID,
			SSHKeyName: finalSSHKeyName,
			// Other fields empty for now, will be updated later or on status check
		}
		if err := database.SaveInstance(inst); err != nil {
			return nil, fmt.Errorf("failed to save instance to db: %w", err)
		}
	}

	return instances, nil
}

// LaunchAndWait launches instances and waits for them to be ready
func (p *LambdaProvider) LaunchAndWait(httpClient *http.Client, apiToken string, instanceType string, region string, quantity int, name string, sshKeyName string, timeout time.Duration, filesystemName string) ([]LaunchedInstance, error) {
	// Launch the instances
	instances, err := p.LaunchInstance(httpClient, apiToken, instanceType, region, quantity, name, sshKeyName, filesystemName)
	if err != nil {
		return nil, fmt.Errorf("failed to launch instances: %w", err)
	}

	// Wait for each instance to become ready
	for _, instance := range instances {
		fmt.Printf("Waiting for instance %s to become ready...\n", instance.ID)
		if err := p.WaitForInstanceReady(httpClient, apiToken, instance.ID, timeout); err != nil {
			return nil, fmt.Errorf("instance %s failed to become ready: %w", instance.ID, err)
		}
		fmt.Printf("Instance %s is now ready!\n", instance.ID)
	}

	return instances, nil
}

// WaitForInstanceReady waits for an instance to become active
func (p *LambdaProvider) WaitForInstanceReady(httpClient *http.Client, apiToken string, instanceID string, timeout time.Duration) error {
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
			instance, err := p.GetInstance(httpClient, apiToken, instanceID)
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
func (p *LambdaProvider) GetInstance(httpClient *http.Client, apiToken string, instanceID string) (*RunningInstance, error) {
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

// ListRunningInstances retrieves a list of running instances from Lambda Cloud
func (p *LambdaProvider) ListRunningInstances(httpClient *http.Client, apiToken string) ([]RunningInstance, error) {
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

// TerminateInstance terminates instances
func (p *LambdaProvider) TerminateInstance(httpClient *http.Client, apiToken string, instanceIDs []string) (*InstanceTerminateResponse, error) {
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
func (p *LambdaProvider) createSSHKey(httpClient *http.Client, apiToken string, name string) (*GeneratedSSHKey, error) {
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
func (p *LambdaProvider) CreateSSHKeyForProject(httpClient *http.Client, apiToken string) (string, string, error) {
	// Create unique key name with timestamp
	timestamp := time.Now().Unix()
	keyName := "lambda-key-" + strconv.FormatInt(timestamp, 10)

	// Create the SSH key
	generatedKey, err := p.createSSHKey(httpClient, apiToken, keyName)
	if err != nil {
		return "", "", fmt.Errorf("failed to create SSH key: %w", err)
	}

	// Create ssh directory if it doesn't exist
	sshDir, err := getSSHDir()
	if err != nil {
		return "", "", fmt.Errorf("failed to get ssh directory: %w", err)
	}
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return "", "", fmt.Errorf("failed to create ssh directory: %w", err)
	}

	// Save the private key in ssh directory
	privateKeyFile := filepath.Join(sshDir, keyName+".pem")
	err = os.WriteFile(privateKeyFile, []byte(generatedKey.PrivateKey), 0400)
	if err != nil {
		return "", "", fmt.Errorf("failed to save private key: %w", err)
	}

	fmt.Printf("Created new SSH key '%s' and saved private key to %s\n", keyName, privateKeyFile)
	fmt.Printf("Use: ssh -i %s ubuntu@<instance-ip>\n", privateKeyFile)
	return generatedKey.Name, privateKeyFile, nil
}

// ListSSHKeys retrieves a list of SSH keys from Lambda Cloud
func (p *LambdaProvider) ListSSHKeys(httpClient *http.Client, apiToken string) ([]SSHKey, error) {
	req, err := http.NewRequest("GET", "https://cloud.lambda.ai/api/v1/ssh-keys", nil)
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
		Data []SSHKey `json:"data"`
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return apiResponse.Data, nil
}

// DeleteSSHKey deletes an SSH key from Lambda Cloud by ID
func (p *LambdaProvider) DeleteSSHKey(httpClient *http.Client, apiToken string, sshKeyID string) error {
	if sshKeyID == "" {
		return fmt.Errorf("SSH key ID is required")
	}

	url := fmt.Sprintf("https://cloud.lambda.ai/api/v1/ssh-keys/%s", sshKeyID)
	req, err := http.NewRequest("DELETE", url, nil)
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

// GetAvailableInstanceTypes retrieves available instance types from Lambda Cloud
func (p *LambdaProvider) GetAvailableInstanceTypes(httpClient *http.Client, apiToken string) ([]Instance, error) {
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

// CheckInstanceTypeAvailability checks if a specific instance type is available and returns the regions with capacity
func (p *LambdaProvider) CheckInstanceTypeAvailability(httpClient *http.Client, apiToken string, instanceTypeName string) ([]Region, error) {
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
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
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

	// Look up the specific instance type
	instance, exists := response.Data[instanceTypeName]
	if !exists {
		return nil, fmt.Errorf("instance type %s not found", instanceTypeName)
	}

	// Return the regions with capacity available
	return instance.RegionsWithCapacityAvailable, nil
}

// CreateFilesystem creates a new filesystem in the specified region and saves it to config
func (p *LambdaProvider) CreateFilesystem(httpClient *http.Client, apiToken string, name string, region string) (*Filesystem, error) {
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
	database, err := db.InitDB()
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
func (p *LambdaProvider) GetFilesystemInfo(filesystemName string) (*FilesystemInfo, error) {
	return GetFilesystemInfo(filesystemName)
}

// ListFilesystems retrieves a list of filesystems from Lambda Cloud
func (p *LambdaProvider) ListFilesystems(httpClient *http.Client, apiToken string) ([]Filesystem, error) {
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
func (p *LambdaProvider) DeleteFilesystem(httpClient *http.Client, apiToken string, filesystemID string) error {
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

// GetGlobalFirewallRules retrieves the current global firewall ruleset
func (p *LambdaProvider) GetGlobalFirewallRules(httpClient *http.Client, apiToken string) (*GlobalFirewallRuleset, error) {
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
func (p *LambdaProvider) UpdateGlobalFirewallRules(httpClient *http.Client, apiToken string, rules []FirewallRule) (*GlobalFirewallRuleset, error) {
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
func (p *LambdaProvider) EnsurePortOpen(httpClient *http.Client, apiToken string, port int, protocol string, description string) error {
	// Get current rules
	currentRuleset, err := p.GetGlobalFirewallRules(httpClient, apiToken)
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
	_, err = p.UpdateGlobalFirewallRules(httpClient, apiToken, updatedRules)
	if err != nil {
		return fmt.Errorf("failed to update firewall rules: %w", err)
	}

	return nil
}
