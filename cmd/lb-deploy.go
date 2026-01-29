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
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/atoniolo76/gotoni/pkg/config"
	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/spf13/cobra"
)

// lbDeployCmd deploys load balancer to remote instances
var lbDeployCmd = &cobra.Command{
	Use:   "lb-deploy [instance-id-or-name...]",
	Short: "Build, upload, and start load balancer on all instances (one command)",
	Long: `Full load balancer deployment: build gotoni, upload to instances, and start LB.

This is the ONE COMMAND to deploy load balancers. It will:
1. Build gotoni for Linux
2. Stop any running LB processes on all instances
3. Upload the new binary to all instances
4. Start the load balancer with all instances as peers

Examples:
  # Full deployment to all instances (recommended)
  gotoni lb-deploy --all

  # Deploy to specific instances
  gotoni lb-deploy instance-1 instance-2

  # Skip build if binary already up-to-date
  gotoni lb-deploy --all --skip-build

  # Use custom strategy
  gotoni lb-deploy --all --strategy gorgo`,
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

	// Flags for lb-deploy - defaults from pkg/config/constants.go
	lbDeployCmd.Flags().Bool("all", false, "Deploy to all running instances")
	lbDeployCmd.Flags().Bool("skip-build", false, "Skip building binary (use existing /tmp/gotoni-linux)")
	lbDeployCmd.Flags().Int("local-port", config.DefaultApplicationPort, "Port of the local backend service (SGLang)")
	lbDeployCmd.Flags().Int("listen-port", config.DefaultLoadBalancerPort, "Port for the load balancer to listen on")
	lbDeployCmd.Flags().Int("max-concurrent", config.DefaultMaxConcurrentRequests, "Max concurrent requests before forwarding")
	lbDeployCmd.Flags().String("strategy", config.DefaultStrategy, "Load balancing strategy (gorgo, least-loaded, prefix-tree)")
	lbDeployCmd.Flags().Int("running-threshold", config.DefaultRunningReqsThreshold, "Forward when running_reqs >= this (forces load distribution)")
	lbDeployCmd.Flags().String("gotoni-path", config.DefaultGotoniRemotePath, "Path to gotoni binary on remote")
	lbDeployCmd.Flags().String("session-name", config.DefaultTmuxSessionName, "Tmux session name for load balancer")

	// Flags for lb-remote-status
	lbRemoteStatusCmd.Flags().Bool("all", false, "Check all running instances")
	lbRemoteStatusCmd.Flags().Int("port", config.DefaultLoadBalancerPort, "Load balancer port to check")

	// Flags for lb-remote-stop
	lbRemoteStopCmd.Flags().Bool("all", false, "Stop on all running instances")
	lbRemoteStopCmd.Flags().String("session-name", config.DefaultTmuxSessionName, "Tmux session name to kill")
}

