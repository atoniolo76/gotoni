/*
Copyright © 2025 ALESSIO TONIOLO

lb.go implements local load balancer commands.
These commands are designed to run on the instance itself (after gotoni binary is deployed).
*/
package cmd

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	serve "github.com/atoniolo76/gotoni/pkg/cluster"
	"github.com/atoniolo76/gotoni/pkg/config"
	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/spf13/cobra"
)

// lbCmd represents the lb command group
var lbCmd = &cobra.Command{
	Use:   "lb",
	Short: "Manage local load balancer processes",
	Long: `Load balancer management commands for running on cluster instances.

These commands are designed to be run on the instance itself after the gotoni
binary has been deployed. The load balancer proxies requests to a local backend
service and can forward requests to peer nodes when at capacity.

Examples:
  # Start load balancer with defaults (proxy localhost:8080, listen on :8000)
  gotoni lb start

  # Start with custom configuration
  gotoni lb start --local-port 8080 --listen-port 8000 --max-concurrent 10

  # Start with config file
  gotoni lb start --config lb-config.json

	# Check if load balancer is responding
	gotoni lb status

	# Stop load balancer (sends SIGTERM)
	gotoni lb stop`,
}

// lbStartCmd starts the load balancer
var lbStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the load balancer process",
	Long: `Start a load balancer that proxies requests to a local backend service.

The load balancer will:
- Accept incoming requests on the listen port (default: 8000)
- Forward requests to the local backend (default: localhost:8080)
- When at capacity, forward requests to healthy peer nodes
- Queue requests when all nodes are at capacity (if enabled)

The process runs in the foreground and can be stopped with Ctrl+C or SIGTERM.`,
	Run: runLBStart,
}

// lbStopCmd stops the load balancer
var lbStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the load balancer process",
	Long:  `Stop a running load balancer by sending SIGTERM to the process.`,
	Run:   runLBStop,
}

// lbStatusCmd checks if load balancer is responding
var lbStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check if load balancer is responding",
	Long: `Check if the load balancer is running and responding to requests.

Examples:
  # Check status on all cluster instances (recommended)
  gotoni lb status --all

  # Check status on localhost
  gotoni lb status

  # Check specific host
  gotoni lb status --host 192.168.1.100`,
	Run: runLBStatus,
}

// lbPolicyCmd switches or shows load balancer policy
var lbPolicyCmd = &cobra.Command{
	Use:   "policy [policy-name]",
	Short: "Get or set load balancer policy",
	Long: `Get the current policy or switch to a new one on all cluster instances.

Available policies: least-loaded, prefix-tree, gorgo

Examples:
  # Show current policy on all running instances (recommended)
  gotoni lb policy --all

  # Switch to gorgo policy on all instances
  gotoni lb policy gorgo --all

  # Show policy on specific hosts
  gotoni lb policy --host 192.168.1.100

  # Switch policy on specific hosts
  gotoni lb policy gorgo --host 192.168.1.100,192.168.1.101`,
	Run: runLBPolicy,
}

// lbClearCacheCmd clears the prefix tree cache
var lbClearCacheCmd = &cobra.Command{
	Use:   "clear-cache",
	Short: "Clear the prefix tree cache on load balancer(s)",
	Long: `Clear the KV cache routing prefix tree on load balancer(s).

This resets the prefix tree used by prefix-tree and gorgo policies,
causing all requests to be routed fresh without historical prefix matching.

Examples:
  # Clear cache on a single host
  gotoni lb clear-cache --host 192.168.1.100

  # Clear cache on multiple hosts
  gotoni lb clear-cache --host 192.168.1.100,192.168.1.101,192.168.1.102`,
	Run: runLBClearCache,
}

