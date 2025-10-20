package providers

import (
	"fmt"
	"strconv"
	"strings"
)

// Common GPU specifications based on actual cloud provider offerings
// These represent the Nvidia GPU models available on Lambda Labs
var (
	// H100 Series - Latest high-end GPUs
	H100_80GB_HBM3 = GPUType{
		Name:             "H100_80GB_HBM3",
		Description:      "H100 (80 GB HBM3)",
		VRAM_GB:          80,
		CUDACores:        14592,
		ComputeCapability: "9.0",
		BaseClock_MHz:    1065,
		MemoryClock_MHz:  1593,
		FP32_TFLOPS:      26.9,
	}

	H100_80GB_SXM5 = GPUType{
		Name:             "H100_80GB_SXM5",
		Description:      "H100 (80 GB SXM5)",
		VRAM_GB:          80,
		CUDACores:        14592,
		ComputeCapability: "9.0",
		BaseClock_MHz:    1065,
		MemoryClock_MHz:  1593,
		FP32_TFLOPS:      26.9,
	}

	H100_NVL = GPUType{
		Name:             "H100_NVL",
		Description:      "H100 NVL",
		VRAM_GB:          94,
		CUDACores:        16896,
		ComputeCapability: "9.0",
		BaseClock_MHz:    1065,
		MemoryClock_MHz:  1593,
		FP32_TFLOPS:      32.6,
	}

	// A100 Series - Previous generation high-end
	A100_40GB_PCIE = GPUType{
		Name:             "A100_40GB_PCIE",
		Description:      "A100 (40 GB PCIe)",
		VRAM_GB:          40,
		CUDACores:        6912,
		ComputeCapability: "8.0",
		BaseClock_MHz:    765,
		MemoryClock_MHz:  1215,
		FP32_TFLOPS:      9.7,
	}

	A100_80GB_PCIE = GPUType{
		Name:             "A100_80GB_PCIE",
		Description:      "A100 (80 GB PCIe)",
		VRAM_GB:          80,
		CUDACores:        6912,
		ComputeCapability: "8.0",
		BaseClock_MHz:    765,
		MemoryClock_MHz:  1215,
		FP32_TFLOPS:      9.7,
	}

	A100_80GB_SXM4 = GPUType{
		Name:             "A100_80GB_SXM4",
		Description:      "A100 (80 GB SXM4)",
		VRAM_GB:          80,
		CUDACores:        6912,
		ComputeCapability: "8.0",
		BaseClock_MHz:    765,
		MemoryClock_MHz:  1215,
		FP32_TFLOPS:      9.7,
	}

	// Ampere Series - Professional GPUs
	A6000 = GPUType{
		Name:             "A6000",
		Description:      "RTX A6000 (48 GB)",
		VRAM_GB:          48,
		CUDACores:        10752,
		ComputeCapability: "8.6",
		BaseClock_MHz:    1410,
		MemoryClock_MHz:  16000,
		FP32_TFLOPS:      38.7,
	}

	A5000 = GPUType{
		Name:             "A5000",
		Description:      "RTX A5000 (24 GB)",
		VRAM_GB:          24,
		CUDACores:        8192,
		ComputeCapability: "8.6",
		BaseClock_MHz:    1170,
		MemoryClock_MHz:  14000,
		FP32_TFLOPS:      27.8,
	}

	A4000 = GPUType{
		Name:             "A4000",
		Description:      "RTX A4000 (16 GB)",
		VRAM_GB:          16,
		CUDACores:        6144,
		ComputeCapability: "8.6",
		BaseClock_MHz:    735,
		MemoryClock_MHz:  14000,
		FP32_TFLOPS:      19.2,
	}

	// Ada Lovelace Professional GPUs
	RTX_6000_ADA = GPUType{
		Name:             "RTX_6000_ADA",
		Description:      "RTX 6000 Ada (48 GB)",
		VRAM_GB:          48,
		CUDACores:        18176,
		ComputeCapability: "8.9",
		BaseClock_MHz:    2505,
		MemoryClock_MHz:  20000,
		FP32_TFLOPS:      91.1,
	}

	RTX_5000_ADA = GPUType{
		Name:             "RTX_5000_ADA",
		Description:      "RTX 5000 Ada (32 GB)",
		VRAM_GB:          32,
		CUDACores:        12800,
		ComputeCapability: "8.9",
		BaseClock_MHz:    2550,
		MemoryClock_MHz:  20000,
		FP32_TFLOPS:      65.3,
	}

	RTX_4000_ADA = GPUType{
		Name:             "RTX_4000_ADA",
		Description:      "RTX 4000 Ada (20 GB)",
		VRAM_GB:          20,
		CUDACores:        7424,
		ComputeCapability: "8.9",
		BaseClock_MHz:    2175,
		MemoryClock_MHz:  18000,
		FP32_TFLOPS:      32.3,
	}


	// Previous generation professional GPUs
	RTX_6000 = GPUType{
		Name:             "RTX_6000",
		Description:      "RTX 6000 (24 GB)",
		VRAM_GB:          24,
		CUDACores:        4608,
		ComputeCapability: "7.5",
		BaseClock_MHz:    1440,
		MemoryClock_MHz:  14000,
		FP32_TFLOPS:      16.3,
	}

	// A10 - Ampere architecture
	A10 = GPUType{
		Name:             "A10",
		Description:      "A10 (24 GB)",
		VRAM_GB:          24,
		CUDACores:        9216,
		ComputeCapability: "8.6",
		BaseClock_MHz:    765,
		MemoryClock_MHz:  1563,
		FP32_TFLOPS:      31.2,
	}

	// L40 Series - Latest professional GPUs
	L40 = GPUType{
		Name:             "L40",
		Description:      "L40 (48 GB)",
		VRAM_GB:          48,
		CUDACores:        18176,
		ComputeCapability: "8.9",
		BaseClock_MHz:    735,
		MemoryClock_MHz:  18000,
		FP32_TFLOPS:      90.5,
	}

	L40S = GPUType{
		Name:             "L40S",
		Description:      "L40S (48 GB)",
		VRAM_GB:          48,
		CUDACores:        18176,
		ComputeCapability: "8.9",
		BaseClock_MHz:    735,
		MemoryClock_MHz:  18000,
		FP32_TFLOPS:      90.5,
	}

	// B200 - Blackwell architecture
	B200 = GPUType{
		Name:             "B200",
		Description:      "B200 (180 GB)",
		VRAM_GB:          180,
		CUDACores:        36864,
		ComputeCapability: "9.0",
		BaseClock_MHz:    1500, // Estimated
		MemoryClock_MHz:  2000, // Estimated
		FP32_TFLOPS:      150.0, // Estimated
	}

	// GH200 - Grace Hopper
	GH200 = GPUType{
		Name:             "GH200",
		Description:      "GH200 (96 GB)",
		VRAM_GB:          96,
		CUDACores:        22016, // 8x H100 dies
		ComputeCapability: "9.0",
		BaseClock_MHz:    1980, // CPU cores
		MemoryClock_MHz:  1593,
		FP32_TFLOPS:      197.0, // Estimated
	}

	// V100 - Previous generation
	V100_16GB = GPUType{
		Name:             "V100_16GB",
		Description:      "V100 (16 GB)",
		VRAM_GB:          16,
		CUDACores:        5120,
		ComputeCapability: "7.0",
		BaseClock_MHz:    1245,
		MemoryClock_MHz:  877,
		FP32_TFLOPS:      14.1,
	}

	V100_32GB = GPUType{
		Name:             "V100_32GB",
		Description:      "V100 (32 GB)",
		VRAM_GB:          32,
		CUDACores:        5120,
		ComputeCapability: "7.0",
		BaseClock_MHz:    1245,
		MemoryClock_MHz:  877,
		FP32_TFLOPS:      14.1,
	}
)

