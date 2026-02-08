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
load balancer policy for accurate routing decisions.

Quick Start:
  gotoni tokenizer deploy    # Build and start on all instances (recommended)

Commands:
  deploy  - Build (if needed) and start tokenizer on all instances
  setup   - Only build the tokenizer binary (does not start)
  start   - Start already-built tokenizer
  stop    - Stop running tokenizer
  status  - Check tokenizer status on all instances
  restart - Stop and restart tokenizer on all instances

Examples:
  # Full deployment (build + start) - use this for first time setup
  gotoni tokenizer deploy

  # Just check status
  gotoni tokenizer status

  # Restart after issues
  gotoni tokenizer restart`,
}

var tokenizerDeployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Build and start tokenizer on all cluster instances (recommended)",
	Long: `Full deployment: builds the Rust tokenizer binary and starts it on all instances.

This is the recommended command for first-time setup or updates. It will:
1. Upload the Rust source code to each instance
2. Install Rust toolchain if needed
3. Build the tokenizer-sidecar binary
4. Start the tokenizer service

The tokenizer listens on /tmp/tokenizer.sock for token counting requests.`,
	Run: runTokenizerDeploy,
}

var tokenizerSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Build tokenizer binary only (use 'deploy' to also start)",
	Long: `Upload the Rust tokenizer source code and build it on all cluster instances.

This only builds the binary - use 'gotoni tokenizer deploy' to build AND start,
or run 'gotoni tokenizer start' after setup to start the service.`,
	Run: runTokenizerSetup,
}

var tokenizerStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start tokenizer sidecar (binary must be built first)",
	Long: `Start the tokenizer sidecar on all cluster instances.

The binary must already be built with 'gotoni tokenizer setup' or 'gotoni tokenizer deploy'.
If you get "binary not found" errors, run 'gotoni tokenizer deploy' instead.`,
	Run: runTokenizerStart,
}

var tokenizerStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop tokenizer sidecar on all instances",
	Run:   runTokenizerStop,
}

var tokenizerRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart tokenizer sidecar on all instances",
	Run:   runTokenizerRestart,
}

var tokenizerStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check tokenizer sidecar status on all instances",
	Run:   runTokenizerStatus,
}

func init() {
	rootCmd.AddCommand(tokenizerCmd)

	tokenizerCmd.AddCommand(tokenizerDeployCmd)
	tokenizerCmd.AddCommand(tokenizerSetupCmd)
	tokenizerCmd.AddCommand(tokenizerStartCmd)
	tokenizerCmd.AddCommand(tokenizerStopCmd)
	tokenizerCmd.AddCommand(tokenizerRestartCmd)
	tokenizerCmd.AddCommand(tokenizerStatusCmd)

	// Flags - cluster name is optional, defaults to all running instances
	tokenizerDeployCmd.Flags().String("cluster", "sglang-auto-cluster", "Cluster name")
	tokenizerSetupCmd.Flags().String("cluster", "sglang-auto-cluster", "Cluster name")
	tokenizerSetupCmd.Flags().Bool("start", false, "Start the sidecar after building (deprecated, use 'deploy' instead)")
	tokenizerStartCmd.Flags().String("cluster", "sglang-auto-cluster", "Cluster name")
	tokenizerStopCmd.Flags().String("cluster", "sglang-auto-cluster", "Cluster name")
	tokenizerRestartCmd.Flags().String("cluster", "sglang-auto-cluster", "Cluster name")
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

func runTokenizerDeploy(cmd *cobra.Command, args []string) {
	cl, err := getClusterForTokenizer(cmd)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	defer cl.Disconnect()

	fmt.Println("🚀 Deploying tokenizer to cluster...")
	fmt.Println()

	// Build the tokenizer
	if err := tokenizer.SetupTokenizerSidecar(cl); err != nil {
		log.Fatalf("Build failed: %v", err)
	}

	fmt.Println()

	// Start the tokenizer
	if err := tokenizer.StartTokenizerSidecar(cl); err != nil {
		log.Fatalf("Start failed: %v", err)
	}

	fmt.Println("\n✅ Tokenizer deployed and running!")
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

	// Optionally start the sidecar (deprecated, kept for backwards compatibility)
	startAfter, _ := cmd.Flags().GetBool("start")
	if startAfter {
		fmt.Println()
		if err := tokenizer.StartTokenizerSidecar(cl); err != nil {
			log.Fatalf("Start failed: %v", err)
		}
	}

	fmt.Println("\n✅ Tokenizer setup complete!")
	if !startAfter {
		fmt.Println("💡 Run 'gotoni tokenizer start' to start the sidecar, or use 'gotoni tokenizer deploy' next time.")
	}
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

func runTokenizerRestart(cmd *cobra.Command, args []string) {
	cl, err := getClusterForTokenizer(cmd)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	defer cl.Disconnect()

	fmt.Println("🔄 Restarting tokenizer sidecar...")
	fmt.Println()

	// Stop first (ignore errors - might not be running)
	_ = tokenizer.StopTokenizerSidecar(cl)

	fmt.Println()

	// Start
	if err := tokenizer.StartTokenizerSidecar(cl); err != nil {
		log.Fatalf("Start failed: %v", err)
	}

	fmt.Println("\n✅ Tokenizer sidecar restarted!")
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
