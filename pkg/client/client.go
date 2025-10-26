package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
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

func main() {
	apiToken := os.Getenv("LAMBDA_API_KEY")
	c := http.Client{Timeout: time.Duration(5) * time.Second}

	instances, err := getAvailableInstanceTypes(&c, apiToken)
	if err != nil {
		fmt.Println("Error getting available instance types: ", err)
		return
	}

	maxCudaVersionFloat := float64(0)
	maxInstanceTypeName := ""
	maxRegionName := ""
	for _, instance := range instances {
		if minCudaVersions[instance.InstanceType.GPUDescription] > maxCudaVersionFloat {
			maxCudaVersionFloat = minCudaVersions[instance.InstanceType.GPUDescription]
			maxInstanceTypeName = instance.InstanceType.Name

			maxRegionName = instance.RegionsWithCapacityAvailable[0].Name
		}
	}

	fmt.Printf("Max cuda version: %.1f\n", maxCudaVersionFloat)
	fmt.Printf("Max instance type name: %s\n", maxInstanceTypeName)
	fmt.Printf("Max region name: %s\n", maxRegionName)

	// Create a new SSH key for programmatic access
	sshKeyName, sshKeyFile, err := createSSHKeyForProject(&c, apiToken)
	if err != nil {
		fmt.Println("Error creating SSH key: ", err)
		return
	}

	launchedInstances, err := launchInstance(&c, apiToken, maxInstanceTypeName, maxRegionName, []string{sshKeyName}, []string{sshKeyFile}, 1, "test")
	if err != nil {
		fmt.Println("Error launching instance: ", err)
		return
	}

	// Print instance info with SSH access details
	for _, instance := range launchedInstances {
		fmt.Printf("Launched instance: %s\n", instance.ID)
		fmt.Printf("SSH Key: %s\n", instance.SSHKeyName)
		fmt.Printf("SSH Key File: %s\n", instance.SSHKeyFile)
		fmt.Printf("Connect with: ssh -i %s ubuntu@<instance-ip>\n\n", instance.SSHKeyFile)
	}
}

// launchInstanceSimple launches a single instance with default settings
func launchInstanceSimple(httpClient *http.Client, apiToken string, instanceType string, region string, sshKeyName string, sshKeyFile string) (*LaunchedInstance, error) {
	instances, err := launchInstance(httpClient, apiToken, instanceType, region, []string{sshKeyName}, []string{sshKeyFile}, 1, "")
	if err != nil {
		return nil, err
	}
	return &instances[0], nil
}

func launchInstance(httpClient *http.Client, apiToken string, instanceType string, region string, sshKeyNames []string, sshKeyFiles []string, quantity int, name string) ([]LaunchedInstance, error) {
	if len(sshKeyNames) == 0 {
		return nil, fmt.Errorf("at least one SSH key name is required")
	}
	if quantity <= 0 {
		quantity = 1 // Default to 1
	}

	requestBody := InstanceLaunchRequest{
		RegionName:       region,
		InstanceTypeName: instanceType,
		SSHKeyNames:      sshKeyNames,
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
		sshKeyIndex := i % len(sshKeyNames) // Cycle through SSH keys if multiple instances
		instances[i] = LaunchedInstance{
			ID:         instanceID,
			SSHKeyName: sshKeyNames[sshKeyIndex],
			SSHKeyFile: sshKeyFiles[sshKeyIndex],
		}
	}

	return instances, nil
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

// createSSHKeyForProject creates a new SSH key and saves it in the project root
func createSSHKeyForProject(httpClient *http.Client, apiToken string) (string, string, error) {
	// Create unique key name with timestamp
	timestamp := time.Now().Unix()
	keyName := "lambda-key-" + strconv.FormatInt(timestamp, 10)

	// Create the SSH key
	generatedKey, err := createSSHKey(httpClient, apiToken, keyName)
	if err != nil {
		return "", "", fmt.Errorf("failed to create SSH key: %w", err)
	}

	// Save the private key in project root
	privateKeyFile := keyName + ".pem"
	err = os.WriteFile(privateKeyFile, []byte(generatedKey.PrivateKey), 0600)
	if err != nil {
		return "", "", fmt.Errorf("failed to save private key: %w", err)
	}

	fmt.Printf("Created new SSH key '%s' and saved private key to %s\n", keyName, privateKeyFile)
	fmt.Printf("Use: ssh -i %s ubuntu@<instance-ip>\n", privateKeyFile)
	return generatedKey.Name, privateKeyFile, nil
}

func getAvailableInstanceTypes(httpClient *http.Client, apiToken string) ([]Instance, error) {
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
