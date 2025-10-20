package imageanalysis

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"toni/gpusnapshot/internal/providers"
)

// ImageAnalysisResult stores the extracted information from a Dockerfile
type ImageAnalysisResult struct {
	CUDAVersion    string
	CUDNNVersion   string
	BaseImage      string
	Libraries      []string
	EstimatedVRAM  int // GB
	EstimatedRAM   int // GB
	MinCUDAVersion string
	CompatibleGPUs []providers.GPUType
}

// AnalyzeDockerfile parses a Dockerfile content and extracts relevant information
func AnalyzeDockerfile(content string) (*ImageAnalysisResult, error) {
	analysis := &ImageAnalysisResult{
		Libraries: []string{},
	}

	scanner := bufio.NewScanner(strings.NewReader(content))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Extract CUDA version from FROM statement
		if strings.HasPrefix(strings.ToUpper(line), "FROM") {
			cudaVersion := extractCUDAFromImage(line)
			if cudaVersion != "" {
				analysis.CUDAVersion = cudaVersion
				analysis.BaseImage = line
			}
		}

		// Extract libraries from RUN commands (pip installs, etc.)
		if strings.HasPrefix(strings.ToUpper(line), "RUN") {
			libs := extractLibraries(line)
			analysis.Libraries = append(analysis.Libraries, libs...)
		}

		// Extract environment variables
		if strings.HasPrefix(strings.ToUpper(line), "ENV") {
			analysis.extractEnvironmentVars(line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading dockerfile: %w", err)
	}

	// Analyze requirements and compatible GPUs
	analysis.analyzeRequirements()

	return analysis, nil
}

// extractCUDAFromImage extracts CUDA version from Docker image name
func extractCUDAFromImage(fromLine string) string {
	// Simple manual extraction - more reliable than complex regex
	if strings.Contains(fromLine, "cuda") {
		// Split by colon to get the part after "nvidia/cuda:X.Y.Z"
		parts := strings.Split(fromLine, ":")
		if len(parts) > 1 {
			versionPart := parts[1]
			// Split by dash to isolate version from rest
			versionParts := strings.Split(versionPart, "-")
			if len(versionParts) > 0 {
				version := versionParts[0]
				// Take only major.minor (e.g., 11.3 from 11.3.0)
				vParts := strings.Split(version, ".")
				if len(vParts) >= 2 {
					return fmt.Sprintf("%s.%s", vParts[0], vParts[1])
				}
			}
		}
	}

	// Also check for cu118 style versions (PyTorch style)
	re := regexp.MustCompile(`cu(\d+)`)
	matches := re.FindStringSubmatch(fromLine)
	if len(matches) > 1 {
		cuVersion := matches[1]
		if len(cuVersion) == 3 {
			// cu118 -> 11.8
			major := cuVersion[:2]
			minor := cuVersion[2:]
			return fmt.Sprintf("%s.%s", major, minor)
		}
	}

	return ""
}

// extractLibraries extracts library names from RUN commands
func extractLibraries(runLine string) []string {
	var libs []string

	// Common patterns
	patterns := []string{
		`pip install ([^&\n]+)`,
		`conda install ([^&\n]+)`,
		`apt-get install ([^&\n]+)`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllStringSubmatch(runLine, -1)
		for _, match := range matches {
			if len(match) > 1 {
				// Split by spaces and clean up
				parts := strings.Fields(match[1])
				for _, part := range parts {
					part = strings.Trim(part, " \t\n\r")
					if part != "" && !strings.HasPrefix(part, "-") {
						libs = append(libs, part)
					}
				}
			}
		}
	}

	return libs
}

// extractEnvironmentVars extracts CUDA/CuDNN versions from ENV statements
func (a *ImageAnalysisResult) extractEnvironmentVars(envLine string) {
	// Extract CUDA version from ENV
	cudaRe := regexp.MustCompile(`CUDA_VERSION[= ]\s*(\d+\.\d+)`)
	if matches := cudaRe.FindStringSubmatch(envLine); len(matches) > 1 {
		a.CUDAVersion = matches[1]
	}

	// Extract cuDNN version
	cudnnRe := regexp.MustCompile(`CUDNN_VERSION[= ]\s*(\d+)`)
	if matches := cudnnRe.FindStringSubmatch(envLine); len(matches) > 1 {
		a.CUDNNVersion = matches[1]
	}
}

// analyzeRequirements estimates resource needs and finds compatible GPUs
func (a *ImageAnalysisResult) analyzeRequirements() {
	// Estimate VRAM based on libraries
	a.estimateVRAM()

	// Determine minimum CUDA version
	a.determineMinCUDAVersion()

	// Find compatible GPUs
	a.findCompatibleGPUs()
}

// estimateVRAM estimates VRAM requirements based on installed libraries
func (a *ImageAnalysisResult) estimateVRAM() {
	vramGB := 4 // Base amount

	// Large ML libraries add VRAM requirements
	largeLibs := map[string]int{
		"torch":         8,  // PyTorch
		"tensorflow":    8,  // TensorFlow
		"pytorch3d":     4,  // 3D processing
		"kaolin":        6,  // NVIDIA Kaolin
		"nvdiffrast":    4,  // NVIDIA differentiable rasterizer
		"opencv":        2,  // Computer vision
		"detectron2":    6,  // Facebook Detectron
		"transformers":  4,  // Hugging Face transformers
		"diffusers":     4,  // Stable diffusion
		"accelerate":    2,  // Hugging Face accelerate
	}

	for _, lib := range a.Libraries {
		libLower := strings.ToLower(lib)
		for libName, vram := range largeLibs {
			if strings.Contains(libLower, libName) {
				vramGB += vram
				break
			}
		}
	}

	// CUDA version affects memory efficiency
	if cudaVer, err := strconv.ParseFloat(a.CUDAVersion, 64); err == nil {
		if cudaVer >= 12.0 {
			vramGB = int(float64(vramGB) * 0.9) // Newer CUDA is more memory efficient
		}
	}

	a.EstimatedVRAM = vramGB
	a.EstimatedRAM = vramGB * 2 // RAM is typically 2x VRAM for ML workloads
}

// determineMinCUDAVersion sets the minimum CUDA version required
func (a *ImageAnalysisResult) determineMinCUDAVersion() {
	if a.CUDAVersion != "" {
		a.MinCUDAVersion = a.CUDAVersion
		return
	}

	// Based on libraries, infer minimum CUDA version
	minVersion := "10.0" // Default

	// Libraries that require newer CUDA
	newCudaLibs := map[string]string{
		"kaolin":     "11.1", // NVIDIA Kaolin requires CUDA 11.1+
		"nvdiffrast": "11.1", // Diff rasterizer requires CUDA 11.1+
		"pytorch3d":  "10.2", // PyTorch3D requires CUDA 10.2+
	}

	for _, lib := range a.Libraries {
		libLower := strings.ToLower(lib)
		for libName, cudaReq := range newCudaLibs {
			if strings.Contains(libLower, libName) {
				if cudaReq > minVersion {
					minVersion = cudaReq
				}
				break
			}
		}
	}

	a.MinCUDAVersion = minVersion
}

// findCompatibleGPUs finds GPUs compatible with the CUDA version
func (a *ImageAnalysisResult) findCompatibleGPUs() {
	if a.MinCUDAVersion == "" {
		return
	}

	minCUDA, err := strconv.ParseFloat(a.MinCUDAVersion, 64)
	if err != nil {
		return
	}

	// Get all GPU types and filter by CUDA compatibility
	allGPUs := []providers.GPUType{
		providers.A100_40GB_PCIE, providers.A100_80GB_PCIE, providers.A100_80GB_SXM4,
		providers.H100_80GB_HBM3, providers.H100_80GB_SXM5, providers.H100_NVL,
		providers.A6000, providers.A5000, providers.A4000, providers.A10,
		providers.RTX_6000_ADA, providers.RTX_5000_ADA, providers.RTX_4000_ADA,
		providers.RTX_4090, providers.RTX_4080, providers.RTX_3090, providers.RTX_3080,
		providers.RTX_6000, providers.L40, providers.L40S, providers.B200, providers.GH200,
		providers.V100_16GB, providers.V100_32GB,
	}

	for _, gpu := range allGPUs {
		gpuMinCUDA := gpu.GetMinCUDAVersion()
		gpuMinFloat, err := strconv.ParseFloat(gpuMinCUDA, 64)
		if err != nil {
			continue
		}

		if minCUDA >= gpuMinFloat {
			a.CompatibleGPUs = append(a.CompatibleGPUs, gpu)
		}
	}

	// Sort by VRAM (prefer GPUs with more VRAM for ML workloads)
	// This is a simple sort - in practice you'd also consider price, availability, etc.
	for i := 0; i < len(a.CompatibleGPUs)-1; i++ {
		for j := i + 1; j < len(a.CompatibleGPUs); j++ {
			if a.CompatibleGPUs[i].VRAM_GB < a.CompatibleGPUs[j].VRAM_GB {
				a.CompatibleGPUs[i], a.CompatibleGPUs[j] = a.CompatibleGPUs[j], a.CompatibleGPUs[i]
			}
		}
	}
}

// GetRecommendedGPU returns the best GPU for this image analysis
func (a *ImageAnalysisResult) GetRecommendedGPU() *providers.GPUType {
	if len(a.CompatibleGPUs) == 0 {
		return nil
	}

	// Return GPU with enough VRAM, preferring higher VRAM
	for _, gpu := range a.CompatibleGPUs {
		if gpu.VRAM_GB >= a.EstimatedVRAM {
			return &gpu
		}
	}

	// If no GPU has enough VRAM, return the one with most VRAM
	return &a.CompatibleGPUs[0]
}
