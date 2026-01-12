package remote

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/atoniolo76/gotoni/pkg/db"
)

// OrgoProvider implements the CloudProvider interface for Orgo
type OrgoProvider struct{}

// Remote Command Execution Example:
//
// To execute commands remotely on Orgo computers (same as SSH ExecuteCommand):
//
// 1. Launch a computer:
//    instances, err := provider.LaunchInstance(httpClient, apiToken, "4gb", "my-project", 1, "test-computer", "", "")
//
// 2. Execute bash commands (same signature as SSH ExecuteCommand):
//    output, err := provider.ExecuteBashCommand(instanceID, "ls -la")
//
// 3. Execute Python code:
//    output, err := provider.ExecutePythonCode(instanceID, "print('Hello from Python!')", 30)
//
// Note: Commands execute in the computer's Linux environment with access to standard system utilities.
// The ORGO_API_KEY environment variable is used automatically.

// NewOrgoProvider creates a new Orgo cloud provider
func NewOrgoProvider() *OrgoProvider {
	return &OrgoProvider{}
}

// Orgo-specific data structures

type OrgoComputerCreateRequest struct {
	Name string `json:"name"`
	OS   string `json:"os,omitempty"`
	RAM  int    `json:"ram,omitempty"`
	CPU  int    `json:"cpu,omitempty"`
}

type OrgoComputerResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ProjectName string `json:"project_name"`
	OS          string `json:"os"`
	RAM         int    `json:"ram"`
	CPU         int    `json:"cpu"`
	Status      string `json:"status"`
	URL         string `json:"url"`
	CreatedAt   string `json:"created_at"`
}

type OrgoProjectResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type OrgoProjectCreateRequest struct {
	Name string `json:"name"`
}

// parseInstanceType extracts RAM size from instance type string (e.g., "2gb" -> 2)
func (p *OrgoProvider) parseInstanceType(instanceType string) (int, error) {
	// Remove "gb" suffix and parse as int
	if strings.HasSuffix(strings.ToLower(instanceType), "gb") {
		ramStr := strings.TrimSuffix(strings.ToLower(instanceType), "gb")
		ram, err := strconv.Atoi(ramStr)
		if err != nil {
			return 0, fmt.Errorf("invalid instance type format: %s", instanceType)
		}
		return ram, nil
	}

	// Try to parse as direct number
	ram, err := strconv.Atoi(instanceType)
	if err != nil {
		return 0, fmt.Errorf("invalid instance type: %s", instanceType)
	}
	return ram, nil
}

// LaunchInstance creates new Orgo computers
func (p *OrgoProvider) LaunchInstance(httpClient *http.Client, apiToken string, instanceType string, region string, quantity int, name string, sshKeyName string, filesystemName string) ([]LaunchedInstance, error) {
	if quantity <= 0 {
		quantity = 1
	}
	if name == "" {
		name = "default"
	}

	// Parse RAM from instance type
	ram, err := p.parseInstanceType(instanceType)
	if err != nil {
		return nil, fmt.Errorf("failed to parse instance type: %w", err)
	}

	// Get the default project for this API key
	projectName, err := p.getDefaultProject(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get default project: %w", err)
	}

	var instances []LaunchedInstance

	// Launch computers
	for idx := 0; idx < quantity; idx++ {
		computerName := name
		if quantity > 1 {
			computerName = fmt.Sprintf("%s-%d", name, idx+1)
		}

		computerID, err := p.createComputer(httpClient, apiToken, projectName, computerName, ram)
		if err != nil {
			return nil, fmt.Errorf("failed to create computer %s: %w", computerName, err)
		}

		// For Orgo, SSH keys are handled differently - we'll use empty SSH key info
		instances = append(instances, LaunchedInstance{
			ID:         computerID,
			SSHKeyName: "", // Orgo doesn't use SSH keys in the same way
			SSHKeyFile: "",
		})

		// Save instance info to DB
		database, err := db.InitDB()
		if err != nil {
			return nil, fmt.Errorf("failed to init db: %w", err)
		}
		defer database.Close()

		inst := &db.Instance{
			ID:             computerID,
			Name:           computerName,
			Region:         projectName,
			Status:         "running", // Orgo computers start as running
			SSHKeyName:     "",
			FilesystemName: filesystemName,
			InstanceType:   instanceType,
		}
		if err := database.SaveInstance(inst); err != nil {
			return nil, fmt.Errorf("failed to save instance to db: %w", err)
		}
	}

	return instances, nil
}

