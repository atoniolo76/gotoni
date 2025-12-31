/*
Copyright © 2025 ALESSIO TONIOLO
*/
package cmd

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/atoniolo76/gotoni/pkg/remote"

	"github.com/spf13/cobra"
)

// gpuCmd represents the gpu command
var gpuCmd = &cobra.Command{
	Use:   "gpu [instance-id] --log <file.csv>",
	Short: "Track GPU memory usage over time",
	Long: `Track GPU memory usage over time and log to CSV file.
When --log is provided, continuously monitors and logs memory usage.
Use the included plot.py script to visualize the CSV data.`,
	Run: func(cmd *cobra.Command, args []string) {
		apiToken, err := cmd.Flags().GetString("api-token")
		if err != nil {
			log.Fatalf("Error getting API token: %v", err)
		}

		if apiToken == "" {
			apiToken = remote.GetAPIToken()
			if apiToken == "" {
				log.Fatal("API token not provided via --api-token flag or LAMBDA_API_KEY environment variable")
			}
		}

		interval, err := cmd.Flags().GetFloat64("interval")
		if err != nil {
			log.Fatalf("Error getting interval flag: %v", err)
		}

		logFile, err := cmd.Flags().GetString("log")
		if err != nil {
			log.Fatalf("Error getting log flag: %v", err)
		}

		if logFile == "" {
			log.Fatal("--log flag is required. Example: gotoni gpu --log gpu-memory.csv")
		}

		var instanceID string
		if len(args) > 0 {
			instanceID = args[0]
		} else {
			httpClient := remote.NewHTTPClient()
			runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
			if err != nil {
				log.Fatalf("Failed to list running instances: %v", err)
			}
			if len(runningInstances) == 0 {
				log.Fatal("No running instances found. Please provide an instance ID or launch an instance first.")
			}
			instanceID = runningInstances[0].ID
			fmt.Printf("Using instance: %s\n", instanceID)
		}

		httpClient := remote.NewHTTPClient()

		instanceDetails, err := remote.GetInstance(httpClient, apiToken, instanceID)
		if err != nil {
			log.Fatalf("Failed to get instance details: %v", err)
		}

		if instanceDetails.IP == "" {
			log.Fatalf("Instance IP address is empty. Instance status: %s. The instance may still be booting. Please wait a moment and try again, or check the instance status with 'gotoni list'.", instanceDetails.Status)
		}

		sshKeyFile, err := remote.GetSSHKeyForInstance(instanceID)
		if err != nil {
			log.Fatalf("Failed to get SSH key: %v", err)
		}

		manager := remote.NewSSHClientManager()
		defer manager.CloseAllConnections()

		fmt.Printf("Connecting to instance %s (%s)...\n", instanceID, instanceDetails.IP)
		if err := manager.ConnectToInstance(instanceDetails.IP, sshKeyFile); err != nil {
			log.Fatalf("Failed to connect via SSH: %v", err)
		}
		fmt.Printf("Connected! Logging to %s\n\n", logFile)

		trackGPU(manager, instanceDetails.IP, logFile, interval)
	},
}

func trackGPU(manager *remote.SSHClientManager, instanceIP string, logFile string, interval float64) {
	// Create directory if needed
	logDir := filepath.Dir(logFile)
	if logDir != "." && logDir != "" {
		if err := os.MkdirAll(logDir, 0755); err != nil {
			log.Fatalf("Failed to create log directory: %v", err)
		}
	}

	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header if file is new
	fileInfo, _ := file.Stat()
	if fileInfo.Size() == 0 {
		writer.Write([]string{"timestamp", "gpu_index", "memory_used_mb", "memory_total_mb", "memory_percent"})
		writer.Flush()
	}

	ticker := time.NewTicker(time.Duration(interval * float64(time.Second)))
	defer ticker.Stop()

	fmt.Printf("Tracking GPU memory (every %.1f seconds, press Ctrl+C to stop)...\n\n", interval)

	for range ticker.C {
		query := "nvidia-smi --query-gpu=index,memory.used,memory.total --format=csv,noheader"
		output, err := manager.ExecuteCommand(instanceIP, query)
		if err != nil {
			fmt.Printf("[ERROR] Failed to query GPU: %v\n", err)
			continue
		}

		lines := strings.Split(strings.TrimSpace(output), "\n")
		timestamp := time.Now().Format(time.RFC3339)

		for _, line := range lines {
			if line == "" {
				continue
			}
			fields := strings.Split(line, ", ")
			if len(fields) < 3 {
				continue
			}

			gpuIndex := strings.TrimSpace(fields[0])
			memUsedStr := strings.TrimSuffix(strings.TrimSpace(fields[1]), " MiB")
			memTotalStr := strings.TrimSuffix(strings.TrimSpace(fields[2]), " MiB")

			memUsed, _ := strconv.ParseFloat(memUsedStr, 64)
			memTotal, _ := strconv.ParseFloat(memTotalStr, 64)
			memPct := (memUsed / memTotal) * 100

			writer.Write([]string{
				timestamp,
				gpuIndex,
				strconv.FormatFloat(memUsed, 'f', 2, 64),
				strconv.FormatFloat(memTotal, 'f', 2, 64),
				strconv.FormatFloat(memPct, 'f', 2, 64),
			})
			writer.Flush()

			fmt.Printf("[%s] GPU %s: %.0f MB / %.0f MB (%.1f%%)\n",
				time.Now().Format("15:04:05"), gpuIndex, memUsed, memTotal, memPct)
		}
	}
}

func init() {
	rootCmd.AddCommand(gpuCmd)
	gpuCmd.Flags().StringP("api-token", "a", "", "API token for Lambda Cloud (can also be set via LAMBDA_API_KEY env var)")
	gpuCmd.Flags().Float64P("interval", "i", 0.5, "Logging interval in seconds (default: 0.5)")
	gpuCmd.Flags().StringP("log", "l", "", "CSV file path to log memory data (required)")
	gpuCmd.MarkFlagRequired("log")
}