// lbPeersCmd manages peers on load balancer(s)
var lbPeersCmd = &cobra.Command{
	Use:   "peers [add|remove] [peer-addresses...]",
	Short: "List, add, or remove peers on load balancer(s)",
	Long: `Manage peer nodes on running load balancer(s).

With no arguments, lists current peers. Use 'add' or 'remove' subcommands
to modify the peer list.

Examples:
  # List peers on a host
  gotoni lb peers --host 192.168.1.100

  # Add peers to a single LB
  gotoni lb peers add 192.168.1.101:8000 192.168.1.102:8000 --host 192.168.1.100

  # Add peers to all LBs (mesh configuration)
  gotoni lb peers add-mesh --host 192.168.1.100,192.168.1.101,192.168.1.102

  # Remove a peer
  gotoni lb peers remove 192.168.1.101:8000 --host 192.168.1.100`,
	Run: runLBPeers,
}

// lbPeersAddMeshCmd configures all LBs as a mesh (each knows about all others)
var lbPeersAddMeshCmd = &cobra.Command{
	Use:   "add-mesh",
	Short: "Configure all LBs as a full mesh (each knows about all others)",
	Long: `Add all other hosts as peers to each load balancer.

This is useful for setting up a cluster where each LB should know about
all other LBs for load balancing.

Example:
  gotoni lb peers add-mesh --host 192.168.1.100,192.168.1.101,192.168.1.102`,
	Run: runLBPeersAddMesh,
}

func init() {
	rootCmd.AddCommand(lbCmd)
	lbCmd.AddCommand(lbStartCmd)
	lbCmd.AddCommand(lbStopCmd)
	lbCmd.AddCommand(lbStatusCmd)
	lbCmd.AddCommand(lbPolicyCmd)
	lbCmd.AddCommand(lbClearCacheCmd)
	lbCmd.AddCommand(lbPeersCmd)
	lbPeersCmd.AddCommand(lbPeersAddMeshCmd)

	// Flags for lb start - defaults from pkg/config/constants.go
	lbStartCmd.Flags().String("config", "", "Path to JSON config file")
	lbStartCmd.Flags().Int("local-port", config.DefaultApplicationPort, "Port of the local SGLang backend service")
	lbStartCmd.Flags().Int("listen-port", config.DefaultLoadBalancerPort, "Port for the load balancer to listen on")
	lbStartCmd.Flags().Int("max-concurrent", config.DefaultMaxConcurrentRequests, "Max concurrent requests before forwarding to peers")
	lbStartCmd.Flags().Bool("queue-enabled", config.DefaultQueueEnabled, "Enable request queuing when all nodes at capacity")
	lbStartCmd.Flags().Duration("queue-timeout", config.DefaultQueueTimeout, "Queue timeout")
	lbStartCmd.Flags().Int("max-queue", config.DefaultMaxQueueSize, "Max requests in queue before rejecting (0=unlimited)")
	lbStartCmd.Flags().Duration("request-timeout", config.DefaultRequestTimeout, "Request timeout for forwarded requests")
	lbStartCmd.Flags().StringSlice("peers", []string{}, "Peer addresses in format ip:port (can specify multiple)")
	lbStartCmd.Flags().String("pid-file", "/tmp/gotoni-lb.pid", "Path to PID file")
	lbStartCmd.Flags().String("strategy", config.DefaultStrategy, "Load balancing strategy (least-loaded, prefix-tree, gorgo)")
	lbStartCmd.Flags().String("node-id", "", "Unique identifier for this node in the cluster")

	// Selective pushing flags
	lbStartCmd.Flags().Bool("ie-queue-indicator", config.DefaultUseIEQueueIndicator, "Use SGLang's internal queue as capacity indicator")
	lbStartCmd.Flags().Int("running-threshold", config.DefaultRunningReqsThreshold, "Forward when running_reqs >= this (0=disabled, overrides ie-queue)")

	// Cluster identity
	lbStartCmd.Flags().String("cluster-name", "default", "Cluster name for tracing")

	// Flags for lb status
	lbStatusCmd.Flags().Bool("all", false, "Check all running instances in cluster")
	lbStatusCmd.Flags().Int("port", config.DefaultLoadBalancerPort, "Load balancer port to check")
	lbStatusCmd.Flags().StringSlice("host", []string{}, "Load balancer host(s) to check (comma-separated)")

	// Flags for lb stop
	lbStopCmd.Flags().String("pid-file", "/tmp/gotoni-lb.pid", "Path to PID file")

	// Flags for lb policy
	lbPolicyCmd.Flags().Bool("all", false, "Apply to all running instances in cluster")
	lbPolicyCmd.Flags().StringSlice("host", []string{}, "Load balancer host(s) to target (comma-separated)")
	lbPolicyCmd.Flags().Int("port", config.DefaultLoadBalancerPort, "Load balancer port")

	// Flags for lb clear-cache
	lbClearCacheCmd.Flags().Bool("all", false, "Apply to all running instances in cluster")
	lbClearCacheCmd.Flags().StringSlice("host", []string{}, "Load balancer host(s) to target (comma-separated)")
	lbClearCacheCmd.Flags().Int("port", config.DefaultLoadBalancerPort, "Load balancer port")

	// Flags for lb peers
	lbPeersCmd.Flags().Bool("all", false, "Apply to all running instances in cluster")
	lbPeersCmd.Flags().StringSlice("host", []string{}, "Load balancer host(s) to target (comma-separated)")
	lbPeersCmd.Flags().Int("port", config.DefaultLoadBalancerPort, "Load balancer port")

	// Flags for lb peers add-mesh
	lbPeersAddMeshCmd.Flags().StringSlice("host", []string{}, "All LB hosts to configure as mesh (comma-separated)")
	lbPeersAddMeshCmd.Flags().Int("port", config.DefaultLoadBalancerPort, "Load balancer port")
}

