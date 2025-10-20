package runpod

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"toni/gpusnapshot/internal/providers"
)

const runpodBaseURL = "https://rest.runpod.io/v1"

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
	return "runpod"
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

	url := runpodBaseURL + endpoint
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

// RunPod API structures
type RunPodPod struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	DesiredStatus         string    `json:"desiredStatus"`
	RuntimeInMin          int       `json:"runtimeInMin"`
	PodType               string    `json:"podType"`
	GPUTypeID             string    `json:"gpuTypeId"`
	GPUCount              int       `json:"gpuCount"`
	VCPUCount             int       `json:"vcpuCount"`
	RAMInGB               int       `json:"ramInGb"`
	ContainerDiskInGB     int       `json:"containerDiskInGb"`
	VolumeInGB            int       `json:"volumeInGb"`
	VolumeMountPath       string    `json:"volumeMountPath"`
	ImageName             string    `json:"imageName"`
	StartJupyter          bool      `json:"startJupyter"`
	JupyterToken          string    `json:"jupyterToken"`
	JupyterPort           int       `json:"jupyterPort"`
	ContainerUser         string    `json:"containerUser"`
	ContainerPassword     string    `json:"containerPassword"`
	StartSSH              bool      `json:"startSsh"`
	SSHPort               int       `json:"sshPort"`
	NetworkVolumeID       string    `json:"networkVolumeId"`
	MachineID             string    `json:"machineId"`
	MachineIP             string    `json:"machineIp"`
	MachineRegion         string    `json:"machineRegion"`
	MachineHostname       string    `json:"machineHostname"`
	CostPerHr             float64   `json:"costPerHr"`
	AdjustedCostPerHr     float64   `json:"adjustedCostPerHr"`
	LastStatusChange      time.Time `json:"lastStatusChange"`
	LastStartTime         time.Time `json:"lastStartTime"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

type RunPodCreateInput struct {
	Name                  string   `json:"name"`
	ImageName             string   `json:"imageName"`
	GPUTypeID             string   `json:"gpuTypeId"`
	CloudType             string   `json:"cloudType,omitempty"`
	ContainerDiskInGB     int      `json:"containerDiskInGb,omitempty"`
	VolumeInGB            int      `json:"volumeInGb,omitempty"`
	VolumeMountPath       string   `json:"volumeMountPath,omitempty"`
	StartJupyter          bool     `json:"startJupyter,omitempty"`
	JupyterPort           int      `json:"jupyterPort,omitempty"`
	ContainerUser         string   `json:"containerUser,omitempty"`
	ContainerPassword     string   `json:"containerPassword,omitempty"`
	StartSSH              bool     `json:"startSsh,omitempty"`
	SSHPort               int      `json:"sshPort,omitempty"`
	AllowedCudaVersions  []string `json:"allowedCudaVersions,omitempty"`
	TemplateID            string   `json:"templateId,omitempty"`
	NetworkVolumeID       string   `json:"networkVolumeId,omitempty"`
	ContainerRegistryAuthID string `json:"containerRegistryAuthId,omitempty"`
	Env                   []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"env,omitempty"`
	Ports                 []string `json:"ports,omitempty"`
}

func (c *Client) ListInstances() ([]*providers.Instance, error) {
	resp, err := c.makeRequest("GET", "/pods", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var runpodPods []RunPodPod
	if err := json.NewDecoder(resp.Body).Decode(&runpodPods); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var instances []*providers.Instance
	for _, pod := range runpodPods {
		instance := c.convertPodToInstance(&pod)
		instances = append(instances, instance)
	}

	return instances, nil
}

func (c *Client) GetInstance(instanceID string) (*providers.Instance, error) {
	resp, err := c.makeRequest("GET", "/pods/"+instanceID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var pod RunPodPod
	if err := json.NewDecoder(resp.Body).Decode(&pod); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return c.convertPodToInstance(&pod), nil
}

func (c *Client) LaunchInstance(req *providers.LaunchRequest) (string, error) {
	// Convert LaunchRequest to RunPod format
	createInput := RunPodCreateInput{
		Name:              req.Name,
		ImageName:         "runpod/pytorch:2.1.0-py3.10-cuda11.8.0-devel-ubuntu22.04", // Default image
		CloudType:         "SECURE",
		ContainerDiskInGB: 50,
		StartSSH:          true,
		SSHPort:           22,
	}

	// Set GPU type if specified
	if req.InstanceTypeName != "" {
		createInput.GPUTypeID = req.InstanceTypeName
	}

	// Set container configuration if provided
	if req.Container != nil {
		createInput.ImageName = req.Container.Image
		if len(req.Container.Ports) > 0 {
			createInput.Ports = req.Container.Ports
		}
		// Convert environment variables
		for key, value := range req.Container.Environment {
			createInput.Env = append(createInput.Env, struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			}{Key: key, Value: value})
		}
	}

	resp, err := c.makeRequest("POST", "/pods", createInput)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error: %s - %s", resp.Status, string(body))
	}

	var pod RunPodPod
	if err := json.NewDecoder(resp.Body).Decode(&pod); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return pod.ID, nil
}

