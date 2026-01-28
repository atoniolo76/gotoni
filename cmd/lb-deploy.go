/*
Copyright © 2025 ALESSIO TONIOLO

lb-deploy.go implements remote load balancer deployment and management.
Uses SSH to deploy and manage load balancer processes on remote instances.
Instance state comes from Lambda API; SSH key paths are looked up locally.
*/
package cmd

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/spf13/cobra"
)

// lbDeployCmd deploys load balancer to remote instances
var lbDeployCmd = &cobra.Command{
	Use:   "lb-deploy [instance-id-or-name...]",
	Short: "Deploy and start load balancer on remote instances",
	Long: `Deploy and start the load balancer process on one or more remote instances via SSH.

This command assumes:
1. The gotoni binary is already installed on the remote instance (see Phase 1)
2. SSH access is configured for the instance

The command will:
1. Connect to each instance via SSH
2. Start the load balancer in the background using tmux
3. Configure peers to form a cluster

Examples:
  # Deploy to a single instance
  gotoni lb-deploy my-instance

  # Deploy to multiple instances
  gotoni lb-deploy instance-1 instance-2 instance-3

  # Deploy to all running instances
  gotoni lb-deploy --all

  # Deploy with custom config
  gotoni lb-deploy my-instance --local-port 8080 --listen-port 8000

  # Deploy and configure all instances as peers of each other
  gotoni lb-deploy --all --cluster-mode`,
	Run: runLBDeploy,
}

// lbRemoteStatusCmd checks load balancer status on remote instances
var lbRemoteStatusCmd = &cobra.Command{
	Use:   "lb-remote-status [instance-id-or-name...]",
	Short: "Check load balancer status on remote instances",
	Long:  `Check the load balancer status on one or more remote instances via SSH.`,
	Run:   runLBRemoteStatus,
}

// lbRemoteStopCmd stops load balancer on remote instances
var lbRemoteStopCmd = &cobra.Command{
	Use:   "lb-remote-stop [instance-id-or-name...]",
	Short: "Stop load balancer on remote instances",
	Long:  `Stop the load balancer process on one or more remote instances via SSH.`,
	Run:   runLBRemoteStop,
}

func init() {
	rootCmd.AddCommand(lbDeployCmd)
	rootCmd.AddCommand(lbRemoteStatusCmd)
	rootCmd.AddCommand(lbRemoteStopCmd)

	// Flags for lb-deploy
	lbDeployCmd.Flags().Bool("all", false, "Deploy to all running instances")
	lbDeployCmd.Flags().Bool("cluster-mode", false, "Configure all instances as peers of each other")
	lbDeployCmd.Flags().Int("local-port", 8080, "Port of the local backend service")
	lbDeployCmd.Flags().Int("listen-port", 8000, "Port for the load balancer to listen on")
	lbDeployCmd.Flags().Int("max-concurrent", 10, "Max concurrent requests before forwarding")
	lbDeployCmd.Flags().String("strategy", "least-loaded", "Load balancing strategy")
	lbDeployCmd.Flags().String("gotoni-path", "/home/ubuntu/gotoni", "Path to gotoni binary on remote")
	lbDeployCmd.Flags().String("session-name", "gotoni-lb", "Tmux session name for load balancer")

	// Flags for lb-remote-status
	lbRemoteStatusCmd.Flags().Bool("all", false, "Check all running instances")
	lbRemoteStatusCmd.Flags().Int("port", 8000, "Load balancer port to check")

	// Flags for lb-remote-stop
	lbRemoteStopCmd.Flags().Bool("all", false, "Stop on all running instances")
	lbRemoteStopCmd.Flags().String("session-name", "gotoni-lb", "Tmux session name to kill")
}