func runLBStart(cmd *cobra.Command, args []string) {
	// Load config from file or flags
	config := serve.DefaultLoadBalancerConfig()

	configFile, _ := cmd.Flags().GetString("config")
	if configFile != "" {
		data, err := os.ReadFile(configFile)
		if err != nil {
			log.Fatalf("Failed to read config file: %v", err)
		}
		if err := json.Unmarshal(data, config); err != nil {
			log.Fatalf("Failed to parse config file: %v", err)
		}
		fmt.Printf("Loaded config from %s\n", configFile)
	}

	// Override with flags if provided
	if cmd.Flags().Changed("local-port") {
		config.ApplicationPort, _ = cmd.Flags().GetInt("local-port")
	}
	if cmd.Flags().Changed("listen-port") {
		config.LoadBalancerPort, _ = cmd.Flags().GetInt("listen-port")
	}
	if cmd.Flags().Changed("max-concurrent") {
		config.MaxConcurrentRequests, _ = cmd.Flags().GetInt("max-concurrent")
	}
	if cmd.Flags().Changed("queue-enabled") {
		config.QueueEnabled, _ = cmd.Flags().GetBool("queue-enabled")
	}
	if cmd.Flags().Changed("queue-timeout") {
		config.QueueTimeout, _ = cmd.Flags().GetDuration("queue-timeout")
	}
	if cmd.Flags().Changed("max-queue") {
		config.MaxQueueSize, _ = cmd.Flags().GetInt("max-queue")
	}
	if cmd.Flags().Changed("request-timeout") {
		config.RequestTimeout, _ = cmd.Flags().GetDuration("request-timeout")
	}

	// Get strategy and node-id (used for cluster mode, logged for debugging)
	strategy, _ := cmd.Flags().GetString("strategy")
	nodeID, _ := cmd.Flags().GetString("node-id")

	// Get IE queue indicator setting
	ieQueueIndicator, _ := cmd.Flags().GetBool("ie-queue-indicator")
	config.UseIEQueueIndicator = ieQueueIndicator

	// Get running threshold (overrides IE queue if > 0)
	runningThreshold, _ := cmd.Flags().GetInt("running-threshold")
	config.RunningReqsThreshold = runningThreshold

	// Set node/cluster identity for tracing
	clusterName, _ := cmd.Flags().GetString("cluster-name")
	config.NodeID = nodeID
	config.ClusterName = clusterName

	// Set strategy based on flag
	switch strategy {
	case "least-loaded":
		config.Strategy = &serve.LeastLoadedPolicy{}
	case "prefix-tree":
		config.Strategy = serve.NewPrefixTreePolicy()
	case "gorgo":
		config.Strategy = &serve.GORGOPolicy{}
	default:
		config.Strategy = &serve.LeastLoadedPolicy{}
	}

	// Write PID file
	pidFile, _ := cmd.Flags().GetString("pid-file")
	if pidFile != "" {
		if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644); err != nil {
			log.Printf("Warning: Failed to write PID file: %v", err)
		} else {
			defer os.Remove(pidFile)
		}
	}

	// Create load balancer (self=nil for standalone usage)
	lb := serve.NewLoadBalancer(config, nil)

	// Add peers if specified
	peers, _ := cmd.Flags().GetStringSlice("peers")
	for _, peer := range peers {
		// Parse peer address (format: ip:port or just ip)
		parts := strings.Split(peer, ":")
		peerIP := parts[0]
		peerPort := 8000 // default
		if len(parts) > 1 {
			if p, err := strconv.Atoi(parts[1]); err == nil {
				peerPort = p
			}
		}
		lb.AddPeerByIP(peerIP, peerPort)
	}

	// Setup signal handling for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println("\nShutting down load balancer...")
		lb.Stop()
		os.Exit(0)
	}()

	// Print startup info
	fmt.Printf("Starting load balancer:\n")
	fmt.Printf("  Node ID:          %s\n", nodeID)
	fmt.Printf("  Listen port:      %d\n", config.LoadBalancerPort)
	fmt.Printf("  Local SGLang:     localhost:%d\n", config.ApplicationPort)
	fmt.Printf("  Max concurrent:   %d\n", config.MaxConcurrentRequests)
	fmt.Printf("  Queue enabled:    %v\n", config.QueueEnabled)
	fmt.Printf("  Strategy:         %s\n", strategy)
	if config.RunningReqsThreshold > 0 {
		fmt.Printf("  Running threshold: %d (forward when running_reqs >= %d)\n", config.RunningReqsThreshold, config.RunningReqsThreshold)
	} else {
		fmt.Printf("  IE queue mode:    %v (forward when SGLang queue > 0)\n", config.UseIEQueueIndicator)
	}
	fmt.Printf("  Metrics polling:  %v\n", config.MetricsEnabled)
	fmt.Printf("  PID file:         %s\n", pidFile)
	fmt.Println()

	// Start the load balancer (blocks)
	if err := lb.ListenAndServe(); err != nil {
		log.Fatalf("Load balancer error: %v", err)
	}
}

