/*
Copyright © 2025 ALESSIO TONIOLO

cluster.go implements cluster management commands for remote operations.
The source of truth for cluster state is the Lambda API (running instances).
SSH key file paths are looked up locally using the key names from Lambda API.
*/
package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	serve "github.com/atoniolo76/gotoni/pkg/cluster"
	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/atoniolo76/gotoni/pkg/tokenizer"
	"github.com/spf13/cobra"
)

var clusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Manage cluster nodes (LB, SGLang, binary uploads)",
	Long: `Cluster management commands for operating on all nodes.

Examples:
  gotoni cluster status          # Check all nodes (LB, SGLang, peers)
  gotoni cluster restart-lb      # Restart LBs on all nodes  
  gotoni cluster upload          # Build and upload gotoni binary
  gotoni cluster start-trace     # Start tracing on all nodes
  gotoni cluster stop-trace      # Stop tracing and collect results

Tokenizer shortcuts (see 'gotoni tokenizer' for full commands):
  gotoni cluster start-tokenizer   # Same as 'gotoni tokenizer start'
  gotoni cluster tokenizer-status  # Same as 'gotoni tokenizer status'`,
}

var clusterStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check status of all cluster nodes",
	Run:   runClusterStatus,
}

var clusterRestartLBCmd = &cobra.Command{
	Use:   "restart-lb",
	Short: "Restart load balancers on all nodes",
	Run:   runClusterRestartLB,
}

var clusterUploadCmd = &cobra.Command{
	Use:   "upload",
	Short: "Build and upload gotoni binary to all nodes",
	Run:   runClusterUpload,
}

var clusterStartTraceCmd = &cobra.Command{
	Use:   "start-trace",
	Short: "Start tracing on all LB nodes",
	Run:   runClusterStartTrace,
}

var clusterStopTraceCmd = &cobra.Command{
	Use:   "stop-trace",
	Short: "Stop tracing and save results",
	Run:   runClusterStopTrace,
}

var clusterFlushCacheCmd = &cobra.Command{
	Use:   "flush-cache",
	Short: "Flush KV cache on all SGLang servers",
	Run:   runClusterFlushCache,
}

var clusterStartTokenizerCmd = &cobra.Command{
	Use:   "start-tokenizer",
	Short: "Start tokenizer sidecar on all nodes",
	Run:   runClusterStartTokenizer,
}

var clusterTokenizerStatusCmd = &cobra.Command{
	Use:   "tokenizer-status",
	Short: "Check tokenizer sidecar status on all nodes",
	Run:   runClusterTokenizerStatus,
}

func init() {
	rootCmd.AddCommand(clusterCmd)
	clusterCmd.AddCommand(clusterStatusCmd)
	clusterCmd.AddCommand(clusterRestartLBCmd)
	clusterCmd.AddCommand(clusterUploadCmd)
	clusterCmd.AddCommand(clusterStartTraceCmd)
	clusterCmd.AddCommand(clusterStopTraceCmd)
	clusterCmd.AddCommand(clusterFlushCacheCmd)
	clusterCmd.AddCommand(clusterStartTokenizerCmd)
	clusterCmd.AddCommand(clusterTokenizerStatusCmd)

	// Flags
	clusterRestartLBCmd.Flags().String("strategy", "gorgo", "LB strategy: gorgo, prefix-tree, least-loaded")
	clusterRestartLBCmd.Flags().Int("max-concurrent", 100, "Max concurrent requests")
	clusterRestartLBCmd.Flags().Int("running-threshold", 0, "Forward when running_reqs >= this (0=use ie-queue)")

	clusterStopTraceCmd.Flags().String("output-dir", "benchmark_results", "Directory to save traces")

	clusterFlushCacheCmd.Flags().Int("port", 8080, "SGLang backend port")
}

type nodeStatus struct {
	Name      string
	IP        string
	LBHealthy bool
	LBPeers   int
	SGLang    bool
	Error     string
}

func getClusterNodes() ([]remote.RunningInstance, error) {
	provider, _ := remote.GetCloudProvider()
	httpClient := remote.NewHTTPClient()
	apiToken := remote.GetAPIToken()

	return provider.ListRunningInstances(httpClient, apiToken)
}

