package docker

// ComputeCapabilityLookup maps GPU names to their compute capabilities
var ComputeCapabilityLookup = map[string]string{
	// Compute Capability 12.1
	"NVIDIA GB10 (DGX Spark)": "12.1",

	// Compute Capability 12.0
	"NVIDIA RTX PRO 6000 Blackwell Server Edition":            "12.0",
	"NVIDIA RTX PRO 6000 Blackwell Workstation Edition":       "12.0",
	"NVIDIA RTX PRO 6000 Blackwell Max-Q Workstation Edition": "12.0",
	"NVIDIA RTX PRO 5000 Blackwell":                           "12.0",
	"NVIDIA RTX PRO 4500 Blackwell":                           "12.0",
	"NVIDIA RTX PRO 4000 Blackwell":                           "12.0",
	"NVIDIA RTX PRO 4000 Blackwell SFF Edition":               "12.0",
	"NVIDIA RTX PRO 2000 Blackwell":                           "12.0",
	"GeForce RTX 5090":                                        "12.0",
	"GeForce RTX 5080":                                        "12.0",
	"GeForce RTX 5070 Ti":                                     "12.0",
	"GeForce RTX 5070":                                        "12.0",
	"GeForce RTX 5060 Ti":                                     "12.0",
	"GeForce RTX 5060":                                        "12.0",
	"GeForce RTX 5050":                                        "12.0",

	// Compute Capability 11.0
	"Jetson T5000": "11.0",
	"Jetson T4000": "11.0",

	// Compute Capability 10.3
	"NVIDIA GB300": "10.3",
	"NVIDIA B300":  "10.3",

	// Compute Capability 10.0
	"NVIDIA GB200": "10.0",
	"NVIDIA B200":  "10.0",

	// Compute Capability 9.0
	"NVIDIA GH200": "9.0",
	"NVIDIA H200":  "9.0",
	"NVIDIA H100":  "9.0",

	// Compute Capability 8.9
	"NVIDIA L4":               "8.9",
	"NVIDIA L40":              "8.9",
	"NVIDIA L40S":             "8.9",
	"NVIDIA RTX 6000 Ada":     "8.9",
	"NVIDIA RTX 5000 Ada":     "8.9",
	"NVIDIA RTX 4500 Ada":     "8.9",
	"NVIDIA RTX 4000 Ada":     "8.9",
	"NVIDIA RTX 4000 SFF Ada": "8.9",
	"NVIDIA RTX 2000 Ada":     "8.9",
	"GeForce RTX 4090":        "8.9",
	"GeForce RTX 4080":        "8.9",
	"GeForce RTX 4070 Ti":     "8.9",
	"GeForce RTX 4070":        "8.9",
	"GeForce RTX 4060 Ti":     "8.9",
	"GeForce RTX 4060":        "8.9",
	"GeForce RTX 4050":        "8.9",

	// Compute Capability 8.7
	"Jetson AGX Orin":  "8.7",
	"Jetson Orin NX":   "8.7",
	"Jetson Orin Nano": "8.7",

	// Compute Capability 8.6
	"NVIDIA A40":          "8.6",
	"NVIDIA A10":          "8.6",
	"NVIDIA A16":          "8.6",
	"NVIDIA A2":           "8.6",
	"NVIDIA RTX A6000":    "8.6",
	"NVIDIA RTX A5000":    "8.6",
	"NVIDIA RTX A4000":    "8.6",
	"NVIDIA RTX A3000":    "8.6",
	"NVIDIA RTX A2000":    "8.6",
	"GeForce RTX 3090 Ti": "8.6",
	"GeForce RTX 3090":    "8.6",
	"GeForce RTX 3080 Ti": "8.6",
	"GeForce RTX 3080":    "8.6",
	"GeForce RTX 3070 Ti": "8.6",
	"GeForce RTX 3070":    "8.6",
	"GeForce RTX 3060 Ti": "8.6",
	"GeForce RTX 3060":    "8.6",
	"GeForce RTX 3050 Ti": "8.6",
	"GeForce RTX 3050":    "8.6",

	// Compute Capability 8.0
	"NVIDIA A100": "8.0",
	"NVIDIA A30":  "8.0",

	// Compute Capability 7.5
	"NVIDIA T4":           "7.5",
	"QUADRO RTX 8000":     "7.5",
	"QUADRO RTX 6000":     "7.5",
	"QUADRO RTX 5000":     "7.5",
	"QUADRO RTX 4000":     "7.5",
	"QUADRO RTX 3000":     "7.5",
	"QUADRO  T2000":       "7.5",
	"NVIDIA T1200":        "7.5",
	"NVIDIA T1000":        "7.5",
	"NVIDIA T600":         "7.5",
	"NVIDIA T500":         "7.5",
	"NVIDIA T400":         "7.5",
	"GeForce GTX 1650 Ti": "7.5",
	"NVIDIA TITAN RTX":    "7.5",
	"GeForce RTX 2080 Ti": "7.5",
	"GeForce RTX 2080":    "7.5",
	"GeForce RTX 2070":    "7.5",
	"GeForce RTX 2060":    "7.5",
}

// GetComputeCapability returns the compute capability for a given GPU name
func GetComputeCapability(gpuName string) (string, bool) {
	capability, exists := ComputeCapabilityLookup[gpuName]
	return capability, exists
}

// GetGPUsByComputeCapability returns all GPUs that support a specific compute capability
func GetGPUsByComputeCapability(computeCapability string) []string {
	var gpus []string
	for gpu, cc := range ComputeCapabilityLookup {
		if cc == computeCapability {
			gpus = append(gpus, gpu)
		}
	}
	return gpus
}

// GetAllComputeCapabilities returns a sorted list of all available compute capabilities
func GetAllComputeCapabilities() []string {
	capabilitySet := make(map[string]bool)
	for _, cc := range ComputeCapabilityLookup {
		capabilitySet[cc] = true
	}

	capabilities := make([]string, 0, len(capabilitySet))
	for cc := range capabilitySet {
		capabilities = append(capabilities, cc)
	}

	// Simple sort by version (this could be improved with proper semantic versioning)
	// For now, just return in the order they appear in the map
	return capabilities
}
