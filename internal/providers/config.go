package providers

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
)

// GPUConfig represents GPU requirements
type GPUConfig struct {
	MinVRAM_GB      int      `yaml:"min_vram_gb"`
	MinCUDAVersion  string   `yaml:"min_cuda_version"`
	PreferredGPUs   []string `yaml:"preferred_gpus,omitempty"`
	ExcludeGPUs     []string `yaml:"exclude_gpus,omitempty"`
}

// ContainerRequirements represents container requirements for config
type ContainerRequirements struct {
	Image       string            `yaml:"image"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Ports       []string          `yaml:"ports,omitempty"`
	Command     []string          `yaml:"command,omitempty"`
	Args        []string          `yaml:"args,omitempty"`
}

// InstanceConfig represents complete instance requirements
type InstanceConfig struct {
	Name      string                `yaml:"name"`
	GPU       GPUConfig             `yaml:"gpu"`
	Container ContainerRequirements `yaml:"container"`
	Provider  string                `yaml:"provider,omitempty"`
	Region    string                `yaml:"region,omitempty"`
	MaxPrice  int                   `yaml:"max_price_cents_hour,omitempty"`
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

	return &config, nil
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
func (c *InstanceConfig) GetRecommendedInstanceType(provider Provider) (*InstanceType, error) {
	instanceTypes, err := provider.ListInstanceTypes()
	if err != nil {
		return nil, fmt.Errorf("failed to list instance types: %w", err)
	}

	// Pre-compute compatible GPU names for fast lookup
	var compatibleGPUNames map[string]bool
	if c.GPU.MinCUDAVersion != "" {
		compatibleGPUs := GetCompatibleGPUs(c.GPU.MinCUDAVersion)
		compatibleGPUNames = make(map[string]bool, len(compatibleGPUs))
		for _, gpu := range compatibleGPUs {
			compatibleGPUNames[gpu.Name] = true
		}
	}

	// Pre-compute region set for fast lookup
	var requiredRegions map[string]bool
	if c.Region != "" {
		requiredRegions = map[string]bool{c.Region: true}
	}

	var candidates []*InstanceType
	minPrice := int(^uint(0) >> 1) // Max int value
	var cheapest *InstanceType

	for _, instanceType := range instanceTypes {
		// Skip if no GPUs
		if len(instanceType.GPUs) == 0 {
			continue
		}

		gpu := instanceType.GPUs[0] // Assume homogeneous GPUs

		// Check VRAM requirement
		if gpu.VRAM_GB < c.GPU.MinVRAM_GB {
			continue
		}

		// Check CUDA compatibility (fast map lookup)
		if compatibleGPUNames != nil && !compatibleGPUNames[gpu.Name] {
			continue
		}

		// Check price limit
		if c.MaxPrice > 0 && instanceType.PriceCentsPerHour > c.MaxPrice {
			continue
		}

		// Check region availability (fast map lookup)
		if requiredRegions != nil {
			regionFound := false
			for _, region := range instanceType.Regions {
				if requiredRegions[region] {
					regionFound = true
					break
				}
			}
			if !regionFound {
				continue
			}
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
			c.GPU.MinVRAM_GB, c.GPU.MinCUDAVersion)
	}

	return cheapest, nil
}
