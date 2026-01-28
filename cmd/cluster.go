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

	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/spf13/cobra"
)

var clusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Manage cluster nodes (LB, SGLang, binary uploads)",
	Long: `Cluster management commands for operating on all nodes.

Examples:
  gotoni cluster status          # Check all nodes
  gotoni cluster restart-lb      # Restart LBs on all nodes  
  gotoni cluster upload          # Build and upload gotoni binary
  gotoni cluster start-trace     # Start tracing on all nodes
  gotoni cluster stop-trace      # Stop tracing and collect results`,
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
pkill -f 'gotoni lb' 2>/dev/null || true
sleep 1
nohup /home/ubuntu/gotoni lb start \
  --listen-port 8000 \
  --local-port 8080 \
  --strategy %s \
  --max-concurrent %d \
  --ie-queue-indicator \
  --node-id %s \
  %s %s \
  > /home/ubuntu/lb.log 2>&1 &
sleep 2
curl -s http://localhost:8000/lb/health || echo 'FAILED'
`, strategy, maxConcurrent, instance.Name, thresholdArg, peersArg)

			output, err := sshMgr.ExecuteCommandWithTimeout(instance.IP, restartCmd, 30*time.Second)
			if err != nil || !strings.Contains(output, "healthy") {
				fmt.Printf("%-20s: ❌ restart failed\n", instance.Name)
			} else {
				fmt.Printf("%-20s: ✅ restarted\n", instance.Name)
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

			// SCP upload
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
	instances, err := getClusterNodes()
	if err != nil {
		fmt.Printf("Failed to get instances: %v\n", err)
		os.Exit(1)
	}

	sshMgr := remote.NewSSHClientManager()

	fmt.Println("Starting tokenizer-sidecar on all nodes...")

	startCmd := `
pkill -f tokenizer-sidecar 2>/dev/null || true
rm -f /tmp/tokenizer.sock 2>/dev/null || true
sleep 1
nohup /home/ubuntu/tokenizer-sidecar > /home/ubuntu/tokenizer.log 2>&1 &
sleep 3
if [ -S /tmp/tokenizer.sock ]; then
    echo "OK"
else
    echo "FAILED"
    cat /home/ubuntu/tokenizer.log | tail -5
fi
`

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

			if err := sshMgr.ConnectToInstance(instance.IP, sshKeyPath); err != nil {
				fmt.Printf("%-20s: ❌ SSH failed: %v\n", instance.Name, err)
				return
			}

			output, err := sshMgr.ExecuteCommandWithTimeout(instance.IP, startCmd, 30*time.Second)
			if err != nil {
				fmt.Printf("%-20s: ❌ %v\n", instance.Name, err)
				return
			}

			if strings.Contains(output, "OK") {
				fmt.Printf("%-20s: ✅ tokenizer running\n", instance.Name)
			} else {
				fmt.Printf("%-20s: ❌ %s\n", instance.Name, strings.TrimSpace(output))
			}
		}(inst)
	}

	wg.Wait()
	fmt.Println("\nDone.")
}

func runClusterTokenizerStatus(cmd *cobra.Command, args []string) {
	instances, err := getClusterNodes()
	if err != nil {
		fmt.Printf("Failed to get instances: %v\n", err)
		os.Exit(1)
	}

	sshMgr := remote.NewSSHClientManager()

	fmt.Println("Checking tokenizer-sidecar status...")
	fmt.Printf("\n%-20s %-12s %-40s\n", "NAME", "STATUS", "INFO")
	fmt.Println(strings.Repeat("-", 75))

	checkCmd := `
if [ -S /tmp/tokenizer.sock ]; then
    # Try to get health
    curl -s --unix-socket /tmp/tokenizer.sock http://localhost/health 2>/dev/null || echo '{"status":"socket_exists"}'
else
    echo '{"status":"not_running"}'
fi
`

	var wg sync.WaitGroup
	for _, inst := range instances {
		wg.Add(1)
		go func(instance remote.RunningInstance) {
			defer wg.Done()

			// Get SSH key using Lambda API key names + local file lookup
			sshKeyPath, err := remote.GetSSHKeyFileForInstance(&instance)
			if err != nil {
				fmt.Printf("%-20s %-12s %-40s\n", instance.Name, "❌", "no SSH key")
				return
			}

			if err := sshMgr.ConnectToInstance(instance.IP, sshKeyPath); err != nil {
				fmt.Printf("%-20s %-12s %-40s\n", instance.Name, "❌", "SSH failed")
				return
			}

			output, err := sshMgr.ExecuteCommandWithTimeout(instance.IP, checkCmd, 10*time.Second)
			if err != nil {
				fmt.Printf("%-20s %-12s %-40s\n", instance.Name, "❌", err.Error())
				return
			}

			var health map[string]interface{}
			if json.Unmarshal([]byte(strings.TrimSpace(output)), &health) == nil {
				status := "❌"
				info := "not running"
				if s, ok := health["status"].(string); ok {
					if s == "ok" {
						status = "✅"
						if model, ok := health["model"].(string); ok {
							info = model
						}
					} else if s == "socket_exists" {
						status = "⚠️"
						info = "socket exists but no response"
					}
				}
				fmt.Printf("%-20s %-12s %-40s\n", instance.Name, status, info)
			} else {
				fmt.Printf("%-20s %-12s %-40s\n", instance.Name, "❌", output)
			}
		}(inst)
	}

	wg.Wait()
}