// LaunchAndWait launches computers and waits for them to be ready
func (p *OrgoProvider) LaunchAndWait(httpClient *http.Client, apiToken string, instanceType string, region string, quantity int, name string, sshKeyName string, timeout time.Duration, filesystemName string) ([]LaunchedInstance, error) {
	instances, err := p.LaunchInstance(httpClient, apiToken, instanceType, region, quantity, name, sshKeyName, filesystemName)
	if err != nil {
		return nil, fmt.Errorf("failed to launch instances: %w", err)
	}

	if timeout == 0 {
		timeout = 5 * time.Minute // Default timeout for Orgo
	}

	// Wait for each instance to be ready
	for _, instance := range instances {
		fmt.Printf("Waiting for computer %s to be ready...\n", instance.ID)
		if err := p.WaitForInstanceReady(httpClient, apiToken, instance.ID, timeout); err != nil {
			return nil, fmt.Errorf("computer %s failed to become ready: %w", instance.ID, err)
		}
		fmt.Printf("Computer %s is now ready!\n", instance.ID)

		// Get full instance details
		instanceDetails, err := p.GetInstance(httpClient, apiToken, instance.ID)
		if err != nil {
			fmt.Printf("Warning: Failed to get computer details: %v\n", err)
			continue
		}

		// Update database with full details
		database, err := db.InitDB()
		if err != nil {
			return nil, fmt.Errorf("failed to init db: %w", err)
		}
		defer database.Close()

		inst := &db.Instance{
			ID:             instance.ID,
			Name:           name,
			Region:         region,
			Status:         instanceDetails.Status,
			SSHKeyName:     "",
			FilesystemName: filesystemName,
			InstanceType:   instanceDetails.InstanceType.Name,
		}
		if err := database.SaveInstance(inst); err != nil {
			fmt.Printf("Warning: Failed to update instance in db: %v\n", err)
		}
	}

	return instances, nil
}

// WaitForInstanceReady waits for a computer to become active
func (p *OrgoProvider) WaitForInstanceReady(httpClient *http.Client, apiToken string, instanceID string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	timeoutChan := time.After(timeout)

	for {
		select {
		case <-timeoutChan:
			return fmt.Errorf("timeout waiting for computer %s to become ready", instanceID)

		case <-ticker.C:
			instance, err := p.GetInstance(httpClient, apiToken, instanceID)
			if err != nil {
				return fmt.Errorf("failed to get computer status: %w", err)
			}

			// Orgo computers start as "running" immediately
			if instance.Status == "running" {
				return nil
			}
		}
	}
}

// GetInstance retrieves details for a specific computer
func (p *OrgoProvider) GetInstance(httpClient *http.Client, apiToken string, instanceID string) (*RunningInstance, error) {
	url := fmt.Sprintf("https://www.orgo.ai/api/computers/%s", instanceID)

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
		Data OrgoComputerResponse `json:"data"`
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Convert Orgo response to RunningInstance format
	runningInstance := &RunningInstance{
		ID:     apiResponse.Data.ID,
		Name:   apiResponse.Data.Name,
		Status: apiResponse.Data.Status,
		IP:     "", // Orgo doesn't provide direct IP access
		InstanceType: InstanceType{
			Name: fmt.Sprintf("%dgb", apiResponse.Data.RAM),
		},
		Region: Region{
			Name: apiResponse.Data.ProjectName,
		},
	}

	return runningInstance, nil
}

// ListRunningInstances retrieves a list of running computers from Orgo
func (p *OrgoProvider) ListRunningInstances(httpClient *http.Client, apiToken string) ([]RunningInstance, error) {
	// First get all projects
	projects, err := p.listProjects(httpClient, apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}

	var allInstances []RunningInstance

	// For each project, list computers
	for _, project := range projects {
		url := fmt.Sprintf("https://www.orgo.ai/api/projects/%s/computers", project.Name)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to execute request: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue // Skip projects that fail
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}

		var apiResponse struct {
			Data struct {
				Computers []OrgoComputerResponse `json:"computers"`
			} `json:"data"`
		}

		err = json.Unmarshal(body, &apiResponse)
		if err != nil {
			continue
		}

		// Convert to RunningInstance format
		for _, computer := range apiResponse.Data.Computers {
			if computer.Status == "running" {
				runningInstance := RunningInstance{
					ID:     computer.ID,
					Name:   computer.Name,
					Status: computer.Status,
					IP:     "",
					InstanceType: InstanceType{
						Name: fmt.Sprintf("%dgb", computer.RAM),
					},
					Region: Region{
						Name: computer.ProjectName,
					},
				}
				allInstances = append(allInstances, runningInstance)
			}
		}
	}

	return allInstances, nil
}

