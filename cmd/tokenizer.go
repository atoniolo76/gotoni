/*
Copyright © 2025 ALESSIO TONIOLO

tokenizer.go implements commands for managing the Rust tokenizer sidecar.
The sidecar provides fast, accurate token counting via Unix socket.
*/
package cmd

import (
	"fmt"
	"log"

	cluster "github.com/atoniolo76/gotoni/pkg/cluster"
	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/atoniolo76/gotoni/pkg/tokenizer"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

var tokenizerCmd = &cobra.Command{
	Use:   "tokenizer",
	Short: "Manage tokenizer sidecar on cluster instances",
	Long: `Tokenizer sidecar management commands.

The tokenizer sidecar is a Rust binary that provides fast, accurate token 
counting via Unix socket (/tmp/tokenizer.sock). This is used by the GORGO
policy for routing decisions.

Examples:
  # Setup (upload and build) tokenizer on all instances
  gotoni tokenizer setup

  # Start the tokenizer sidecar
  gotoni tokenizer start

  # Check status
  gotoni tokenizer status

  # Stop the sidecar
  gotoni tokenizer stop

  # Setup and start in one command
  gotoni tokenizer setup --start`,
}

var tokenizerSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Upload and build tokenizer sidecar on cluster instances",
	Long: `Upload the Rust tokenizer source code and build it on all cluster instances.

This installs Rust (if needed), uploads main.rs and Cargo.toml, and runs
cargo build --release to create the tokenizer-sidecar binary.`,
	Run: runTokenizerSetup,
}

var tokenizerStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start tokenizer sidecar on cluster instances",
	Run:   runTokenizerStart,
}

var tokenizerStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop tokenizer sidecar on cluster instances",
	Run:   runTokenizerStop,
}

var tokenizerStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check tokenizer sidecar status on cluster instances",
	Run:   runTokenizerStatus,
}

func init() {
	rootCmd.AddCommand(tokenizerCmd)

	tokenizerCmd.AddCommand(tokenizerSetupCmd)
	tokenizerCmd.AddCommand(tokenizerStartCmd)
	tokenizerCmd.AddCommand(tokenizerStopCmd)
	tokenizerCmd.AddCommand(tokenizerStatusCmd)

	// Flags
	tokenizerSetupCmd.Flags().String("cluster", "sglang-auto-cluster", "Cluster name")
	tokenizerSetupCmd.Flags().Bool("start", false, "Start the sidecar after building")

	tokenizerStartCmd.Flags().String("cluster", "sglang-auto-cluster", "Cluster name")
	tokenizerStopCmd.Flags().String("cluster", "sglang-auto-cluster", "Cluster name")
	tokenizerStatusCmd.Flags().String("cluster", "sglang-auto-cluster", "Cluster name")
}

func getClusterForTokenizer(cmd *cobra.Command) (*cluster.Cluster, error) {
	// Load environment variables
	if err := godotenv.Load(".env"); err != nil {
		log.Printf("Warning: Could not load .env file: %v", err)
	}

	httpClient := remote.NewHTTPClient()
	apiToken := remote.GetAPIToken()
	if apiToken == "" {
		return nil, fmt.Errorf("API token not set. Please set LAMBDA_API_KEY environment variable")
	}

	clusterName, _ := cmd.Flags().GetString("cluster")

	cl, err := cluster.GetCluster(httpClient, apiToken, clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster '%s': %v", clusterName, err)
	}

	if err := cl.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to cluster: %v", err)
	}

	return cl, nil
}

func runTokenizerSetup(cmd *cobra.Command, args []string) {
	cl, err := getClusterForTokenizer(cmd)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	defer cl.Disconnect()

	if err := tokenizer.SetupTokenizerSidecar(cl); err != nil {
		log.Fatalf("Setup failed: %v", err)
	}

	// Optionally start the sidecar
	startAfter, _ := cmd.Flags().GetBool("start")
	if startAfter {
		fmt.Println()
		if err := tokenizer.StartTokenizerSidecar(cl); err != nil {
			log.Fatalf("Start failed: %v", err)
		}
	}

	fmt.Println("\n✅ Tokenizer setup complete!")
}

func runTokenizerStart(cmd *cobra.Command, args []string) {
	cl, err := getClusterForTokenizer(cmd)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	defer cl.Disconnect()

	if err := tokenizer.StartTokenizerSidecar(cl); err != nil {
		log.Fatalf("Start failed: %v", err)
	}

	fmt.Println("\n✅ Tokenizer sidecar started!")
}

func runTokenizerStop(cmd *cobra.Command, args []string) {
	cl, err := getClusterForTokenizer(cmd)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	defer cl.Disconnect()

	if err := tokenizer.StopTokenizerSidecar(cl); err != nil {
		log.Fatalf("Stop failed: %v", err)
	}

	fmt.Println("\n✅ Tokenizer sidecar stopped!")
}

func runTokenizerStatus(cmd *cobra.Command, args []string) {
	cl, err := getClusterForTokenizer(cmd)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	defer cl.Disconnect()

	results, err := tokenizer.TokenizerStatus(cl)
	if err != nil {
		log.Fatalf("Status check failed: %v", err)
	}

	// Count running
	runningCount := 0
	for _, status := range results {
		if status == "running" {
			runningCount++
		}
	}

	fmt.Printf("\nSummary: %d/%d instances have tokenizer running\n", runningCount, len(results))
}
