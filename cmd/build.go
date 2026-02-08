/*
Copyright © 2025 ALESSIO TONIOLO

build.go provides commands for building and deploying gotoni.
*/
package cmd

import (
	"fmt"

	serve "github.com/atoniolo76/gotoni/pkg/cluster"
	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/spf13/cobra"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build and deploy gotoni binary",
	Long: `Build gotoni for Linux and optionally deploy to cluster.

Examples:
  gotoni build                    # Build only
  gotoni build --deploy           # Build and deploy to cluster
  gotoni build --deploy --start-lb # Build, deploy, and start LBs`,
	RunE: runBuild,
}

func init() {
	rootCmd.AddCommand(buildCmd)

	buildCmd.Flags().Bool("deploy", false, "Deploy to cluster after building")
	buildCmd.Flags().Bool("start-lb", false, "Start load balancers after deployment")
	buildCmd.Flags().String("strategy", "gorgo", "LB strategy if starting LBs")
	buildCmd.Flags().String("cluster", "sglang-auto-cluster", "Cluster name for deployment")
	buildCmd.Flags().StringP("output", "o", "/tmp/gotoni-linux", "Output path for binary")
}

func runBuild(cmd *cobra.Command, args []string) error {
	outputPath, _ := cmd.Flags().GetString("output")
	deploy, _ := cmd.Flags().GetBool("deploy")
	startLB, _ := cmd.Flags().GetBool("start-lb")
	strategy, _ := cmd.Flags().GetString("strategy")
	clusterName, _ := cmd.Flags().GetString("cluster")

	// Build
	if err := serve.BuildGotoniLinux(outputPath); err != nil {
		return err
	}

	if !deploy {
		fmt.Println("\nBuild complete. Use --deploy to upload to cluster.")
		return nil
	}

	// Get cluster
	httpClient := remote.NewHTTPClient()
	apiToken := remote.GetAPIToken()
	if apiToken == "" {
		return fmt.Errorf("LAMBDA_API_KEY not set")
	}

	cluster, err := serve.GetCluster(httpClient, apiToken, clusterName)
	if err != nil {
		return fmt.Errorf("failed to get cluster: %w", err)
	}

	if err := cluster.Connect(); err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer cluster.Disconnect()

	// Deploy
	if err := serve.DeployGotoniToCluster(cluster, outputPath); err != nil {
		return err
	}

	// Optionally start LBs
	if startLB {
		fmt.Println("\nStarting load balancers...")
		if err := serve.DeployLBStrategy(cluster, strategy); err != nil {
			return err
		}
	}

	fmt.Println("\n✅ Done!")
	return nil
}