func runLBStop(cmd *cobra.Command, args []string) {
	pidFile, _ := cmd.Flags().GetString("pid-file")

	// Read PID from file
	data, err := os.ReadFile(pidFile)
	if err != nil {
		log.Fatalf("Failed to read PID file %s: %v", pidFile, err)
	}

	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		log.Fatalf("Failed to parse PID from file: %v", err)
	}

	// Find the process
	process, err := os.FindProcess(pid)
	if err != nil {
		log.Fatalf("Failed to find process %d: %v", pid, err)
	}

	// Send SIGTERM
	if err := process.Signal(syscall.SIGTERM); err != nil {
		log.Fatalf("Failed to send SIGTERM to process %d: %v", pid, err)
	}

	fmt.Printf("Sent SIGTERM to load balancer process (PID: %d)\n", pid)
}

func runLBStatus(cmd *cobra.Command, args []string) {
	useAll, _ := cmd.Flags().GetBool("all")
	hosts, _ := cmd.Flags().GetStringSlice("host")
	port, _ := cmd.Flags().GetInt("port")

	client := &http.Client{Timeout: 5 * time.Second}

	// Get hosts from cluster if --all is specified
	var allHosts []string
	if useAll {
		apiToken := remote.GetAPIToken()
		if apiToken == "" {
			log.Fatal("LAMBDA_API_KEY not set. Required for --all flag.")
		}
		httpClient := remote.NewHTTPClient()
		instances, err := remote.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Failed to list instances: %v", err)
		}
		for _, inst := range instances {
			allHosts = append(allHosts, inst.IP)
		}
		if len(allHosts) == 0 {
			log.Fatal("No running instances found")
		}
		fmt.Printf("Checking LB status on %d instances...\n\n", len(allHosts))
	} else if len(hosts) > 0 {
		// Expand comma-separated hosts
		for _, h := range hosts {
			for _, host := range strings.Split(h, ",") {
				host = strings.TrimSpace(host)
				if host != "" {
					allHosts = append(allHosts, host)
				}
			}
		}
	} else {
		// Default to localhost if nothing specified
		allHosts = []string{"localhost"}
	}

	healthyCount := 0
	for _, host := range allHosts {
		// Try to get LB status endpoint
		statusURL := fmt.Sprintf("http://%s:%d/lb/status", host, port)
		resp, err := client.Get(statusURL)
		if err != nil {
			fmt.Printf("❌ %s: NOT running or unreachable\n", host)
			continue
		}
		defer resp.Body.Close()

		var status map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			fmt.Printf("⚠️  %s: running but invalid response\n", host)
			healthyCount++
			continue
		}

		// Extract useful info from status
		running := int64(0)
		waiting := int64(0)
		peers := 0
		healthy := status["healthy"] == true

		if r, ok := status["running_reqs"].(float64); ok {
			running = int64(r)
		}
		if w, ok := status["waiting_reqs"].(float64); ok {
			waiting = int64(w)
		}
		if p, ok := status["peer_count"].(float64); ok {
			peers = int(p)
		}

		statusIcon := "✅"
		if !healthy {
			statusIcon = "⚠️"
		}
		fmt.Printf("%s %s: running=%d, waiting=%d, peers=%d\n", statusIcon, host, running, waiting, peers)
		healthyCount++
	}

	if len(allHosts) > 1 {
		fmt.Printf("\nSummary: %d/%d LBs responding\n", healthyCount, len(allHosts))
	}
}

