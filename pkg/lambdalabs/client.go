package lambdalabs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	Name        string    // Provider-specific instance type name
	Description string    // Human-readable description
	GPUs        []GPUType // GPUs in this instance type
	VCPUs       int       // Number of virtual CPUs
	RAM_GB      int       // RAM in GB
	Storage_GB  int       // Storage in GB
	PriceCentsPerHour int // Cost in cents per hour
	Regions     []string  // Available regions
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
	Protocol     string // "tcp", "udp", "icmp"
	Port         int    // Port number (for TCP/UDP)
	SourceNetwork string // CIDR notation, e.g., "0.0.0.0/0"
	Description  string // Human-readable description
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
	apiToken string
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
			InstanceType          InstanceType `json:"instance_type"`
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
		RegionName      string   `json:"region_name"`
		InstanceTypeName string  `json:"instance_type_name"`
		SSHKeyNames     []string `json:"ssh_key_names"`
		Name            string   `json:"name,omitempty"`
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
			"protocol":      rule.Protocol,
			"port_range":    [2]int{rule.Port, rule.Port},
			"source_network": rule.SourceNetwork,
			"description":   rule.Description,
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