func runLBDeploy(cmd *cobra.Command, args []string) {
	deployAll, _ := cmd.Flags().GetBool("all")
	clusterMode, _ := cmd.Flags().GetBool("cluster-mode")
	localPort, _ := cmd.Flags().GetInt("local-port")
	listenPort, _ := cmd.Flags().GetInt("listen-port")
	maxConcurrent, _ := cmd.Flags().GetInt("max-concurrent")
	strategy, _ := cmd.Flags().GetString("strategy")
	gotoniPath, _ := cmd.Flags().GetString("gotoni-path")
	sessionName, _ := cmd.Flags().GetString("session-name")

	if !deployAll && len(args) == 0 {
		log.Fatal("Please specify instance IDs/names or use --all flag")
	}

	// Get API token
	apiToken := remote.GetAPIToken()
	if apiToken == "" {
		log.Fatal("LAMBDA_API_KEY environment variable not set")
	}

	httpClient := remote.NewHTTPClient()

	// Get running instances
	runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
	if err != nil {
		log.Fatalf("Failed to list running instances: %v", err)
	}

	if len(runningInstances) == 0 {
		log.Fatal("No running instances found")
	}

	// Filter instances based on args
	var targetInstances []remote.RunningInstance
	if deployAll {
		targetInstances = runningInstances
	} else {
		for _, arg := range args {
			for _, inst := range runningInstances {
				if inst.ID == arg || inst.Name == arg || strings.HasPrefix(inst.ID, arg) {
					targetInstances = append(targetInstances, inst)
					break
				}
			}
		}
	}

	if len(targetInstances) == 0 {
		log.Fatal("No matching instances found")
	}

	fmt.Printf("Deploying load balancer to %d instance(s)...\n\n", len(targetInstances))

	// Create SSH manager
	sshMgr := remote.NewSSHClientManager()

	// Connect to all instances
	for _, inst := range targetInstances {
		// Get SSH key using Lambda API key names + local file lookup
		sshKeyPath, err := remote.GetSSHKeyFileForInstance(&inst)
		if err != nil {
			log.Printf("Warning: Could not find SSH key for instance %s: %v", inst.IP, err)
			continue
		}

		if err := sshMgr.ConnectToInstance(inst.IP, sshKeyPath); err != nil {
			log.Printf("Warning: Failed to connect to instance %s: %v", inst.IP, err)
			continue
		}

		fmt.Printf("✅ Connected to %s (%s)\n", inst.Name, inst.IP)
	}

	// Build peer list if cluster mode is enabled
	var peerIPs []string
	if clusterMode && len(targetInstances) > 1 {
		for _, inst := range targetInstances {
			peerIPs = append(peerIPs, inst.IP)
		}
	}

	// Deploy to each instance
	for _, inst := range targetInstances {
		fmt.Printf("\nDeploying to %s (%s)...\n", inst.Name, inst.IP)

		// Build the gotoni lb start command
		lbCommand := fmt.Sprintf("%s lb start --local-port %d --listen-port %d --max-concurrent %d --strategy %s --node-id %s",
			gotoniPath, localPort, listenPort, maxConcurrent, strategy, inst.ID[:16])

		// Add peers (excluding self) if cluster mode
		if clusterMode && len(peerIPs) > 1 {
			var peers []string
			for _, peerIP := range peerIPs {
				if peerIP != inst.IP {
					peers = append(peers, fmt.Sprintf("%s:%d", peerIP, listenPort))
				}
			}
			if len(peers) > 0 {
				lbCommand += fmt.Sprintf(" --peers %s", strings.Join(peers, ","))
			}
		}

		// Kill existing session if it exists
		killCmd := fmt.Sprintf("tmux kill-session -t %s 2>/dev/null || true", sessionName)
		sshMgr.ExecuteCommand(inst.IP, killCmd)

		// Start in tmux session
		tmuxCmd := fmt.Sprintf("tmux new-session -d -s %s '%s'", sessionName, lbCommand)

		output, err := sshMgr.ExecuteCommand(inst.IP, tmuxCmd)
		if err != nil {
			log.Printf("❌ Failed to start load balancer on %s: %v\nOutput: %s", inst.Name, err, output)
			continue
		}

		// Verify it started
		time.Sleep(1 * time.Second)
		checkCmd := fmt.Sprintf("curl -s http://localhost:%d/lb/status 2>/dev/null || echo 'not_running'", listenPort)
		status, _ := sshMgr.ExecuteCommand(inst.IP, checkCmd)

		if strings.Contains(status, "not_running") {
			log.Printf("⚠️  Load balancer may not have started correctly on %s", inst.Name)
		} else {
			fmt.Printf("✅ Load balancer started on %s\n", inst.Name)
			fmt.Printf("   Status: %s\n", strings.TrimSpace(status))
		}
	}

	fmt.Println("\n✅ Deployment complete!")

	if clusterMode && len(targetInstances) > 1 {
		fmt.Println("\nCluster mode enabled - all instances are configured as peers.")
	}
}

