package lambdalabs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const lambdaLabsBaseURL = "https://cloud.lambda.ai/api/v1"

// GPUType represents a specific Nvidia GPU model with its capabilities
type GPUType struct {
	Name           string // GPU model identifier
	Description    string // Human-readable description
	VRAM_GB        int    // GPU memory in GB
	MinCUDAVersion string // Minimum required CUDA version (e.g., "12.0")
}

// InstanceType represents an instance configuration with GPU specs
type InstanceType struct {
	Name              string    // Provider-specific instance type name
	Description       string    // Human-readable description
	GPUs              []GPUType // GPUs in this instance type
	VCPUs             int       // Number of virtual CPUs
	RAM_GB            int       // RAM in GB
	Storage_GB        int       // Storage in GB
	PriceCentsPerHour int       // Cost in cents per hour
	Regions           []string  // Available regions
}

// Instance represents a running GPU instance/pod
type Instance struct {
	ID           string
	Name         string
	Status       string // "running", "stopped", "terminated", etc.
	InstanceType InstanceType
	Region       string
	PublicIP     string
	PrivateIP    string
	SSHKeys      []string
	CreatedAt    time.Time
}

// LaunchRequest represents parameters for launching a new instance
type LaunchRequest struct {
	InstanceTypeName string
	Region           string
	Name             string   // Optional instance name
	SSHKeyNames      []string // SSH keys to add
}

// SSHKey represents an SSH key pair
type SSHKey struct {
	ID         string
	Name       string
	PublicKey  string
	PrivateKey string // Only returned when provider generates the key
}

// FirewallRule represents a firewall rule for opening ports
type FirewallRule struct {
	Protocol      string // "tcp", "udp", "icmp"
	Port          int    // Port number (for TCP/UDP)
	SourceNetwork string // CIDR notation, e.g., "0.0.0.0/0"
	Description   string // Human-readable description
}

// NewTCPRules creates firewall rules for opening TCP ports
func NewTCPRules(ports []int, sourceNetwork string) []FirewallRule {
	rules := make([]FirewallRule, len(ports))
	for i, port := range ports {
		rules[i] = FirewallRule{
			Protocol:      "tcp",
			Port:          port,
			SourceNetwork: sourceNetwork,
			Description:   fmt.Sprintf("TCP port %d access", port),
		}
	}
	return rules
}

type Client struct {
	apiToken   string
	httpClient *http.Client
}

func NewClient(apiToken string) *Client {
	return &Client{
		apiToken: apiToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) GetProviderName() string {
	return "lambdalabs"
}

func (c *Client) makeRequest(method, endpoint string, body interface{}) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	url := lambdaLabsBaseURL + endpoint
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}

	return resp, nil
}