func runClusterStatus(cmd *cobra.Command, args []string) {
	instances, err := getClusterNodes()
	if err != nil {
		fmt.Printf("Failed to get instances: %v\n", err)
		os.Exit(1)
	}

	if len(instances) == 0 {
		fmt.Println("No running instances found")
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	var wg sync.WaitGroup
	results := make(chan nodeStatus, len(instances))

	for _, inst := range instances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()
			status := nodeStatus{Name: instance.Name, IP: instance.IP}

			// Check LB
			resp, err := client.Get(fmt.Sprintf("http://%s:8000/lb/status", instance.IP))
			if err == nil {
				defer resp.Body.Close()
				var lbStatus map[string]interface{}
				if json.NewDecoder(resp.Body).Decode(&lbStatus) == nil {
					status.LBHealthy = true
					if peers, ok := lbStatus["peer_count"].(float64); ok {
						status.LBPeers = int(peers)
					}
				}
			}

			// Check SGLang
			resp, err = client.Get(fmt.Sprintf("http://%s:8080/health", instance.IP))
			if err == nil {
				defer resp.Body.Close()
				status.SGLang = resp.StatusCode == 200
			}

			results <- status
		}(inst)
	}

	wg.Wait()
	close(results)

	// Print results
	fmt.Printf("\n%-20s %-16s %-12s %-8s %-8s\n", "NAME", "IP", "LB", "PEERS", "SGLANG")
	fmt.Println(strings.Repeat("-", 70))

	for status := range results {
		lbStr := "❌"
		if status.LBHealthy {
			lbStr = "✅"
		}
		sgStr := "❌"
		if status.SGLang {
			sgStr = "✅"
		}
		fmt.Printf("%-20s %-16s %-12s %-8d %-8s\n",
			status.Name, status.IP, lbStr, status.LBPeers, sgStr)
	}
	fmt.Println()
}

func runClusterRestartLB(cmd *cobra.Command, args []string) {
	instances, err := getClusterNodes()
	if err != nil {
		fmt.Printf("Failed to get instances: %v\n", err)
		os.Exit(1)
	}

	strategy, _ := cmd.Flags().GetString("strategy")
	maxConcurrent, _ := cmd.Flags().GetInt("max-concurrent")
	runningThreshold, _ := cmd.Flags().GetInt("running-threshold")

	// Collect all IPs for peer configuration
	var allIPs []string
	for _, inst := range instances {
		allIPs = append(allIPs, inst.IP)
	}

	sshMgr := remote.NewSSHClientManager()

	fmt.Printf("Restarting LBs with strategy=%s, max_concurrent=%d\n\n", strategy, maxConcurrent)

	var wg sync.WaitGroup
	for i, inst := range instances {
		wg.Add(1)
		go func(instance remote.RunningInstance, idx int) {
			defer wg.Done()

			// Get SSH key using Lambda API key names + local file lookup
			sshKeyPath, err := remote.GetSSHKeyFileForInstance(&instance)
			if err != nil {
				fmt.Printf("%-20s: ❌ no SSH key: %v\n", instance.Name, err)
				return
			}

			// Connect
			if err := sshMgr.ConnectToInstance(instance.IP, sshKeyPath); err != nil {
				fmt.Printf("%-20s: ❌ SSH failed: %v\n", instance.Name, err)
				return
			}

			// Build peer list (all other nodes)
			var peers []string
			for j, ip := range allIPs {
				if j != idx {
					peers = append(peers, fmt.Sprintf("--peers %s:8000", ip))
				}
			}
			peersArg := strings.Join(peers, " ")

			// Restart LB
			// Build threshold flag if set
			thresholdArg := ""
			if runningThreshold > 0 {
				thresholdArg = fmt.Sprintf("--running-threshold %d", runningThreshold)
			}

			restartCmd := fmt.Sprintf(`
# Kill existing LB
pkill -f 'gotoni lb' 2>/dev/null || true
sleep 1

# Check binary exists
if [ ! -f /home/ubuntu/gotoni ]; then
  echo "ERROR: /home/ubuntu/gotoni not found"
  exit 1
fi

# Start LB
nohup /home/ubuntu/gotoni lb start \
  --listen-port 8000 \
  --local-port 8080 \
  --strategy %s \
  --max-concurrent %d \
  --ie-queue-indicator \
  --node-id %s \
  %s %s \
  > /home/ubuntu/lb.log 2>&1 &

# Wait for startup
sleep 3

# Check health
if curl -s http://localhost:8000/lb/health | grep -q healthy; then
  echo "OK"
else
  echo "FAILED - log output:"
  tail -20 /home/ubuntu/lb.log 2>/dev/null || echo "no log"
fi
`, strategy, maxConcurrent, instance.Name, thresholdArg, peersArg)

			output, err := sshMgr.ExecuteCommandWithTimeout(instance.IP, restartCmd, 30*time.Second)
			if err != nil {
				fmt.Printf("%-20s: ❌ SSH error: %v\n", instance.Name, err)
			} else if strings.Contains(output, "OK") {
				fmt.Printf("%-20s: ✅ restarted\n", instance.Name)
			} else {
				fmt.Printf("%-20s: ❌ restart failed\n%s\n", instance.Name, output)
			}
		}(inst, i)
	}

	wg.Wait()
	fmt.Println("\nDone. Run 'gotoni cluster status' to verify.")
}