func (c *Client) TerminateInstance(instanceID string) error {
	resp, err := c.makeRequest("DELETE", "/pods/"+instanceID, nil)
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

func (c *Client) ListInstanceTypes() ([]*providers.InstanceType, error) {
	// RunPod doesn't have a direct instance types endpoint
	// We'll need to get this from pods or implement a different approach
	// For now, return some common GPU types based on the API spec
	instanceTypes := []*providers.InstanceType{
		{
			Name:        "NVIDIA GeForce RTX 4090",
			Description: "NVIDIA GeForce RTX 4090 GPU",
			GPUs: []providers.GPUType{
				{
					Name:               "RTX_4090",
					Description:        "NVIDIA GeForce RTX 4090",
					VRAM_GB:            24,
					CUDACores:          16384,
					ComputeCapability:  "8.9",
					BaseClock_MHz:      2230,
					MemoryClock_MHz:    1313,
					FP32_TFLOPS:        83.0,
				},
			},
			VCPUs: 8,
			RAM_GB: 30,
			Storage_GB: 100,
		},
		{
			Name:        "NVIDIA A100",
			Description: "NVIDIA A100 GPU",
			GPUs: []providers.GPUType{
				{
					Name:               "A100",
					Description:        "NVIDIA A100 (80GB)",
					VRAM_GB:            80,
					CUDACores:          6912,
					ComputeCapability:  "8.0",
					BaseClock_MHz:      1410,
					MemoryClock_MHz:    1215,
					FP32_TFLOPS:        19.5,
				},
			},
			VCPUs: 8,
			RAM_GB: 30,
			Storage_GB: 100,
		},
		{
			Name:        "NVIDIA H100",
			Description: "NVIDIA H100 GPU",
			GPUs: []providers.GPUType{
				{
					Name:               "H100",
					Description:        "NVIDIA H100 (80GB)",
					VRAM_GB:            80,
					CUDACores:          16896,
					ComputeCapability:  "9.0",
					BaseClock_MHz:      1980,
					MemoryClock_MHz:    1750,
					FP32_TFLOPS:        67.0,
				},
			},
			VCPUs: 8,
			RAM_GB: 30,
			Storage_GB: 100,
		},
	}

	return instanceTypes, nil
}

// SSH Key Management - RunPod doesn't seem to have SSH key management in the API
// These methods return empty results or errors as appropriate

func (c *Client) ListSSHKeys() ([]*providers.SSHKey, error) {
	// RunPod doesn't have SSH key management API
	return []*providers.SSHKey{}, nil
}

func (c *Client) CreateSSHKey(name string) (*providers.SSHKey, error) {
	// RunPod doesn't have SSH key management API
	return nil, fmt.Errorf("SSH key management not supported by RunPod")
}

func (c *Client) AddSSHKey(name, publicKey string) (*providers.SSHKey, error) {
	// RunPod doesn't have SSH key management API
	return nil, fmt.Errorf("SSH key management not supported by RunPod")
}

// Helper function to convert RunPod pod to providers.Instance
func (c *Client) convertPodToInstance(pod *RunPodPod) *providers.Instance {
	// Map RunPod status to standard status
	status := pod.DesiredStatus
	switch pod.DesiredStatus {
	case "RUNNING":
		status = "running"
	case "EXITED":
		status = "stopped"
	case "TERMINATED":
		status = "terminated"
	}

	// Create instance type from pod info
	instanceType := providers.InstanceType{
		Name:        pod.GPUTypeID,
		Description: pod.GPUTypeID,
		GPUs: []providers.GPUType{
			{
				Name: pod.GPUTypeID,
				// We'll need to populate more details from a GPU types lookup
			},
		},
		VCPUs:      pod.VCPUCount,
		RAM_GB:     pod.RAMInGB,
		Storage_GB: pod.ContainerDiskInGB,
	}

	return &providers.Instance{
		ID:           pod.ID,
		Name:         pod.Name,
		Status:       status,
		InstanceType: instanceType,
		Region:       pod.MachineRegion,
		PublicIP:     pod.MachineIP,
		PrivateIP:    pod.MachineIP, // RunPod doesn't distinguish between public/private IPs
		SSHKeys:      []string{}, // RunPod doesn't manage SSH keys separately
		CreatedAt:    pod.CreatedAt,
	}
}
