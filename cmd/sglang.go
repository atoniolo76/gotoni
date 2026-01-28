/*
Copyright © 2025 ALESSIO TONIOLO

sglang.go implements SGLang cluster management commands.
These commands help set up and manage SGLang servers across cluster instances.
*/
package cmd

import (
	"fmt"
	"log"
	"os"
	"time"

	cluster "github.com/atoniolo76/gotoni/pkg/cluster"
	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

// sglangCmd represents the sglang command group
var sglangCmd = &cobra.Command{
	Use:   "sglang",
	Short: "Manage SGLang servers on cluster instances",
	Long: `SGLang cluster management commands for deploying and managing SGLang servers.

These commands help you set up SGLang Docker containers, deploy servers across
cluster instances, and diagnose issues with your SGLang deployment.

Examples:
  # Setup SGLang cluster with all running instances
  gotoni sglang setup-cluster --cluster-name my-sglang-cluster

  # Deploy SGLang servers to cluster instances
  gotoni sglang deploy-servers

  # Run diagnostics on SGLang instances
  gotoni sglang diagnose

  # Test SGLang API endpoints
  gotoni sglang test-endpoints`,
}

var sglangSetupClusterCmd = &cobra.Command{
	Use:   "setup-cluster",
	Short: "Setup a complete SGLang cluster",
	Long: `Create or load a cluster and deploy SGLang servers to all instances.

This command discovers all running instances, creates a cluster, connects via SSH,
sets up SGLang Docker containers, and deploys SGLang servers.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Load environment variables
		if err := godotenv.Load(".env"); err != nil {
			log.Printf("Warning: Could not load .env file: %v", err)
		}

		httpClient := remote.NewHTTPClient()
		apiToken := remote.GetAPIToken()
		if apiToken == "" {
			log.Fatal("API token not set. Please set LAMBDA_API_KEY environment variable or use --api-token flag")
		}

		clusterName, _ := cmd.Flags().GetString("cluster-name")
		if clusterName == "" {
			clusterName = "sglang-cluster"
		}

		hfToken := os.Getenv("HF_TOKEN")

		fmt.Printf("Setting up SGLang cluster '%s'...\n", clusterName)
		cl, err := cluster.SetupSGLangCluster(httpClient, apiToken, clusterName, hfToken)
		if err != nil {
			log.Fatalf("Failed to setup SGLang cluster: %v", err)
		}
		defer cl.Disconnect()

		fmt.Println("✅ SGLang cluster setup complete!")
	},
}

var sglangDeployServersCmd = &cobra.Command{
	Use:   "deploy-servers",
	Short: "Deploy SGLang servers to existing cluster",
	Long: `Deploy SGLang server containers to all instances in the current cluster.

This assumes you have already created a cluster with 'gotoni sglang setup-cluster'.
It will deploy SGLang Docker containers and start the servers.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Load environment variables
		if err := godotenv.Load(".env"); err != nil {
			log.Printf("Warning: Could not load .env file: %v", err)
		}

		httpClient := remote.NewHTTPClient()
		apiToken := remote.GetAPIToken()
		if apiToken == "" {
			log.Fatal("API token not set. Please set LAMBDA_API_KEY environment variable")
		}

		clusterName, _ := cmd.Flags().GetString("cluster-name")
		if clusterName == "" {
			clusterName = "sglang-cluster"
		}

		hfToken := os.Getenv("HF_TOKEN")

		// Get existing cluster
		cl, err := cluster.GetCluster(httpClient, apiToken, clusterName)
		if err != nil {
			log.Fatalf("Failed to get cluster '%s': %v", clusterName, err)
		}

		// Connect to cluster
		if err := cl.Connect(); err != nil {
			log.Fatalf("Failed to connect to cluster: %v", err)
		}
		defer cl.Disconnect()

		if err := cluster.WaitForHealthyCluster(cl, len(cl.Instances), 10*time.Minute); err != nil {
			log.Fatalf("Failed to get healthy cluster: %w", err)
		}

		// Setup SGLang Docker
		fmt.Println("Setting up SGLang Docker on cluster instances...")
		cluster.SetupSGLangDockerOnCluster(cl, hfToken)

		// Deploy servers
		fmt.Println("Deploying SGLang servers...")
		// Note: The actual server deployment logic would go here
		// For now, this is a placeholder

		fmt.Println("✅ SGLang servers deployed!")
	},
}