// TerminateInstance terminates Orgo computers
func (p *OrgoProvider) TerminateInstance(httpClient *http.Client, apiToken string, instanceIDs []string) (*InstanceTerminateResponse, error) {
	if len(instanceIDs) == 0 {
		return nil, fmt.Errorf("at least one instance ID is required")
	}

	var terminatedInstances []RunningInstance

	for _, instanceID := range instanceIDs {
		url := fmt.Sprintf("https://www.orgo.ai/api/computers/%s", instanceID)

		req, err := http.NewRequest("DELETE", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request for instance %s: %w", instanceID, err)
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to execute request for instance %s: %w", instanceID, err)
		}

		resp.Body.Close()

		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
			// Try to get instance details before termination
			instance, err := p.GetInstance(httpClient, apiToken, instanceID)
			if err != nil {
				// If we can't get details, create a minimal RunningInstance
				instance = &RunningInstance{
					ID:     instanceID,
					Status: "terminated",
				}
			}
			terminatedInstances = append(terminatedInstances, *instance)
		} else {
			fmt.Printf("Warning: Failed to terminate instance %s (status %d)\n", instanceID, resp.StatusCode)
		}
	}

	return &InstanceTerminateResponse{
		TerminatedInstances: terminatedInstances,
	}, nil
}

// Helper methods

func (p *OrgoProvider) ensureProjectExists(httpClient *http.Client, apiToken string, projectName string) error {
	// Try to get the project first
	url := fmt.Sprintf("https://www.orgo.ai/api/projects/by-name/%s", projectName)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// Project exists
		return nil
	}

	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected response when checking project: %s", string(body))
	}

	// Project doesn't exist, create it
	return p.createProject(httpClient, apiToken, projectName)
}