// GetGPUTypeByName returns a GPUType by its name identifier
func GetGPUTypeByName(name string) (GPUType, bool) {
	gpuTypes := map[string]GPUType{
		// H100 Series
		"H100_80GB_HBM3": H100_80GB_HBM3,
		"H100_80GB_SXM5": H100_80GB_SXM5,
		"H100_NVL":       H100_NVL,

		// A100 Series
		"A100_40GB_PCIE": A100_40GB_PCIE,
		"A100_80GB_PCIE": A100_80GB_PCIE,
		"A100_80GB_SXM4": A100_80GB_SXM4,

		// Ampere Professional
		"A6000": A6000,
		"A5000": A5000,
		"A4000": A4000,
		"A10":   A10,

		// Ada Lovelace Professional
		"RTX_6000_ADA": RTX_6000_ADA,
		"RTX_5000_ADA": RTX_5000_ADA,
		"RTX_4000_ADA": RTX_4000_ADA,


		// Previous generation professional
		"RTX_6000": RTX_6000,

		// L40 Series
		"L40":  L40,
		"L40S": L40S,

		// Latest Blackwell
		"B200":  B200,
		"GH200": GH200,

		// Legacy V100
		"V100_16GB": V100_16GB,
		"V100_32GB": V100_32GB,
	}

	gpu, exists := gpuTypes[name]
	return gpu, exists
}