func runLBPolicy(cmd *cobra.Command, args []string) {
	useAll, _ := cmd.Flags().GetBool("all")
	hosts, _ := cmd.Flags().GetStringSlice("host")
	port, _ := cmd.Flags().GetInt("port")

	client := &http.Client{Timeout: 5 * time.Second}

	// Get hosts from cluster if --all is specified
	var allHosts []string
	if useAll {
		apiToken := remote.GetAPIToken()
		if apiToken == "" {
			log.Fatal("LAMBDA_API_KEY not set. Required for --all flag.")
		}
		httpClient := remote.NewHTTPClient()
		instances, err := remote.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Failed to list instances: %v", err)
		}
		for _, inst := range instances {
			allHosts = append(allHosts, inst.IP)
		}
		if len(allHosts) == 0 {
			log.Fatal("No running instances found")
		}
		fmt.Printf("Targeting %d instances...\n\n", len(allHosts))
	} else if len(hosts) > 0 {
		// Expand comma-separated hosts
		for _, h := range hosts {
			for _, host := range strings.Split(h, ",") {
				host = strings.TrimSpace(host)
				if host != "" {
					allHosts = append(allHosts, host)
				}
			}
		}
	} else {
		// Default to localhost if nothing specified
		allHosts = []string{"localhost"}
	}

	for _, host := range allHosts {
		policyURL := fmt.Sprintf("http://%s:%d/lb/policy", host, port)

		if len(args) == 0 {
			// GET current policy
			resp, err := client.Get(policyURL)
			if err != nil {
				fmt.Printf("❌ %s: failed to connect - %v\n", host, err)
				continue
			}
			defer resp.Body.Close()

			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				fmt.Printf("❌ %s: failed to parse response - %v\n", host, err)
				continue
			}

			fmt.Printf("✅ %s: policy=%s (available: %v)\n",
				host, result["current"], result["available"])
		} else {
			// SET new policy
			newPolicy := args[0]
			setPolicyURL := fmt.Sprintf("%s?set=%s", policyURL, newPolicy)

			resp, err := client.Get(setPolicyURL)
			if err != nil {
				fmt.Printf("❌ %s: failed to connect - %v\n", host, err)
				continue
			}
			defer resp.Body.Close()

			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				fmt.Printf("❌ %s: failed to parse response - %v\n", host, err)
				continue
			}

			if result["success"] == true {
				fmt.Printf("✅ %s: %s -> %s\n", host, result["old_policy"], result["new_policy"])
			} else {
				fmt.Printf("❌ %s: %v\n", host, result["error"])
			}
		}
	}
}