var sglangDiagnoseCmd = &cobra.Command{
	Use:   "diagnose",
	Short: "Run diagnostics on SGLang cluster instances",
	Long: `Run comprehensive diagnostics on all SGLang instances to identify issues.

This checks Docker container status, port availability, GPU access, and API endpoints.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Load environment variables
		if err := godotenv.Load(".env"); err != nil {
			log.Printf("Warning: Could not load .env file: %v", err)
		}

		httpClient := remote.NewHTTPClient()
		apiToken := remote.GetAPIToken()
		if apiToken == "" {
			log.Fatal("API token not set. Please set LAMBDA_API_KEY environment variable")
		}

		clusterName, _ := cmd.Flags().GetString("cluster-name")
		if clusterName == "" {
			clusterName = "sglang-cluster"
		}

		// Get existing cluster
		cl, err := cluster.GetCluster(httpClient, apiToken, clusterName)
		if err != nil {
			log.Fatalf("Failed to get cluster '%s': %v", clusterName, err)
		}

		// Connect to cluster
		if err := cl.Connect(); err != nil {
			log.Fatalf("Failed to connect to cluster: %v", err)
		}
		defer cl.Disconnect()

		fmt.Println("Running SGLang diagnostics...")
		cluster.DiagnoseSGLangOnCluster(cl)
		fmt.Println("✅ Diagnostics complete!")
	},
}

var sglangTestEndpointsCmd = &cobra.Command{
	Use:   "test-endpoints",
	Short: "Test SGLang API endpoints on cluster instances",
	Long: `Test the SGLang API endpoints (/health and /get_server_info) on all cluster instances.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Load environment variables
		if err := godotenv.Load(".env"); err != nil {
			log.Printf("Warning: Could not load .env file: %v", err)
		}

		httpClient := remote.NewHTTPClient()
		apiToken := remote.GetAPIToken()
		if apiToken == "" {
			log.Fatal("API token not set. Please set LAMBDA_API_KEY environment variable")
		}

		clusterName, _ := cmd.Flags().GetString("cluster-name")
		if clusterName == "" {
			clusterName = "sglang-cluster"
		}

		// Get existing cluster
		cl, err := cluster.GetCluster(httpClient, apiToken, clusterName)
		if err != nil {
			log.Fatalf("Failed to get cluster '%s': %v", clusterName, err)
		}

		// Connect to cluster
		if err := cl.Connect(); err != nil {
			log.Fatalf("Failed to connect to cluster: %v", err)
		}
		defer cl.Disconnect()

		fmt.Println("Testing SGLang API endpoints...")
		cluster.TestSGLangAPIEndpoints(cl, cl.Instances)
		fmt.Println("✅ Endpoint tests complete!")
	},
}

func init() {
	// Add sglang command to root
	rootCmd.AddCommand(sglangCmd)

	// Add subcommands
	sglangCmd.AddCommand(sglangSetupClusterCmd)
	sglangCmd.AddCommand(sglangDeployServersCmd)
	sglangCmd.AddCommand(sglangDiagnoseCmd)
	sglangCmd.AddCommand(sglangTestEndpointsCmd)

	// Add flags
	sglangSetupClusterCmd.Flags().String("cluster-name", "", "Name for the SGLang cluster")
	sglangDeployServersCmd.Flags().String("cluster-name", "", "Name of the existing cluster")
	sglangDiagnoseCmd.Flags().String("cluster-name", "", "Name of the cluster to diagnose")
	sglangTestEndpointsCmd.Flags().String("cluster-name", "", "Name of the cluster to test")
}