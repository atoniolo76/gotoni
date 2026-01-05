/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/atoniolo76/gotoni/pkg/docker"
	"github.com/spf13/cobra"
)

// checkCmd represents the check command
var checkCmd = &cobra.Command{
	Use:   "check [dockerfile]",
	Short: "Parse and analyze a Dockerfile",
	Long: `Parse a Dockerfile and extract key information including base image, CUDA version, and architecture.

Examples:
  gotoni check Dockerfile
  gotoni check ./path/to/Dockerfile
  gotoni check Dockerfile.prod`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		dockerfilePath := args[0]

		// Check if file exists
		if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
			log.Fatalf("Dockerfile not found: %s", dockerfilePath)
		}

		// Read the dockerfile
		content, err := os.ReadFile(dockerfilePath)
		if err != nil {
			log.Fatalf("Error reading dockerfile: %v", err)
		}

		// Parse the dockerfile
		parsedDockerfile, err := docker.ParseDockerfile(string(content))
		if err != nil {
			log.Fatalf("Error parsing dockerfile: %v", err)
		}

		// Print the parsed information
		fmt.Printf("Dockerfile Analysis: %s\n", dockerfilePath)
		fmt.Println("=" + strings.Repeat("=", 40))

		fmt.Printf("Base Image: %s\n", parsedDockerfile.BaseImage)
		fmt.Printf("Architecture: %s\n", parsedDockerfile.Arch)

		if parsedDockerfile.CUDAVersion != "" {
			fmt.Printf("CUDA Version: %s\n", parsedDockerfile.CUDAVersion)
		} else {
			fmt.Println("CUDA Version: Not detected")
		}

		// if parsedDockerfile.Model != "" {
		// 	fmt.Printf("Model: %s\n", parsedDockerfile.Model)
		// } else {
		// 	fmt.Println("Model: Not specified")
		// }
	},
}

func init() {
	rootCmd.AddCommand(checkCmd)
}