func (p *OrgoProvider) createProject(httpClient *http.Client, apiToken string, projectName string) error {
	requestBody := OrgoProjectCreateRequest{Name: projectName}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", "https://www.orgo.ai/api/projects", strings.NewReader(string(jsonBody)))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))
	req.Header.Set("Content-Type", "application/json")

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

func (p *OrgoProvider) createComputer(httpClient *http.Client, apiToken string, projectName string, computerName string, ram int) (string, error) {
	requestBody := OrgoComputerCreateRequest{
		Name: computerName,
		OS:   "linux", // Default to Linux
		RAM:  ram,
		CPU:  2, // Default to 2 CPU cores
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := fmt.Sprintf("https://www.orgo.ai/api/projects/%s/computers", projectName)
	fmt.Printf("DEBUG: Creating computer at URL: %s\n", url)
	fmt.Printf("DEBUG: Request body: %s\n", string(jsonBody))

	req, err := http.NewRequest("POST", url, strings.NewReader(string(jsonBody)))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("DEBUG: Create computer response status: %d, body: %s\n", resp.StatusCode, string(body))

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var apiResponse struct {
		Data OrgoComputerResponse `json:"data"`
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return apiResponse.Data.ID, nil
}

func (p *OrgoProvider) listProjects(httpClient *http.Client, apiToken string) ([]OrgoProjectResponse, error) {
	req, err := http.NewRequest("GET", "https://www.orgo.ai/api/projects", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("DEBUG: List projects response status: %d, body: %s\n", resp.StatusCode, string(body))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var apiResponse struct {
		Data []OrgoProjectResponse `json:"data"`
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return apiResponse.Data, nil
}

// getDefaultProject returns the first available project for the API key
// Since Orgo API keys are tied to projects, this gets the project automatically
func (p *OrgoProvider) getDefaultProject(httpClient *http.Client, apiToken string) (string, error) {
	projects, err := p.listProjects(httpClient, apiToken)
	if err != nil {
		return "", fmt.Errorf("failed to list projects: %w", err)
	}

	if len(projects) == 0 {
		// If no projects found, try to use a default project name
		// This can happen when API key is project-scoped
		defaultProjectName := "default"
		fmt.Printf("No projects found for API key, trying default project '%s'\n", defaultProjectName)
		return defaultProjectName, nil
	}

	// Return the first project (API key is tied to projects)
	return projects[0].Name, nil
}

// Stub implementations for methods not applicable to Orgo

// CreateSSHKeyForProject - Orgo doesn't use SSH keys in the same way
func (p *OrgoProvider) CreateSSHKeyForProject(httpClient *http.Client, apiToken string) (string, string, error) {
	return "", "", fmt.Errorf("SSH keys are not supported for Orgo provider")
}

// ListSSHKeys - Orgo doesn't use SSH keys in the same way
func (p *OrgoProvider) ListSSHKeys(httpClient *http.Client, apiToken string) ([]SSHKey, error) {
	return []SSHKey{}, nil // Return empty list
}

// DeleteSSHKey - Orgo doesn't use SSH keys in the same way
func (p *OrgoProvider) DeleteSSHKey(httpClient *http.Client, apiToken string, sshKeyID string) error {
	return fmt.Errorf("SSH keys are not supported for Orgo provider")
}

// GetAvailableInstanceTypes returns available RAM-based instance types for Orgo
func (p *OrgoProvider) GetAvailableInstanceTypes(httpClient *http.Client, apiToken string) ([]Instance, error) {
	// Orgo supports 2GB, 4GB, and 8GB RAM configurations as requested
	instances := []Instance{
		{
			InstanceType: InstanceType{
				Name: "2gb",
			},
			RegionsWithCapacityAvailable: []Region{
				{Name: "default", Description: "Default Project"},
			},
		},
		{
			InstanceType: InstanceType{
				Name: "4gb",
			},
			RegionsWithCapacityAvailable: []Region{
				{Name: "default", Description: "Default Project"},
			},
		},
		{
			InstanceType: InstanceType{
				Name: "8gb",
			},
			RegionsWithCapacityAvailable: []Region{
				{Name: "default", Description: "Default Project"},
			},
		},
	}
	return instances, nil
}

// CheckInstanceTypeAvailability checks if a specific RAM-based instance type is available
func (p *OrgoProvider) CheckInstanceTypeAvailability(httpClient *http.Client, apiToken string, instanceTypeName string) ([]Region, error) {
	// Check if the instance type is one of our supported RAM configurations
	validTypes := map[string]bool{
		"2gb": true,
		"4gb": true,
		"8gb": true,
	}

	if !validTypes[strings.ToLower(instanceTypeName)] {
		return nil, fmt.Errorf("instance type %s not supported by Orgo provider", instanceTypeName)
	}

	return []Region{
		{Name: "default", Description: "Default Project"},
	}, nil
}

// Filesystem operations - Orgo doesn't have traditional filesystems
func (p *OrgoProvider) CreateFilesystem(httpClient *http.Client, apiToken string, name string, region string) (*Filesystem, error) {
	return nil, fmt.Errorf("filesystems are not supported for Orgo provider")
}

func (p *OrgoProvider) GetFilesystemInfo(filesystemName string) (*FilesystemInfo, error) {
	return nil, fmt.Errorf("filesystems are not supported for Orgo provider")
}

func (p *OrgoProvider) ListFilesystems(httpClient *http.Client, apiToken string) ([]Filesystem, error) {
	return []Filesystem{}, nil
}

func (p *OrgoProvider) DeleteFilesystem(httpClient *http.Client, apiToken string, filesystemID string) error {
	return fmt.Errorf("filesystems are not supported for Orgo provider")
}

// Firewall operations - Orgo handles security differently
func (p *OrgoProvider) GetGlobalFirewallRules(httpClient *http.Client, apiToken string) (*GlobalFirewallRuleset, error) {
	return nil, fmt.Errorf("firewall rules are not supported for Orgo provider")
}

func (p *OrgoProvider) UpdateGlobalFirewallRules(httpClient *http.Client, apiToken string, rules []FirewallRule) (*GlobalFirewallRuleset, error) {
	return nil, fmt.Errorf("firewall rules are not supported for Orgo provider")
}

func (p *OrgoProvider) EnsurePortOpen(httpClient *http.Client, apiToken string, port int, protocol string, description string) error {
	return fmt.Errorf("firewall rules are not supported for Orgo provider")
}

// ExecuteBashCommand executes a bash command on an Orgo computer (same signature as SSH ExecuteCommand)
func (p *OrgoProvider) ExecuteBashCommand(instanceID string, command string) (string, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	apiToken := os.Getenv("ORGO_API_KEY")

	url := fmt.Sprintf("https://www.orgo.ai/api/computers/%s/bash", instanceID)

	requestBody := map[string]string{
		"command": command,
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", url, strings.NewReader(string(jsonBody)))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var apiResponse struct {
		Data struct {
			Output string `json:"output"`
		} `json:"data"`
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return apiResponse.Data.Output, nil
}

// ExecutePythonCode executes Python code on an Orgo computer (same signature pattern as SSH ExecuteCommand)
func (p *OrgoProvider) ExecutePythonCode(instanceID string, code string, timeout int) (string, error) {
	httpClient := &http.Client{Timeout: time.Duration(timeout+10) * time.Second}
	apiToken := os.Getenv("ORGO_API_KEY")

	url := fmt.Sprintf("https://www.orgo.ai/api/computers/%s/exec", instanceID)

	requestBody := map[string]interface{}{
		"code": code,
	}
	if timeout > 0 {
		requestBody["timeout"] = timeout
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", url, strings.NewReader(string(jsonBody)))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var apiResponse struct {
		Data struct {
			Output string `json:"output"`
		} `json:"data"`
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return apiResponse.Data.Output, nil
}