func (c *Client) ListInstances() ([]*Instance, error) {
	resp, err := c.makeRequest("GET", "/instances", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var response struct {
		Data []*Instance `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return response.Data, nil
}

func (c *Client) GetInstance(instanceID string) (*Instance, error) {
	resp, err := c.makeRequest("GET", "/instances/"+instanceID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var response struct {
		Data *Instance `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return response.Data, nil
}

func (c *Client) ListInstanceTypes() ([]*InstanceType, error) {
	resp, err := c.makeRequest("GET", "/instance-types", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var response struct {
		Data map[string]struct {
			InstanceType                 InstanceType `json:"instance_type"`
			RegionsWithCapacityAvailable []struct {
				Name string `json:"name"`
			} `json:"regions_with_capacity_available"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var instanceTypes []*InstanceType
	for _, item := range response.Data {
		instanceType := item.InstanceType
		var regions []string
		for _, region := range item.RegionsWithCapacityAvailable {
			regions = append(regions, region.Name)
		}
		instanceType.Regions = regions
		instanceTypes = append(instanceTypes, &instanceType)
	}

	return instanceTypes, nil
}

func (c *Client) LaunchInstance(req *LaunchRequest) (string, error) {
	launchReq := struct {
		RegionName       string   `json:"region_name"`
		InstanceTypeName string   `json:"instance_type_name"`
		SSHKeyNames      []string `json:"ssh_key_names"`
		Name             string   `json:"name,omitempty"`
	}{
		RegionName:       req.Region,
		InstanceTypeName: req.InstanceTypeName,
		SSHKeyNames:      req.SSHKeyNames,
		Name:             req.Name,
	}

	resp, err := c.makeRequest("POST", "/instance-operations/launch", launchReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var response struct {
		Data struct {
			InstanceIds []string `json:"instance_ids"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(response.Data.InstanceIds) == 0 {
		return "", fmt.Errorf("no instance IDs returned")
	}

	return response.Data.InstanceIds[0], nil
}

func (c *Client) TerminateInstance(instanceID string) error {
	terminateReq := struct {
		InstanceIds []string `json:"instance_ids"`
	}{
		InstanceIds: []string{instanceID},
	}

	resp, err := c.makeRequest("POST", "/instance-operations/terminate", terminateReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	return nil
}

// SSH Key Management

func (c *Client) ListSSHKeys() ([]*SSHKey, error) {
	resp, err := c.makeRequest("GET", "/ssh-keys", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var response struct {
		Data []*SSHKey `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return response.Data, nil
}

func (c *Client) CreateSSHKey(name string) (*SSHKey, error) {
	createReq := struct {
		Name string `json:"name"`
	}{
		Name: name,
	}

	resp, err := c.makeRequest("POST", "/ssh-keys", createReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var response struct {
		Data *SSHKey `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return response.Data, nil
}

func (c *Client) AddSSHKey(name, publicKey string) (*SSHKey, error) {
	addReq := struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}{
		Name:      name,
		PublicKey: publicKey,
	}

	resp, err := c.makeRequest("POST", "/ssh-keys", addReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var response struct {
		Data *SSHKey `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return response.Data, nil
}

// Firewall/Port Management

func (c *Client) CreateFirewallRuleset(name, region string, rules []FirewallRule) (string, error) {
	// Convert our FirewallRule to Lambda Labs format
	lambdaRules := make([]map[string]interface{}, len(rules))
	for i, rule := range rules {
		lambdaRules[i] = map[string]interface{}{
			"protocol":       rule.Protocol,
			"port_range":     [2]int{rule.Port, rule.Port},
			"source_network": rule.SourceNetwork,
			"description":    rule.Description,
		}
	}

	createReq := map[string]interface{}{
		"name":   name,
		"region": region,
		"rules":  lambdaRules,
	}

	resp, err := c.makeRequest("POST", "/firewall-rulesets", createReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var response struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return response.Data.ID, nil
}

var (
	H100_80GB_HBM3 = GPUType{
		Name:           "H100_80GB_HBM3",
		Description:    "H100 (80 GB HBM3)",
		VRAM_GB:        80,
		MinCUDAVersion: "12.0",
	}

	H100_80GB_SXM5 = GPUType{
		Name:           "H100_80GB_SXM5",
		Description:    "H100 (80 GB SXM5)",
		VRAM_GB:        80,
		MinCUDAVersion: "12.0",
	}

	H100_NVL = GPUType{
		Name:           "H100_NVL",
		Description:    "H100 NVL",
		VRAM_GB:        94,
		MinCUDAVersion: "12.0",
	}

	A100_40GB_PCIE = GPUType{
		Name:           "A100_40GB_PCIE",
		Description:    "A100 (40 GB PCIe)",
		VRAM_GB:        40,
		MinCUDAVersion: "11.0",
	}

	A100_80GB_PCIE = GPUType{
		Name:           "A100_80GB_PCIE",
		Description:    "A100 (80 GB PCIe)",
		VRAM_GB:        80,
		MinCUDAVersion: "11.0",
	}

	A100_80GB_SXM4 = GPUType{
		Name:           "A100_80GB_SXM4",
		Description:    "A100 (80 GB SXM4)",
		VRAM_GB:        80,
		MinCUDAVersion: "11.0",
	}

	A6000 = GPUType{
		Name:           "A6000",
		Description:    "RTX A6000 (48 GB)",
		VRAM_GB:        48,
		MinCUDAVersion: "11.0",
	}

	A5000 = GPUType{
		Name:           "A5000",
		Description:    "RTX A5000 (24 GB)",
		VRAM_GB:        24,
		MinCUDAVersion: "11.0",
	}

	A4000 = GPUType{
		Name:           "A4000",
		Description:    "RTX A4000 (16 GB)",
		VRAM_GB:        16,
		MinCUDAVersion: "11.0",
	}

	RTX_6000_ADA = GPUType{
		Name:           "RTX_6000_ADA",
		Description:    "RTX 6000 Ada (48 GB)",
		VRAM_GB:        48,
		MinCUDAVersion: "11.8",
	}

	RTX_5000_ADA = GPUType{
		Name:           "RTX_5000_ADA",
		Description:    "RTX 5000 Ada (32 GB)",
		VRAM_GB:        32,
		MinCUDAVersion: "11.8",
	}

	RTX_4000_ADA = GPUType{
		Name:           "RTX_4000_ADA",
		Description:    "RTX 4000 Ada (20 GB)",
		VRAM_GB:        20,
		MinCUDAVersion: "11.8",
	}

	RTX_6000 = GPUType{
		Name:           "RTX_6000",
		Description:    "RTX 6000 (24 GB)",
		VRAM_GB:        24,
		MinCUDAVersion: "10.0",
	}

	A10 = GPUType{
		Name:           "A10",
		Description:    "A10 (24 GB)",
		VRAM_GB:        24,
		MinCUDAVersion: "11.0",
	}

	L40 = GPUType{
		Name:           "L40",
		Description:    "L40 (48 GB)",
		VRAM_GB:        48,
		MinCUDAVersion: "11.8",
	}

	L40S = GPUType{
		Name:           "L40S",
		Description:    "L40S (48 GB)",
		VRAM_GB:        48,
		MinCUDAVersion: "11.8",
	}

	B200 = GPUType{
		Name:           "B200",
		Description:    "B200 (180 GB)",
		VRAM_GB:        180,
		MinCUDAVersion: "12.0",
	}

	GH200 = GPUType{
		Name:           "GH200",
		Description:    "GH200 (96 GB)",
		VRAM_GB:        96,
		MinCUDAVersion: "12.0",
	}

	V100_16GB = GPUType{
		Name:           "V100_16GB",
		Description:    "V100 (16 GB)",
		VRAM_GB:        16,
		MinCUDAVersion: "9.0",
	}

	V100_32GB = GPUType{
		Name:           "V100_32GB",
		Description:    "V100 (32 GB)",
		VRAM_GB:        32,
		MinCUDAVersion: "9.0",
	}
)

// GetGPUTypeByName returns a GPUType by its name identifier
func GetGPUTypeByName(name string) (GPUType, bool) {
	gpuTypes := map[string]GPUType{
		"H100_80GB_HBM3": H100_80GB_HBM3,
		"H100_80GB_SXM5": H100_80GB_SXM5,
		"H100_NVL":       H100_NVL,

		"A100_40GB_PCIE": A100_40GB_PCIE,
		"A100_80GB_PCIE": A100_80GB_PCIE,
		"A100_80GB_SXM4": A100_80GB_SXM4,

		"A6000": A6000,
		"A5000": A5000,
		"A4000": A4000,
		"A10":   A10,

		"RTX_6000_ADA": RTX_6000_ADA,
		"RTX_5000_ADA": RTX_5000_ADA,
		"RTX_4000_ADA": RTX_4000_ADA,

		"RTX_6000": RTX_6000,

		"L40":  L40,
		"L40S": L40S,

		"B200":  B200,
		"GH200": GH200,

		"V100_16GB": V100_16GB,
		"V100_32GB": V100_32GB,
	}

	gpu, exists := gpuTypes[name]
	return gpu, exists
}

// GetAllGPUs returns a slice of all available GPU types
func GetAllGPUs() []GPUType {
	gpuTypes := map[string]GPUType{
		"H100_80GB_HBM3": H100_80GB_HBM3,
		"H100_80GB_SXM5": H100_80GB_SXM5,
		"H100_NVL":       H100_NVL,

		"A100_40GB_PCIE": A100_40GB_PCIE,
		"A100_80GB_PCIE": A100_80GB_PCIE,
		"A100_80GB_SXM4": A100_80GB_SXM4,

		"A6000": A6000,
		"A5000": A5000,
		"A4000": A4000,
		"A10":   A10,

		"RTX_6000_ADA": RTX_6000_ADA,
		"RTX_5000_ADA": RTX_5000_ADA,
		"RTX_4000_ADA": RTX_4000_ADA,

		"RTX_6000": RTX_6000,

		"L40":  L40,
		"L40S": L40S,

		"B200":  B200,
		"GH200": GH200,

		"V100_16GB": V100_16GB,
		"V100_32GB": V100_32GB,
	}

	var allGPUs []GPUType
	for _, gpu := range gpuTypes {
		allGPUs = append(allGPUs, gpu)
	}
	return allGPUs
}

func GetCompatibleGPUs(cudaVersion string) []GPUType {
	if cudaVersion == "" {
		return []GPUType{}
	}

	version, err := parseVersion(cudaVersion)
	if err != nil {
		return []GPUType{}
	}

	var compatible []GPUType
	for _, gpu := range GetAllGPUs() {
		gpuVersion, err := parseVersion(gpu.MinCUDAVersion)
		if err != nil {
			continue
		}
		if version >= gpuVersion {
			compatible = append(compatible, gpu)
		}
	}

	return compatible
}

func parseVersion(version string) (float64, error) {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid version format: %s", version)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}

	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}

	return float64(major) + float64(minor)/10.0, nil
}