func runLBClearCache(cmd *cobra.Command, args []string) {
	useAll, _ := cmd.Flags().GetBool("all")
	hosts, _ := cmd.Flags().GetStringSlice("host")
	port, _ := cmd.Flags().GetInt("port")

	client := &http.Client{Timeout: 5 * time.Second}

	// Get hosts from cluster if --all is specified
	var allHosts []string
	if useAll {
		apiToken := remote.GetAPIToken()
		if apiToken == "" {
			log.Fatal("LAMBDA_API_KEY not set. Required for --all flag.")
		}
		httpClient := remote.NewHTTPClient()
		instances, err := remote.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Failed to list instances: %v", err)
		}
		for _, inst := range instances {
			allHosts = append(allHosts, inst.IP)
		}
		if len(allHosts) == 0 {
			log.Fatal("No running instances found")
		}
		fmt.Printf("Clearing cache on %d instances...\n\n", len(allHosts))
	} else if len(hosts) > 0 {
		// Expand comma-separated hosts
		for _, h := range hosts {
			for _, host := range strings.Split(h, ",") {
				host = strings.TrimSpace(host)
				if host != "" {
					allHosts = append(allHosts, host)
				}
			}
		}
	} else {
		// Default to localhost if nothing specified
		allHosts = []string{"localhost"}
	}

	for _, host := range allHosts {
		clearURL := fmt.Sprintf("http://%s:%d/lb/cache/clear", host, port)

		resp, err := client.Post(clearURL, "application/json", nil)
		if err != nil {
			fmt.Printf("❌ %s: failed to connect - %v\n", host, err)
			continue
		}
		defer resp.Body.Close()

		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			fmt.Printf("❌ %s: failed to parse response - %v\n", host, err)
			continue
		}

		if result["success"] == true {
			nodesCleared := result["nodes_cleared"]
			strategy := result["strategy"]
			fmt.Printf("✅ %s: cleared %v nodes (strategy: %s)\n", host, nodesCleared, strategy)
		} else {
			fmt.Printf("⚠️  %s: %v (strategy: %v)\n", host, result["message"], result["strategy"])
		}
	}
}