func runClusterUpload(cmd *cobra.Command, args []string) {
	fmt.Println("Building gotoni for Linux...")

	// Build binary
	buildCmd := exec.Command("go", "build", "-o", "/tmp/gotoni-linux", ".")
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		fmt.Printf("Build failed: %v\n%s\n", err, output)
		os.Exit(1)
	}

	// Check binary size
	info, _ := os.Stat("/tmp/gotoni-linux")
	fmt.Printf("Built /tmp/gotoni-linux (%d MB)\n\n", info.Size()/1024/1024)

	instances, err := getClusterNodes()
	if err != nil {
		fmt.Printf("Failed to get instances: %v\n", err)
		os.Exit(1)
	}

	sshMgr := remote.NewSSHClientManager()
	fmt.Println("Uploading to nodes...")

	var wg sync.WaitGroup
	for _, inst := range instances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()

			// Get SSH key using Lambda API key names + local file lookup
			sshKeyPath, err := remote.GetSSHKeyFileForInstance(&instance)
			if err != nil {
				fmt.Printf("%-20s: ❌ no SSH key: %v\n", instance.Name, err)
				return
			}

			// Connect via SSH manager
			if err := sshMgr.ConnectToInstance(instance.IP, sshKeyPath); err != nil {
				fmt.Printf("%-20s: ❌ SSH failed: %v\n", instance.Name, err)
				return
			}

			// Stop running gotoni processes and delete old binary before uploading
			// This ensures clean deployment without stale processes
			cleanupScript := `
tmux kill-session -t gotoni-start_gotoni_load_balancer 2>/dev/null || true
PIDS=$(pgrep -f "gotoni lb" 2>/dev/null | head -5)
if [ -n "$PIDS" ]; then kill $PIDS 2>/dev/null; fi
rm -f /home/ubuntu/gotoni
`
			sshMgr.ExecuteCommand(instance.IP, cleanupScript)

			// SCP upload (still need exec for file transfer)
			scpCmd := exec.Command("scp",
				"-i", sshKeyPath,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"/tmp/gotoni-linux",
				fmt.Sprintf("ubuntu@%s:/home/ubuntu/gotoni", instance.IP),
			)
			if err := scpCmd.Run(); err != nil {
				fmt.Printf("%-20s: ❌ upload failed: %v\n", instance.Name, err)
				return
			}

			// Verify upload
			output, err := sshMgr.ExecuteCommand(instance.IP, "ls -la /home/ubuntu/gotoni && chmod +x /home/ubuntu/gotoni")
			if err != nil {
				fmt.Printf("%-20s: ❌ verify failed: %v\n", instance.Name, err)
				return
			}
			_ = output

			fmt.Printf("%-20s: ✅ uploaded\n", instance.Name)
		}(inst)
	}

	wg.Wait()
	fmt.Println("\nDone. Run 'gotoni cluster restart-lb' to use new binary.")
}