// GetMinCUDAVersion returns the minimum CUDA version required for a GPU
// Based on NVIDIA CUDA compatibility documentation
func (g GPUType) GetMinCUDAVersion() string {
	switch g.ComputeCapability {
	case "9.0":  // Hopper architecture (H100, B200)
		return "12.0"
	case "8.9":  // Ada Lovelace (RTX 40-series, RTX 6000 Ada)
		return "11.8"
	case "8.6":  // Ampere RTX series (A6000, RTX 30-series)
		return "11.0"
	case "8.0":  // Ampere (A100, A6000)
		return "11.0"
	case "7.5":  // Turing (RTX 20-series, T4)
		return "10.0"
	case "7.2":  // Jetson AGX Xavier
		return "10.0"
	case "7.0":  // Volta (V100)
		return "9.0"
	case "6.2":  // Jetson TX2
		return "8.0"
	case "6.1":  // Pascal (P100, GTX 10-series)
		return "8.0"
	case "6.0":  // Pascal (P100)
		return "8.0"
	case "5.3":  // Jetson Nano
		return "8.0"
	case "5.2":  // Maxwell (M60, M40)
		return "8.0"
	default:
		return "8.0" // Conservative default for older GPUs
	}
}

// GetCompatibleGPUs returns all GPUs compatible with a given CUDA version
// Uses a lookup table organized by minimum required CUDA version
func GetCompatibleGPUs(cudaVersion string) []GPUType {
	if cudaVersion == "" {
		return []GPUType{}
	}

	version, err := parseVersion(cudaVersion)
	if err != nil {
		return []GPUType{}
	}

	// GPUs organized by minimum CUDA version requirement
	gpuByMinVersion := map[string][]GPUType{
		"8.0":  {V100_16GB, V100_32GB, RTX_6000},
		"11.0": {A100_40GB_PCIE, A100_80GB_PCIE, A100_80GB_SXM4, A6000, A5000, A4000, A10},
		"11.8": {RTX_6000_ADA, RTX_5000_ADA, RTX_4000_ADA},
		"12.0": {H100_80GB_HBM3, H100_80GB_SXM5, H100_NVL, B200},
	}

	var compatible []GPUType
	for minVersion, gpus := range gpuByMinVersion {
		minFloat, _ := parseVersion(minVersion)
		if version >= minFloat {
			compatible = append(compatible, gpus...)
		}
	}

	return compatible
}

// parseVersion converts version string like "11.8" to float 11.8
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
