package providers

import (
	"fmt"
	"strconv"
	"strings"
)

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
