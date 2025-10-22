package providers

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"

	"toni/gotoni/pkg/lambdalabs"
)

// InstanceConfig represents instance requirements for inference workloads
type InstanceConfig struct {
	Name             string `yaml:"name"`                        // Instance name
	Image            string `yaml:"image"`                       // Container image to deploy (works with k3s/containerd)
	MinVRAM_GB       int    `yaml:"min_vram_gb"`                // Minimum GPU VRAM needed (critical for model fit)
	MinCUDAVersion   string `yaml:"min_cuda_version"`           // Minimum CUDA version required
}

// LoadConfig loads configuration from a YAML file
func LoadConfig(filename string) (*InstanceConfig, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config InstanceConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Validate required fields
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &config, nil
}

// Validate checks that required fields are provided and valid
func (c *InstanceConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("name is required")
	}
	if c.MinVRAM_GB <= 0 {
		return fmt.Errorf("min_vram_gb must be > 0")
	}
	if c.MinCUDAVersion == "" {
		return fmt.Errorf("min_cuda_version is required")
	}
	if c.Image == "" {
		return fmt.Errorf("image is required")
	}
	return nil
}

// GetDefaultPorts returns the default ports for inference services
func (c *InstanceConfig) GetDefaultPorts() []string {
	return []string{"8080/tcp"}
}

// SaveConfig saves configuration to a YAML file
func (c *InstanceConfig) SaveConfig(filename string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}


// GetRecommendedInstanceType returns the best instance type for this config
func (c *InstanceConfig) GetRecommendedInstanceType(instanceTypes []*lambdalabs.InstanceType) (*lambdalabs.InstanceType, error) {
	// Pre-compute compatible GPU names for fast lookup
	var compatibleGPUNames map[string]bool
	if c.MinCUDAVersion != "" {
		compatibleGPUs := GetCompatibleGPUs(c.MinCUDAVersion)
		compatibleGPUNames = make(map[string]bool, len(compatibleGPUs))
		for _, gpu := range compatibleGPUs {
			compatibleGPUNames[gpu.Name] = true
		}
	}

	var candidates []*lambdalabs.InstanceType
	minPrice := int(^uint(0) >> 1) // Max int value
	var cheapest *lambdalabs.InstanceType

	for _, instanceType := range instanceTypes {
		// Skip if no GPUs
		if len(instanceType.GPUs) == 0 {
			continue
		}

		gpu := instanceType.GPUs[0] // Assume homogeneous GPUs

		// Check VRAM requirement
		if gpu.VRAM_GB < c.MinVRAM_GB {
			continue
		}

		// Check CUDA compatibility (fast map lookup)
		if compatibleGPUNames != nil && !compatibleGPUNames[gpu.Name] {
			continue
		}

		candidates = append(candidates, instanceType)

		// Track cheapest during iteration
		if instanceType.PriceCentsPerHour < minPrice {
			minPrice = instanceType.PriceCentsPerHour
			cheapest = instanceType
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no compatible instance types found for requirements: VRAM>=%dGB, CUDA>=%s",
			c.MinVRAM_GB, c.MinCUDAVersion)
	}

	return cheapest, nil
}