func runLBDeploy(cmd *cobra.Command, args []string) {
	deployAll, _ := cmd.Flags().GetBool("all")
	skipBuild, _ := cmd.Flags().GetBool("skip-build")
	localPort, _ := cmd.Flags().GetInt("local-port")
	listenPort, _ := cmd.Flags().GetInt("listen-port")
	maxConcurrent, _ := cmd.Flags().GetInt("max-concurrent")
	strategy, _ := cmd.Flags().GetString("strategy")
	runningThreshold, _ := cmd.Flags().GetInt("running-threshold")
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

	// Step 1: Build binary (unless skipped)
	binaryPath := "/tmp/gotoni-linux"
	if !skipBuild {
		fmt.Println("Step 1: Building gotoni for Linux...")
		buildCmd := exec.Command("go", "build", "-o", binaryPath, ".")
		buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
		if output, err := buildCmd.CombinedOutput(); err != nil {
			log.Fatalf("Build failed: %v\n%s", err, output)
		}
		info, _ := os.Stat(binaryPath)
		fmt.Printf("   Built %s (%d MB)\n\n", binaryPath, info.Size()/1024/1024)
	} else {
		fmt.Println("Step 1: Skipping build (--skip-build)")
		if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
			log.Fatalf("Binary not found at %s. Run without --skip-build first.", binaryPath)
		}
		fmt.Println()
	}

	// Create SSH manager
	sshMgr := remote.NewSSHClientManager()

	// Connect to all instances first
	fmt.Println("Step 2: Connecting to instances...")
	var connectedInstances []remote.RunningInstance
	for _, inst := range targetInstances {
		sshKeyPath, err := remote.GetSSHKeyFileForInstance(&inst)
		if err != nil {
			fmt.Printf("   %s: ❌ no SSH key: %v\n", inst.Name, err)
			continue
		}

		if err := sshMgr.ConnectToInstance(inst.IP, sshKeyPath); err != nil {
			fmt.Printf("   %s: ❌ connection failed: %v\n", inst.Name, err)
			continue
		}

		fmt.Printf("   %s: ✅ connected\n", inst.Name)
		connectedInstances = append(connectedInstances, inst)
	}

	if len(connectedInstances) == 0 {
		log.Fatal("Failed to connect to any instances")
	}
	fmt.Println()

	// Step 3: Stop running LBs and upload new binary
	fmt.Println("Step 3: Stopping LBs and uploading binary...")
	var wg sync.WaitGroup
	for _, inst := range connectedInstances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()

			// Stop running LB and remove old binary
			// Use pgrep + kill instead of pkill -f to avoid killing SSH session
			cleanupScript := fmt.Sprintf(`
tmux kill-session -t %s 2>/dev/null || true
tmux kill-session -t gotoni-start_gotoni_load_balancer 2>/dev/null || true
PIDS=$(pgrep -f "gotoni lb" 2>/dev/null | head -5)
if [ -n "$PIDS" ]; then kill $PIDS 2>/dev/null; fi
rm -f %s
`, sessionName, gotoniPath)
			sshMgr.ExecuteCommand(instance.IP, cleanupScript)

			// Get SSH key for SCP
			sshKeyPath, _ := remote.GetSSHKeyFileForInstance(&instance)

			// Upload new binary via SCP
			scpCmd := exec.Command("scp",
				"-i", sshKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				binaryPath,
				fmt.Sprintf("ubuntu@%s:%s", instance.IP, gotoniPath),
			)
			if err := scpCmd.Run(); err != nil {
				fmt.Printf("   %s: ❌ upload failed: %v\n", instance.Name, err)
				return
			}

			// Make executable
			sshMgr.ExecuteCommand(instance.IP, fmt.Sprintf("chmod +x %s", gotoniPath))
			fmt.Printf("   %s: ✅ uploaded\n", instance.Name)
		}(inst)
	}
	wg.Wait()
	fmt.Println()

	// Build peer list (all instances)
	var peerIPs []string
	for _, inst := range connectedInstances {
		peerIPs = append(peerIPs, inst.IP)
	}

	// Step 4: Start LB on each instance
	fmt.Println("Step 4: Starting load balancers...")
	for _, inst := range connectedInstances {
		// Build the gotoni lb start command
		nodeID := inst.Name
		if nodeID == "" {
			nodeID = inst.ID[:16]
		}
		lbCommand := fmt.Sprintf("%s lb start --local-port %d --listen-port %d --max-concurrent %d --strategy %s --node-id %s --running-threshold %d",
			gotoniPath, localPort, listenPort, maxConcurrent, strategy, nodeID, runningThreshold)

		// Add peers (excluding self)
		if len(peerIPs) > 1 {
			var peers []string
			for _, peerIP := range peerIPs {
				if peerIP != inst.IP {
					peers = append(peers, fmt.Sprintf("%s:%d", peerIP, listenPort))
				}
			}
			if len(peers) > 0 {
				for _, peer := range peers {
					lbCommand += fmt.Sprintf(" --peers %s", peer)
				}
			}
		}

		// Start in tmux session with increased file descriptor limit
		// ulimit -n 65535 ensures we can handle many concurrent connections
		tmuxCmd := fmt.Sprintf("tmux new-session -d -s %s 'ulimit -n 65535; %s'", sessionName, lbCommand)

		output, err := sshMgr.ExecuteCommand(inst.IP, tmuxCmd)
		if err != nil {
			fmt.Printf("   %s: ❌ start failed: %v\nOutput: %s\n", inst.Name, err, output)
			continue
		}

		// Verify it started
		time.Sleep(1 * time.Second)
		checkCmd := fmt.Sprintf("curl -s http://localhost:%d/lb/status 2>/dev/null || echo 'not_running'", listenPort)
		status, _ := sshMgr.ExecuteCommand(inst.IP, checkCmd)

		if strings.Contains(status, "not_running") {
			fmt.Printf("   %s: ⚠️  may not have started correctly\n", inst.Name)
		} else {
			fmt.Printf("   %s: ✅ LB running with %d peers (threshold=%d)\n", inst.Name, len(peerIPs)-1, runningThreshold)
		}
	}

	fmt.Println("\n✅ Deployment complete!")
	fmt.Println("\nRun 'gotoni cluster status' to verify all LBs are healthy.")
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