func runLBPeers(cmd *cobra.Command, args []string) {
	useAll, _ := cmd.Flags().GetBool("all")
	hosts, _ := cmd.Flags().GetStringSlice("host")
	port, _ := cmd.Flags().GetInt("port")

	client := &http.Client{Timeout: 5 * time.Second}

	// Get hosts from cluster if --all is specified
	var allHosts []string
	if useAll {
		apiToken := remote.GetAPIToken()
		if apiToken == "" {
			log.Fatal("LAMBDA_API_KEY not set. Required for --all flag.")
		}
		httpClient := remote.NewHTTPClient()
		instances, err := remote.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Failed to list instances: %v", err)
		}
		for _, inst := range instances {
			allHosts = append(allHosts, inst.IP)
		}
		if len(allHosts) == 0 {
			log.Fatal("No running instances found")
		}
		fmt.Printf("Checking peers on %d instances...\n\n", len(allHosts))
	} else if len(hosts) > 0 {
		// Expand comma-separated hosts
		for _, h := range hosts {
			for _, host := range strings.Split(h, ",") {
				host = strings.TrimSpace(host)
				if host != "" {
					allHosts = append(allHosts, host)
				}
			}
		}
	} else {
		// Default to localhost if nothing specified
		allHosts = []string{"localhost"}
	}

	// Determine action
	action := "list"
	var peerAddrs []string
	if len(args) > 0 {
		if args[0] == "add" || args[0] == "remove" {
			action = args[0]
			peerAddrs = args[1:]
		} else {
			// Assume adding peers if first arg looks like an address
			action = "add"
			peerAddrs = args
		}
	}

	for _, host := range allHosts {
		peersURL := fmt.Sprintf("http://%s:%d/lb/peers", host, port)

		switch action {
		case "list":
			resp, err := client.Get(peersURL)
			if err != nil {
				fmt.Printf("❌ %s: failed to connect - %v\n", host, err)
				continue
			}
			defer resp.Body.Close()

			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				fmt.Printf("❌ %s: failed to parse response - %v\n", host, err)
				continue
			}

			peers := result["peers"].([]interface{})
			fmt.Printf("📍 %s: %d peers\n", host, len(peers))
			for _, p := range peers {
				peer := p.(map[string]interface{})
				status := "✅"
				if peer["healthy"] != true {
					status = "❌"
				}
				fmt.Printf("   %s %s:%v (running=%v, waiting=%v)\n",
					status, peer["ip"], peer["port"], peer["running_reqs"], peer["waiting_reqs"])
			}

		case "add":
			for _, peerAddr := range peerAddrs {
				parts := strings.Split(peerAddr, ":")
				peerIP := parts[0]
				peerPort := port // default to same port
				if len(parts) > 1 {
					if p, err := strconv.Atoi(parts[1]); err == nil {
						peerPort = p
					}
				}

				body := fmt.Sprintf(`{"ip": "%s", "port": %d}`, peerIP, peerPort)
				resp, err := client.Post(peersURL, "application/json", strings.NewReader(body))
				if err != nil {
					fmt.Printf("❌ %s: failed to add peer %s - %v\n", host, peerAddr, err)
					continue
				}
				defer resp.Body.Close()

				var result map[string]interface{}
				json.NewDecoder(resp.Body).Decode(&result)
				fmt.Printf("✅ %s: added peer %s:%d (status: %v)\n", host, peerIP, peerPort, result["status"])
			}

		case "remove":
			for _, peerAddr := range peerAddrs {
				parts := strings.Split(peerAddr, ":")
				peerIP := parts[0]
				peerPort := port
				if len(parts) > 1 {
					if p, err := strconv.Atoi(parts[1]); err == nil {
						peerPort = p
					}
				}

				body := fmt.Sprintf(`{"ip": "%s", "port": %d}`, peerIP, peerPort)
				req, _ := http.NewRequest(http.MethodDelete, peersURL, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					fmt.Printf("❌ %s: failed to remove peer %s - %v\n", host, peerAddr, err)
					continue
				}
				defer resp.Body.Close()

				var result map[string]interface{}
				json.NewDecoder(resp.Body).Decode(&result)
				fmt.Printf("✅ %s: removed peer %s (status: %v)\n", host, peerAddr, result["status"])
			}
		}
	}
}

func runLBPeersAddMesh(cmd *cobra.Command, args []string) {
	hosts, _ := cmd.Flags().GetStringSlice("host")
	port, _ := cmd.Flags().GetInt("port")

	client := &http.Client{Timeout: 5 * time.Second}

	// Expand comma-separated hosts
	var allHosts []string
	for _, h := range hosts {
		for _, host := range strings.Split(h, ",") {
			host = strings.TrimSpace(host)
			if host != "" {
				allHosts = append(allHosts, host)
			}
		}
	}

	if len(allHosts) < 2 {
		fmt.Println("❌ Need at least 2 hosts for mesh configuration")
		fmt.Println("   Usage: gotoni lb peers add-mesh --host ip1,ip2,ip3")
		return
	}

	fmt.Printf("🔗 Configuring %d LBs as full mesh...\n", len(allHosts))

	// For each host, add all other hosts as peers
	for _, host := range allHosts {
		peersURL := fmt.Sprintf("http://%s:%d/lb/peers", host, port)
		addedCount := 0

		for _, peerHost := range allHosts {
			if peerHost == host {
				continue // Skip self
			}

			body := fmt.Sprintf(`{"ip": "%s", "port": %d}`, peerHost, port)
			resp, err := client.Post(peersURL, "application/json", strings.NewReader(body))
			if err != nil {
				fmt.Printf("  ❌ %s: failed to add peer %s - %v\n", host, peerHost, err)
				continue
			}
			resp.Body.Close()
			addedCount++
		}

		fmt.Printf("  ✅ %s: added %d peers\n", host, addedCount)
	}

	fmt.Println("🔗 Mesh configuration complete!")
}
