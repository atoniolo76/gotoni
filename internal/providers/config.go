package providers

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
)

// GPUConfig represents GPU requirements
type GPUConfig struct {
	MinVRAM_GB      int      `yaml:"min_vram_gb" json:"min_vram_gb"`
	MinCUDAVersion  string   `yaml:"min_cuda_version" json:"min_cuda_version"`
	PreferredGPUs   []string `yaml:"preferred_gpus,omitempty" json:"preferred_gpus,omitempty"`
	ExcludeGPUs     []string `yaml:"exclude_gpus,omitempty" json:"exclude_gpus,omitempty"`
}

// ContainerRequirements represents container requirements for config
type ContainerRequirements struct {
	Image         string            `yaml:"image" json:"image"`
	Environment   map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	Ports         []string          `yaml:"ports,omitempty" json:"ports,omitempty"`
	Command       []string          `yaml:"command,omitempty" json:"command,omitempty"`
	Args          []string          `yaml:"args,omitempty" json:"args,omitempty"`
}

// InstanceConfig represents complete instance requirements
type InstanceConfig struct {
	Name       string                 `yaml:"name" json:"name"`
	GPU        GPUConfig              `yaml:"gpu" json:"gpu"`
	Container  ContainerRequirements  `yaml:"container" json:"container"`
	Provider   string                 `yaml:"provider,omitempty" json:"provider,omitempty"` // "lambdalabs" or "runpod"
	Region     string                 `yaml:"region,omitempty" json:"region,omitempty"`
	MaxPrice   int                    `yaml:"max_price_cents_hour,omitempty" json:"max_price_cents_hour,omitempty"`
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

	var candidates []*InstanceType

	for _, instanceType := range instanceTypes {
		// Check GPU compatibility
		if len(instanceType.GPUs) > 0 {
			gpu := instanceType.GPUs[0] // Assume homogeneous GPUs for now

			// Check VRAM requirement
			if gpu.VRAM_GB < c.GPU.MinVRAM_GB {
				continue
			}

			// Check CUDA compatibility using the new lookup function
			if c.GPU.MinCUDAVersion != "" {
				compatibleGPUs := GetCompatibleGPUs(c.GPU.MinCUDAVersion)
				gpuCompatible := false
				for _, compatibleGPU := range compatibleGPUs {
					if compatibleGPU.Name == gpu.Name {
						gpuCompatible = true
						break
					}
				}
				if !gpuCompatible {
					continue
				}
			}

			// Check price limit
			if c.MaxPrice > 0 && instanceType.PriceCentsPerHour > c.MaxPrice {
				continue
			}

			// Check region availability
			if c.Region != "" {
				regionAvailable := false
				for _, region := range instanceType.Regions {
					if region == c.Region {
						regionAvailable = true
						break
					}
				}
				if !regionAvailable {
					continue
				}
			}

			candidates = append(candidates, instanceType)
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no compatible instance types found for requirements: VRAM>=%dGB, CUDA>=%s",
			c.GPU.MinVRAM_GB, c.GPU.MinCUDAVersion)
	}

	// Return the cheapest option
	cheapest := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.PriceCentsPerHour < cheapest.PriceCentsPerHour {
			cheapest = candidate
		}
	}

	return cheapest, nil
}