func runClusterStartTrace(cmd *cobra.Command, args []string) {
	instances, err := getClusterNodes()
	if err != nil {
		fmt.Printf("Failed to get instances: %v\n", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: 5 * time.Second}

	fmt.Println("Starting traces on all nodes...")
	for _, inst := range instances {
		resp, err := client.Post(
			fmt.Sprintf("http://%s:8000/lb/trace/start", inst.IP),
			"application/json",
			nil,
		)
		if err != nil {
			fmt.Printf("%-20s: ❌ %v\n", inst.Name, err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == 200 {
			fmt.Printf("%-20s: ✅ trace started\n", inst.Name)
		} else {
			fmt.Printf("%-20s: ❌ status %d\n", inst.Name, resp.StatusCode)
		}
	}
}

func runClusterStopTrace(cmd *cobra.Command, args []string) {
	outputDir, _ := cmd.Flags().GetString("output-dir")

	instances, err := getClusterNodes()
	if err != nil {
		fmt.Printf("Failed to get instances: %v\n", err)
		os.Exit(1)
	}

	// Create output directory
	traceDir := fmt.Sprintf("%s/traces_%s", outputDir, time.Now().Format("20060102_150405"))
	if err := os.MkdirAll(traceDir, 0755); err != nil {
		fmt.Printf("Failed to create dir: %v\n", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: 30 * time.Second}

	fmt.Printf("Collecting traces to %s/\n\n", traceDir)
	for _, inst := range instances {
		resp, err := client.Post(
			fmt.Sprintf("http://%s:8000/lb/trace/stop", inst.IP),
			"application/json",
			nil,
		)
		if err != nil {
			fmt.Printf("%-20s: ❌ %v\n", inst.Name, err)
			continue
		}

		// Save trace
		traceFile := fmt.Sprintf("%s/%s_trace.json", traceDir, inst.Name)
		file, _ := os.Create(traceFile)

		var events []interface{}
		if json.NewDecoder(resp.Body).Decode(&events) == nil {
			json.NewEncoder(file).Encode(events)
			fmt.Printf("%-20s: ✅ %d events saved\n", inst.Name, len(events))
		} else {
			fmt.Printf("%-20s: ❌ invalid trace\n", inst.Name)
		}

		file.Close()
		resp.Body.Close()
	}

	fmt.Printf("\nTraces saved to: %s/\n", traceDir)
}

func runClusterFlushCache(cmd *cobra.Command, args []string) {
	port, _ := cmd.Flags().GetInt("port")

	instances, err := getClusterNodes()
	if err != nil {
		fmt.Printf("Failed to get instances: %v\n", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: 10 * time.Second}

	fmt.Println("Flushing KV caches on all SGLang servers...")

	var wg sync.WaitGroup
	for _, inst := range instances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()

			flushURL := fmt.Sprintf("http://%s:%d/flush_cache", instance.IP, port)
			req, _ := http.NewRequest("POST", flushURL, nil)

			resp, err := client.Do(req)
			if err != nil {
				fmt.Printf("%-20s: ❌ %v\n", instance.Name, err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode == 200 {
				fmt.Printf("%-20s: ✅ cache flushed\n", instance.Name)
			} else {
				fmt.Printf("%-20s: ❌ status %d\n", instance.Name, resp.StatusCode)
			}
		}(inst)
	}

	wg.Wait()
	fmt.Println("\nDone.")
}

func runClusterStartTokenizer(cmd *cobra.Command, args []string) {
	// Delegate to the tokenizer package for consistency
	// This is equivalent to 'gotoni tokenizer start'
	httpClient := remote.NewHTTPClient()
	apiToken := remote.GetAPIToken()
	if apiToken == "" {
		fmt.Println("Error: LAMBDA_API_KEY not set")
		os.Exit(1)
	}

	cl, err := serve.GetCluster(httpClient, apiToken, "cluster")
	if err != nil {
		fmt.Printf("Failed to get cluster: %v\n", err)
		os.Exit(1)
	}

	if err := cl.Connect(); err != nil {
		fmt.Printf("Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer cl.Disconnect()

	if err := tokenizer.StartTokenizerSidecar(cl); err != nil {
		fmt.Printf("Start failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n✅ Done! Use 'gotoni tokenizer status' or 'gotoni cluster tokenizer-status' to verify.")
}

func runClusterTokenizerStatus(cmd *cobra.Command, args []string) {
	// Delegate to the tokenizer package for consistency
	// This is equivalent to 'gotoni tokenizer status'
	httpClient := remote.NewHTTPClient()
	apiToken := remote.GetAPIToken()
	if apiToken == "" {
		fmt.Println("Error: LAMBDA_API_KEY not set")
		os.Exit(1)
	}

	cl, err := serve.GetCluster(httpClient, apiToken, "cluster")
	if err != nil {
		fmt.Printf("Failed to get cluster: %v\n", err)
		os.Exit(1)
	}

	if err := cl.Connect(); err != nil {
		fmt.Printf("Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer cl.Disconnect()

	results, err := tokenizer.TokenizerStatus(cl)
	if err != nil {
		fmt.Printf("Status check failed: %v\n", err)
		os.Exit(1)
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
