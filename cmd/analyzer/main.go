package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"toni/gpusnapshot/internal/providers"
)

func main() {
	var (
		configFile = flag.String("config", "", "Input config file to analyze (if not provided, generates example)")
		outputFile = flag.String("output", "", "Output file for analysis (optional)")
		provider   = flag.String("provider", "lambdalabs", "Target provider (lambdalabs or runpod)")
	)
	flag.Parse()

	if *configFile == "" {
		// Generate example config
		fmt.Printf("📝 No config file provided. Generating example config...\n")
		exampleConfig := &providers.InstanceConfig{
			Name:     "example-ml-instance",
			Provider: *provider,
			GPU: providers.GPUConfig{
				MinVRAM_GB:     24,
				MinCUDAVersion: "11.8",
			},
			Container: providers.ContainerRequirements{
				Image: "nvidia/cuda:11.8-devel-ubuntu22.04",
				Ports: []string{"22/tcp", "8888/http"},
				Environment: map[string]string{
					"CUDA_VISIBLE_DEVICES": "0",
				},
			},
		}

		output := "example-config.yaml"
		if *outputFile != "" {
			output = *outputFile
		}

		if err := exampleConfig.SaveConfig(output); err != nil {
			log.Fatalf("❌ Failed to save example config: %v", err)
		}

		fmt.Printf("✅ Example config saved to: %s\n", output)
		fmt.Printf("📝 Edit this file with your requirements and run again with --config %s\n", output)
		return
	}

	// Load and analyze config
	fmt.Printf("🔍 Analyzing config: %s\n", *configFile)

	config, err := providers.LoadConfig(*configFile)
	if err != nil {
		log.Fatalf("❌ Failed to load config: %v", err)
	}

	// Display config analysis
	fmt.Printf("\n📊 Config Analysis:\n")
	fmt.Printf("   Name: %s\n", config.Name)
	fmt.Printf("   Provider: %s\n", config.Provider)
	if config.Region != "" {
		fmt.Printf("   Region: %s\n", config.Region)
	}
	fmt.Printf("   Min VRAM: %d GB\n", config.GPU.MinVRAM_GB)
	fmt.Printf("   Min CUDA: %s\n", config.GPU.MinCUDAVersion)

	// Find compatible GPUs for the CUDA version
	if config.GPU.MinCUDAVersion != "" {
		compatibleGPUs := providers.GetCompatibleGPUs(config.GPU.MinCUDAVersion)
		fmt.Printf("   Compatible GPUs: %d types\n", len(compatibleGPUs))

		if len(compatibleGPUs) > 0 {
			// Show top 5 compatible GPUs
			fmt.Printf("   Top Compatible GPUs:\n")
			for i, gpu := range compatibleGPUs {
				if i >= 5 {
					break
				}
				fmt.Printf("     - %s (%s, %d GB VRAM)\n", gpu.Name, gpu.Description, gpu.VRAM_GB)
			}
		}
	}

	if config.Container.Image != "" {
		fmt.Printf("   Container Image: %s\n", config.Container.Image)
	}

	// Try to get provider for instance recommendation (optional)
	var p providers.Provider
	var hasProvider bool

	switch *provider {
	case "lambdalabs":
		if token := os.Getenv("LAMBDA_API_TOKEN"); token != "" {
			var err error
			p, err = providers.NewProvider("lambdalabs", token)
			if err == nil {
				hasProvider = true
			}
		}
	case "runpod":
		if token := os.Getenv("RUNPOD_API_TOKEN"); token != "" {
			var err error
			p, err = providers.NewProvider("runpod", token)
			if err == nil {
				hasProvider = true
			}
		}
	}

	if hasProvider && p != nil {
		recommended, recommendErr := config.GetRecommendedInstanceType(p)
		if recommendErr != nil {
			fmt.Printf("⚠️  Could not find recommended instance: %v\n", recommendErr)
		} else {
			fmt.Printf("\n💡 Recommended Instance: %s\n", recommended.Name)
			fmt.Printf("   Price: $%d/hour\n", recommended.PriceCentsPerHour/100)
			fmt.Printf("   GPU: %s (%d GB VRAM)\n", recommended.GPUs[0].Description, recommended.GPUs[0].VRAM_GB)
			fmt.Printf("   CPU: %d vCPUs\n", recommended.VCPUs)
			fmt.Printf("   RAM: %d GB\n", recommended.RAM_GB)
		}
	} else {
		fmt.Printf("\n💡 To get instance recommendations, set %s_API_TOKEN\n",
			strings.ToUpper(*provider))
	}

	fmt.Printf("\n✅ Config analysis complete!\n")
	fmt.Printf("🚀 Ready to launch with: gpusnapshot launch --config %s\n", *configFile)
}