func runLBRemoteStatus(cmd *cobra.Command, args []string) {
	checkAll, _ := cmd.Flags().GetBool("all")
	port, _ := cmd.Flags().GetInt("port")

	if !checkAll && len(args) == 0 {
		log.Fatal("Please specify instance IDs/names or use --all flag")
	}

	// Get API token
	apiToken := remote.GetAPIToken()
	if apiToken == "" {
		log.Fatal("LAMBDA_API_KEY environment variable not set")
	}

	httpClient := remote.NewHTTPClient()

	// Get running instances
	runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
	if err != nil {
		log.Fatalf("Failed to list running instances: %v", err)
	}

	// Filter instances
	var targetInstances []remote.RunningInstance
	if checkAll {
		targetInstances = runningInstances
	} else {
		for _, arg := range args {
			for _, inst := range runningInstances {
				if inst.ID == arg || inst.Name == arg || strings.HasPrefix(inst.ID, arg) {
					targetInstances = append(targetInstances, inst)
					break
				}
			}
		}
	}

	if len(targetInstances) == 0 {
		log.Fatal("No matching instances found")
	}

	// Create SSH manager
	sshMgr := remote.NewSSHClientManager()

	fmt.Printf("Checking load balancer status on %d instance(s)...\n\n", len(targetInstances))

	for _, inst := range targetInstances {
		// Get SSH key using Lambda API key names + local file lookup
		sshKeyPath, err := remote.GetSSHKeyFileForInstance(&inst)
		if err != nil {
			fmt.Printf("❌ %s: Could not find SSH key: %v\n", inst.Name, err)
			continue
		}

		if err := sshMgr.ConnectToInstance(inst.IP, sshKeyPath); err != nil {
			fmt.Printf("❌ %s: Connection failed\n", inst.Name)
			continue
		}

		// Check LB status
		statusCmd := fmt.Sprintf("curl -s http://localhost:%d/lb/status 2>/dev/null", port)
		output, err := sshMgr.ExecuteCommand(inst.IP, statusCmd)

		if err != nil || strings.TrimSpace(output) == "" {
			fmt.Printf("❌ %s (%s): Load balancer NOT running\n", inst.Name, inst.IP)

			// Debug: check if process exists and port is listening
			debugCmd := fmt.Sprintf("echo '--- Processes ---' && ps aux | grep -E 'gotoni|lb' | grep -v grep; echo '--- Port %d ---' && ss -tlnp | grep %d || echo 'Port %d not listening'; echo '--- Tmux sessions ---' && tmux list-sessions 2>/dev/null || echo 'No tmux sessions'", port, port, port)
			debugOutput, _ := sshMgr.ExecuteCommand(inst.IP, debugCmd)
			fmt.Printf("   Debug info:\n%s\n", debugOutput)
			continue
		}

		// Parse status
		var status map[string]interface{}
		if err := json.Unmarshal([]byte(output), &status); err != nil {
			fmt.Printf("⚠️  %s (%s): Running but invalid response\n", inst.Name, inst.IP)
			continue
		}

		currentLoad := int64(0)
		maxLoad := 0
		if cl, ok := status["current_load"].(float64); ok {
			currentLoad = int64(cl)
		}
		if ml, ok := status["max_load"].(float64); ok {
			maxLoad = int(ml)
		}

		fmt.Printf("✅ %s (%s): Running - Load: %d/%d\n", inst.Name, inst.IP, currentLoad, maxLoad)
	}
}

func runLBRemoteStop(cmd *cobra.Command, args []string) {
	stopAll, _ := cmd.Flags().GetBool("all")
	sessionName, _ := cmd.Flags().GetString("session-name")

	if !stopAll && len(args) == 0 {
		log.Fatal("Please specify instance IDs/names or use --all flag")
	}

	// Get API token
	apiToken := remote.GetAPIToken()
	if apiToken == "" {
		log.Fatal("LAMBDA_API_KEY environment variable not set")
	}

	httpClient := remote.NewHTTPClient()

	// Get running instances
	runningInstances, err := remote.ListRunningInstances(httpClient, apiToken)
	if err != nil {
		log.Fatalf("Failed to list running instances: %v", err)
	}

	// Filter instances
	var targetInstances []remote.RunningInstance
	if stopAll {
		targetInstances = runningInstances
	} else {
		for _, arg := range args {
			for _, inst := range runningInstances {
				if inst.ID == arg || inst.Name == arg || strings.HasPrefix(inst.ID, arg) {
					targetInstances = append(targetInstances, inst)
					break
				}
			}
		}
	}

	if len(targetInstances) == 0 {
		log.Fatal("No matching instances found")
	}

	// Create SSH manager
	sshMgr := remote.NewSSHClientManager()

	fmt.Printf("Stopping load balancer on %d instance(s)...\n\n", len(targetInstances))

	for _, inst := range targetInstances {
		// Get SSH key using Lambda API key names + local file lookup
		sshKeyPath, err := remote.GetSSHKeyFileForInstance(&inst)
		if err != nil {
			fmt.Printf("❌ %s: Could not find SSH key: %v\n", inst.Name, err)
			continue
		}

		if err := sshMgr.ConnectToInstance(inst.IP, sshKeyPath); err != nil {
			fmt.Printf("❌ %s: Connection failed\n", inst.Name)
			continue
		}

		// Kill tmux session
		killCmd := fmt.Sprintf("tmux kill-session -t %s 2>/dev/null && echo 'stopped' || echo 'not_running'", sessionName)
		output, _ := sshMgr.ExecuteCommand(inst.IP, killCmd)

		if strings.Contains(output, "stopped") {
			fmt.Printf("✅ %s (%s): Load balancer stopped\n", inst.Name, inst.IP)
		} else {
			fmt.Printf("⚠️  %s (%s): Load balancer was not running\n", inst.Name, inst.IP)
		}
	}

	fmt.Println("\n✅ Stop complete!")
}
