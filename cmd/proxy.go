/*
Copyright © 2025 ALESSIO TONIOLO

proxy.go implements centralized proxy commands for routing to SGLang server pools.
Unlike the distributed lb command (which runs on each node), the proxy runs centrally
and has a global view of all requests across all servers.
*/
package cmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/atoniolo76/gotoni/pkg/proxy"
	"github.com/atoniolo76/gotoni/pkg/remote"
	"github.com/spf13/cobra"
)

// proxyCmd represents the proxy command group
var proxyCmd = &cobra.Command{
	Use:   "proxy",
	Short: "Manage centralized proxy for SGLang server pools",
	Long: `Centralized proxy management for routing requests to SGLang server pools.

Unlike the distributed load balancer (lb) which runs on each node, the proxy
runs centrally and has a GLOBAL view of all requests across all servers.

Key differences from 'gotoni lb':
  - Proxies directly to SGLang endpoints (not to other load balancers)
  - Tracks ALL running/queued requests globally (not just local)
  - Single point of routing decision with complete cluster state
  - Auto-discovers servers from Lambda cloud API

Cost calculation (GORGO-style):
  Cost = (queued_tokens * ms_per_token) + (running_tokens * ms_per_token * 0.5) + latency

Examples:
  # Start proxy with auto-discovery from Lambda cloud
  gotoni proxy start --auto-discover

  # Start proxy with explicit servers
  gotoni proxy start --servers 192.168.1.100:8080,192.168.1.101:8080

  # Check proxy status
  gotoni proxy status

  # Clear prefix cache on proxy
  gotoni proxy clear-cache`,
}

// proxyStartCmd starts the centralized proxy
var proxyStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the centralized proxy",
	Long: `Start a centralized proxy that routes requests to a pool of SGLang servers.

The proxy will:
- Accept incoming requests on the listen port (default: 8000)
- Route to the optimal SGLang server based on GORGO cost calculation
- Track all running/queued requests globally
- Queue requests when all servers are at capacity

Auto-discovery:
  With --auto-discover, the proxy will fetch all running instances from
  the Lambda cloud API and add their SGLang endpoints to the pool.`,
	Run: runProxyStart,
}

// proxyStopCmd stops the proxy
var proxyStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the centralized proxy",
	Long:  `Stop a running proxy by sending SIGTERM to the process.`,
	Run:   runProxyStop,
}

// proxyStatusCmd checks proxy status
var proxyStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check proxy status",
	Long:  `Check if the proxy is running and get detailed status information.`,
	Run:   runProxyStatus,
}

// proxyServersCmd manages servers in the pool
var proxyServersCmd = &cobra.Command{
	Use:   "servers [add|remove] [addresses...]",
	Short: "List, add, or remove servers from the pool",
	Long: `Manage SGLang servers in the proxy's pool.

Examples:
  # List all servers
  gotoni proxy servers

  # Add servers to pool
  gotoni proxy servers add 192.168.1.100:8080 192.168.1.101:8080

  # Remove a server
  gotoni proxy servers remove 192.168.1.100:8080`,
	Run: runProxyServers,
}

// proxyTuneCmd adjusts GORGO tuning parameters
var proxyTuneCmd = &cobra.Command{
	Use:   "tune",
	Short: "Get or set GORGO tuning parameters",
	Long: `Get or set tuning parameters for GORGO cost calculation.

Parameters:
  --ms-per-token        Estimated prefill time per token in ms
  --running-cost-factor Weight for running requests (qhat, 0.0-1.0)

Examples:
  # Show current parameters
  gotoni proxy tune

  # Set parameters
  gotoni proxy tune --ms-per-token 0.1 --running-cost-factor 0.3`,
	Run: runProxyTune,
}

// proxyClearCacheCmd clears the prefix tree cache
var proxyClearCacheCmd = &cobra.Command{
	Use:   "clear-cache",
	Short: "Clear the prefix tree cache",
	Long: `Clear the KV cache routing prefix tree on the proxy.

This resets the prefix tree used for GORGO routing decisions,
causing all requests to be routed fresh without historical prefix matching.`,
	Run: runProxyClearCache,
}

// proxyLatencyCmd shows latency to all servers
var proxyLatencyCmd = &cobra.Command{
	Use:   "latency",
	Short: "Show latency to all servers in the pool",
	Long: `Display network latencies from the proxy to all SGLang servers.

Latencies are measured via dedicated probing and include:
- Current (latest) latency
- Average (EWMA) latency
- Min/Max observed latency`,
	Run: runProxyLatency,
}

// proxyDeployCmd deploys and starts proxy on a remote instance
var proxyDeployCmd = &cobra.Command{
	Use:   "deploy <instance-name>",
	Short: "Deploy and start proxy on a remote instance",
	Long: `Build, upload, and start the proxy on a remote Lambda instance.

This command will:
1. Build the gotoni binary for Linux
2. Upload it to the specified instance
3. Start the proxy on that instance
4. Auto-discover and add all SGLang servers from other instances

Example:
  gotoni proxy deploy west_2 --sglang-port 8080`,
	Args: cobra.ExactArgs(1),
	Run:  runProxyDeploy,
}

func init() {
	rootCmd.AddCommand(proxyCmd)
	proxyCmd.AddCommand(proxyStartCmd)
	proxyCmd.AddCommand(proxyStopCmd)
	proxyCmd.AddCommand(proxyStatusCmd)
	proxyCmd.AddCommand(proxyServersCmd)
	proxyCmd.AddCommand(proxyTuneCmd)
	proxyCmd.AddCommand(proxyClearCacheCmd)
	proxyCmd.AddCommand(proxyLatencyCmd)
	proxyCmd.AddCommand(proxyDeployCmd)

	// Persistent flags for all proxy subcommands (except start/stop)
	proxyCmd.PersistentFlags().String("remote", "", "Remote instance name or ID - resolves from Lambda API")

	// Flags for proxy start
	proxyStartCmd.Flags().String("config", "", "Path to JSON config file")
	proxyStartCmd.Flags().Int("listen-port", 8000, "Port for the proxy to listen on")
	proxyStartCmd.Flags().Int("sglang-port", 8080, "SGLang server port on discovered instances")
	proxyStartCmd.Flags().Duration("request-timeout", 60*time.Second, "Request timeout")
	proxyStartCmd.Flags().Bool("queue-enabled", true, "Enable request queuing")
	proxyStartCmd.Flags().Duration("queue-timeout", 60*time.Second, "Queue timeout")
	proxyStartCmd.Flags().Int("max-queue", 10000, "Max requests in queue")
	proxyStartCmd.Flags().StringSlice("servers", []string{}, "Server addresses in format ip:port")
	proxyStartCmd.Flags().Bool("auto-discover", false, "Auto-discover servers from Lambda cloud API")
	proxyStartCmd.Flags().String("pid-file", "/tmp/gotoni-proxy.pid", "Path to PID file")

	// GORGO tuning parameters
	proxyStartCmd.Flags().Float64("ms-per-token", 0.094, "Estimated prefill time per token in ms")
	proxyStartCmd.Flags().Float64("running-cost-factor", 0.5, "Weight for running requests (qhat)")

	// Metrics/latency polling
	proxyStartCmd.Flags().Duration("metrics-interval", 500*time.Millisecond, "Metrics polling interval")
	proxyStartCmd.Flags().Duration("latency-interval", 100*time.Millisecond, "Latency probing interval")
	proxyStartCmd.Flags().String("metrics-endpoint", "/metrics", "SGLang metrics endpoint")

	// Flags for proxy stop
	proxyStopCmd.Flags().String("pid-file", "/tmp/gotoni-proxy.pid", "Path to PID file")

	// Flags for proxy status
	proxyStatusCmd.Flags().String("host", "localhost", "Proxy host to check")
	proxyStatusCmd.Flags().Int("port", 8000, "Proxy port")

	// Flags for proxy servers
	proxyServersCmd.Flags().String("host", "localhost", "Proxy host")
	proxyServersCmd.Flags().Int("port", 8000, "Proxy port")

	// Flags for proxy tune
	proxyTuneCmd.Flags().String("host", "localhost", "Proxy host")
	proxyTuneCmd.Flags().Int("port", 8000, "Proxy port")
	proxyTuneCmd.Flags().Float64("ms-per-token", 0, "Set ms_per_token (0 = don't change)")
	proxyTuneCmd.Flags().Float64("running-cost-factor", 0, "Set running_cost_factor (0 = don't change)")

	// Flags for proxy clear-cache
	proxyClearCacheCmd.Flags().String("host", "localhost", "Proxy host")
	proxyClearCacheCmd.Flags().Int("port", 8000, "Proxy port")

	// Flags for proxy latency
	proxyLatencyCmd.Flags().String("host", "localhost", "Proxy host")
	proxyLatencyCmd.Flags().Int("port", 8000, "Proxy port")

	// Flags for proxy deploy
	proxyDeployCmd.Flags().Int("listen-port", 8000, "Port for the proxy to listen on")
	proxyDeployCmd.Flags().Int("sglang-port", 8080, "SGLang server port on discovered instances")
	proxyDeployCmd.Flags().Float64("ms-per-token", 0.094, "Estimated prefill time per token in ms")
	proxyDeployCmd.Flags().Float64("running-cost-factor", 0.5, "Weight for running requests (qhat)")
	proxyDeployCmd.Flags().Bool("skip-build", false, "Skip building the binary (use existing /tmp/gotoni-linux)")
	proxyDeployCmd.Flags().Bool("auto-add-servers", true, "Auto-discover and add SGLang servers from other instances")
}

// resolveProxyHost resolves the --remote flag to an actual IP address using Lambda API.
func resolveProxyHost(cmd *cobra.Command) (string, int) {
	remoteAlias, _ := cmd.Flags().GetString("remote")
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")

	if remoteAlias == "" {
		return host, port
	}

	// Resolve from Lambda API (ground truth)
	instance, err := resolveInstanceByName(remoteAlias)
	if err != nil {
		fmt.Printf("Warning: Could not resolve '%s': %v\n", remoteAlias, err)
		return remoteAlias, port
	}

	fmt.Printf("Resolved '%s' to %s (instance: %s)\n", remoteAlias, instance.IP, instance.Name)
	return instance.IP, port
}

// resolveInstanceByName resolves an instance name/ID to a RunningInstance using Lambda API
func resolveInstanceByName(nameOrID string) (*remote.RunningInstance, error) {
	apiToken := remote.GetAPIToken()
	if apiToken == "" {
		return nil, fmt.Errorf("LAMBDA_API_KEY not set")
	}

	httpClient := remote.NewHTTPClient()
	return remote.ResolveInstance(httpClient, apiToken, nameOrID)
}

// getSSHManagerForInstance creates an SSH client manager and connects to an instance
func getSSHManagerForInstance(instance *remote.RunningInstance) (*remote.SSHClientManager, error) {
	keyFile, err := remote.GetSSHKeyFileForInstance(instance)
	if err != nil {
		return nil, fmt.Errorf("failed to get SSH key: %w", err)
	}

	manager := remote.NewSSHClientManager()
	if err := manager.ConnectToInstance(instance.IP, keyFile); err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	return manager, nil
}

// executeRemoteCommand runs a command on a remote instance using SSH
func executeRemoteCommand(instance *remote.RunningInstance, command string) (string, error) {
	manager, err := getSSHManagerForInstance(instance)
	if err != nil {
		return "", err
	}
	defer manager.CloseAllConnections()

	return manager.ExecuteCommand(instance.IP, command)
}

func runProxyStart(cmd *cobra.Command, args []string) {
	// Load config
	cfg := proxy.DefaultProxyConfig()

	configFile, _ := cmd.Flags().GetString("config")
	if configFile != "" {
		data, err := os.ReadFile(configFile)
		if err != nil {
			log.Fatalf("Failed to read config file: %v", err)
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			log.Fatalf("Failed to parse config file: %v", err)
		}
		fmt.Printf("Loaded config from %s\n", configFile)
	}

	// Override with flags
	if cmd.Flags().Changed("listen-port") {
		cfg.ListenPort, _ = cmd.Flags().GetInt("listen-port")
	}
	if cmd.Flags().Changed("request-timeout") {
		cfg.RequestTimeout, _ = cmd.Flags().GetDuration("request-timeout")
	}
	if cmd.Flags().Changed("queue-enabled") {
		cfg.QueueEnabled, _ = cmd.Flags().GetBool("queue-enabled")
	}
	if cmd.Flags().Changed("queue-timeout") {
		cfg.QueueTimeout, _ = cmd.Flags().GetDuration("queue-timeout")
	}
	if cmd.Flags().Changed("max-queue") {
		cfg.MaxQueueSize, _ = cmd.Flags().GetInt("max-queue")
	}
	if cmd.Flags().Changed("ms-per-token") {
		cfg.MsPerToken, _ = cmd.Flags().GetFloat64("ms-per-token")
	}
	if cmd.Flags().Changed("running-cost-factor") {
		cfg.RunningCostFactor, _ = cmd.Flags().GetFloat64("running-cost-factor")
	}
	if cmd.Flags().Changed("metrics-interval") {
		cfg.MetricsPollInterval, _ = cmd.Flags().GetDuration("metrics-interval")
	}
	if cmd.Flags().Changed("latency-interval") {
		cfg.LatencyProbeInterval, _ = cmd.Flags().GetDuration("latency-interval")
	}
	if cmd.Flags().Changed("metrics-endpoint") {
		cfg.MetricsEndpoint, _ = cmd.Flags().GetString("metrics-endpoint")
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

	// Create proxy
	p := proxy.NewHttpProxy(cfg)

	// Add servers
	servers, _ := cmd.Flags().GetStringSlice("servers")
	sglangPort, _ := cmd.Flags().GetInt("sglang-port")
	autoDiscover, _ := cmd.Flags().GetBool("auto-discover")

	// Auto-discover from Lambda cloud
	if autoDiscover {
		apiToken := remote.GetAPIToken()
		if apiToken == "" {
			log.Fatal("LAMBDA_API_KEY not set. Required for --auto-discover.")
		}
		httpClient := remote.NewHTTPClient()
		instances, err := remote.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			log.Fatalf("Failed to list instances: %v", err)
		}

		fmt.Printf("Auto-discovered %d instances from Lambda cloud\n", len(instances))
		for _, inst := range instances {
			p.AddServer(inst.ID, inst.IP, sglangPort)
		}
	}

	// Add explicit servers
	for _, server := range servers {
		parts := strings.Split(server, ":")
		serverIP := parts[0]
		serverPort := sglangPort
		if len(parts) > 1 {
			if port, err := strconv.Atoi(parts[1]); err == nil {
				serverPort = port
			}
		}
		serverID := fmt.Sprintf("%s:%d", serverIP, serverPort)
		p.AddServer(serverID, serverIP, serverPort)
	}

	// Setup signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println("\nShutting down proxy...")
		p.Stop()
		os.Exit(0)
	}()

	// Print startup info
	fmt.Printf("\nStarting centralized proxy:\n")
	fmt.Printf("  Listen port:          %d\n", cfg.ListenPort)
	fmt.Printf("  SGLang port:          %d\n", sglangPort)
	fmt.Printf("  Queue enabled:        %v\n", cfg.QueueEnabled)
	fmt.Printf("  Max queue:            %d\n", cfg.MaxQueueSize)
	fmt.Printf("  ms_per_token:         %.4f\n", cfg.MsPerToken)
	fmt.Printf("  running_cost_factor:  %.2f\n", cfg.RunningCostFactor)
	fmt.Printf("  Metrics interval:     %v\n", cfg.MetricsPollInterval)
	fmt.Printf("  Latency interval:     %v\n", cfg.LatencyProbeInterval)
	fmt.Printf("  PID file:             %s\n", pidFile)
	fmt.Println()

	// Start background tasks
	if err := p.Start(); err != nil {
		log.Fatalf("Failed to start proxy: %v", err)
	}

	// Start HTTP server
	addr := fmt.Sprintf(":%d", cfg.ListenPort)
	fmt.Printf("Proxy listening on %s\n", addr)

	server := &http.Server{
		Addr:    addr,
		Handler: p.Handler(),
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Proxy error: %v", err)
	}
}

func runProxyStop(cmd *cobra.Command, args []string) {
	pidFile, _ := cmd.Flags().GetString("pid-file")

	data, err := os.ReadFile(pidFile)
	if err != nil {
		log.Fatalf("Failed to read PID file %s: %v", pidFile, err)
	}

	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		log.Fatalf("Failed to parse PID: %v", err)
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		log.Fatalf("Failed to find process %d: %v", pid, err)
	}

	if err := process.Signal(syscall.SIGTERM); err != nil {
		log.Fatalf("Failed to send SIGTERM: %v", err)
	}

	fmt.Printf("Sent SIGTERM to proxy (PID: %d)\n", pid)
}

func runProxyStatus(cmd *cobra.Command, args []string) {
	host, port := resolveProxyHost(cmd)

	client := &http.Client{Timeout: 5 * time.Second}
	statusURL := fmt.Sprintf("http://%s:%d/proxy/status", host, port)

	resp, err := client.Get(statusURL)
	if err != nil {
		fmt.Printf("Proxy not responding: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		fmt.Printf("Failed to parse response: %v\n", err)
		os.Exit(1)
	}

	// Pretty print status
	fmt.Printf("Proxy Status:\n")
	fmt.Printf("  Running:              %v\n", status["running"])
	fmt.Printf("  Uptime:               %.0f seconds\n", status["uptime_seconds"])
	fmt.Printf("  Queue size:           %v\n", status["queue_size"])
	fmt.Printf("  ms_per_token:         %v\n", status["ms_per_token"])
	fmt.Printf("  running_cost_factor:  %v\n", status["running_cost_factor"])
	fmt.Printf("\nRequest Stats:\n")
	fmt.Printf("  Total handled:        %v\n", status["total_handled"])
	fmt.Printf("  Total forwarded:      %v\n", status["total_forwarded"])
	fmt.Printf("  Total queued:         %v\n", status["total_queued"])
	fmt.Printf("  Total rejected:       %v\n", status["total_rejected"])

	// Print servers
	if servers, ok := status["servers"].([]interface{}); ok {
		fmt.Printf("\nServers (%d):\n", len(servers))
		for _, s := range servers {
			server := s.(map[string]interface{})
			healthIcon := "  "
			if server["healthy"] == true {
				healthIcon = ""
			}
			latency := server["latency"].(map[string]interface{})
			fmt.Printf("  %s %s:%v - running=%v, waiting=%v, latency=%vms (avg=%vms)\n",
				healthIcon,
				server["ip"], server["port"],
				server["running_reqs"], server["waiting_reqs"],
				latency["current_ms"], latency["avg_ms"])
		}
	}
}

func runProxyServers(cmd *cobra.Command, args []string) {
	host, port := resolveProxyHost(cmd)

	client := &http.Client{Timeout: 5 * time.Second}
	serversURL := fmt.Sprintf("http://%s:%d/proxy/servers", host, port)

	// Determine action
	action := "list"
	var serverAddrs []string
	if len(args) > 0 {
		if args[0] == "add" || args[0] == "remove" {
			action = args[0]
			serverAddrs = args[1:]
		} else {
			action = "add"
			serverAddrs = args
		}
	}

	switch action {
	case "list":
		resp, err := client.Get(serversURL)
		if err != nil {
			fmt.Printf("Failed to connect: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		var servers []interface{}
		if err := json.NewDecoder(resp.Body).Decode(&servers); err != nil {
			fmt.Printf("Failed to parse response: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Servers in pool (%d):\n", len(servers))
		for _, s := range servers {
			server := s.(map[string]interface{})
			healthIcon := ""
			if server["Metrics"].(map[string]interface{})["healthy"] != true {
				healthIcon = ""
			}
			fmt.Printf("  %s %s (%s:%v)\n",
				healthIcon, server["id"], server["ip"], server["port"])
		}

	case "add":
		for _, addr := range serverAddrs {
			parts := strings.Split(addr, ":")
			ip := parts[0]
			serverPort := 8080
			if len(parts) > 1 {
				if p, err := strconv.Atoi(parts[1]); err == nil {
					serverPort = p
				}
			}

			body := fmt.Sprintf(`{"id": "%s:%d", "ip": "%s", "port": %d}`, ip, serverPort, ip, serverPort)
			resp, err := client.Post(serversURL, "application/json", strings.NewReader(body))
			if err != nil {
				fmt.Printf("Failed to add %s: %v\n", addr, err)
				continue
			}
			resp.Body.Close()
			fmt.Printf("Added server %s:%d\n", ip, serverPort)
		}

	case "remove":
		for _, addr := range serverAddrs {
			parts := strings.Split(addr, ":")
			id := addr
			if len(parts) == 1 {
				id = fmt.Sprintf("%s:8080", parts[0])
			}

			req, _ := http.NewRequest(http.MethodDelete, serversURL+"?id="+id, nil)
			resp, err := client.Do(req)
			if err != nil {
				fmt.Printf("Failed to remove %s: %v\n", addr, err)
				continue
			}
			resp.Body.Close()
			fmt.Printf("Removed server %s\n", id)
		}
	}
}

func runProxyTune(cmd *cobra.Command, args []string) {
	host, port := resolveProxyHost(cmd)
	msPerToken, _ := cmd.Flags().GetFloat64("ms-per-token")
	runningCostFactor, _ := cmd.Flags().GetFloat64("running-cost-factor")

	client := &http.Client{Timeout: 5 * time.Second}
	tuneURL := fmt.Sprintf("http://%s:%d/proxy/tune", host, port)

	if msPerToken == 0 && runningCostFactor == 0 {
		// GET current values
		statusURL := fmt.Sprintf("http://%s:%d/proxy/status", host, port)
		resp, err := client.Get(statusURL)
		if err != nil {
			fmt.Printf("Failed to connect: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		var status map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&status)
		fmt.Printf("Current tuning parameters:\n")
		fmt.Printf("  ms_per_token:         %v\n", status["ms_per_token"])
		fmt.Printf("  running_cost_factor:  %v\n", status["running_cost_factor"])
		return
	}

	// POST to update
	reqBody := make(map[string]interface{})
	if msPerToken > 0 {
		reqBody["ms_per_token"] = msPerToken
	}
	if runningCostFactor > 0 {
		reqBody["running_cost_factor"] = runningCostFactor
	}

	bodyBytes, _ := json.Marshal(reqBody)
	resp, err := client.Post(tuneURL, "application/json", strings.NewReader(string(bodyBytes)))
	if err != nil {
		fmt.Printf("Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var result map[string]float64
	json.NewDecoder(resp.Body).Decode(&result)
	fmt.Printf("Updated tuning parameters:\n")
	fmt.Printf("  ms_per_token:         %.4f\n", result["ms_per_token"])
	fmt.Printf("  running_cost_factor:  %.2f\n", result["running_cost_factor"])
}

func runProxyClearCache(cmd *cobra.Command, args []string) {
	host, port := resolveProxyHost(cmd)

	client := &http.Client{Timeout: 5 * time.Second}
	clearURL := fmt.Sprintf("http://%s:%d/proxy/cache/clear", host, port)

	resp, err := client.Post(clearURL, "application/json", nil)
	if err != nil {
		fmt.Printf("Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body := make([]byte, 1024)
	n, _ := resp.Body.Read(body)
	fmt.Println(string(body[:n]))
}

func runProxyLatency(cmd *cobra.Command, args []string) {
	host, port := resolveProxyHost(cmd)

	client := &http.Client{Timeout: 5 * time.Second}
	latencyURL := fmt.Sprintf("http://%s:%d/proxy/latency", host, port)

	resp, err := client.Get(latencyURL)
	if err != nil {
		fmt.Printf("Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var latencies map[string]map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&latencies); err != nil {
		fmt.Printf("Failed to parse response: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Latency to servers:\n")
	for id, data := range latencies {
		healthIcon := ""
		if data["healthy"] != true {
			healthIcon = ""
		}
		fmt.Printf("  %s %s (%s:%v)\n", healthIcon, id, data["ip"], data["port"])
		fmt.Printf("      Current: %v ms\n", data["current_ms"])
		fmt.Printf("      Average: %v ms (EWMA)\n", data["avg_ms"])
		fmt.Printf("      Min:     %v ms\n", data["min_ms"])
		fmt.Printf("      Max:     %v ms\n", data["max_ms"])
	}
}

func runProxyDeploy(cmd *cobra.Command, args []string) {
	instanceName := args[0]
	listenPort, _ := cmd.Flags().GetInt("listen-port")
	sglangPort, _ := cmd.Flags().GetInt("sglang-port")
	msPerToken, _ := cmd.Flags().GetFloat64("ms-per-token")
	runningCostFactor, _ := cmd.Flags().GetFloat64("running-cost-factor")
	skipBuild, _ := cmd.Flags().GetBool("skip-build")
	autoAddServers, _ := cmd.Flags().GetBool("auto-add-servers")

	apiToken := remote.GetAPIToken()
	if apiToken == "" {
		log.Fatal("LAMBDA_API_KEY not set")
	}

	httpClient := remote.NewHTTPClient()

	// Step 1: Resolve the proxy instance
	fmt.Printf("Resolving instance '%s'...\n", instanceName)
	proxyInstance, err := remote.ResolveInstance(httpClient, apiToken, instanceName)
	if err != nil {
		log.Fatalf("Failed to resolve instance: %v", err)
	}
	fmt.Printf("Found instance: %s (%s)\n", proxyInstance.Name, proxyInstance.IP)

	// Step 2: Build Linux binary (unless skipped)
	binaryPath := "/tmp/gotoni-linux"
	if !skipBuild {
		fmt.Println("\nBuilding gotoni for Linux...")
		buildCmd := exec.Command("go", "build", "-o", binaryPath, ".")
		buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
		buildCmd.Stdout = os.Stdout
		buildCmd.Stderr = os.Stderr
		if err := buildCmd.Run(); err != nil {
			log.Fatalf("Failed to build: %v", err)
		}
		fmt.Printf("Built %s\n", binaryPath)
	}

	// Step 3: Get SSH key and connect
	keyFile, err := remote.GetSSHKeyFileForInstance(proxyInstance)
	if err != nil {
		log.Fatalf("Failed to get SSH key: %v", err)
	}

	// Step 4: Upload binary using SCP
	fmt.Printf("\nUploading binary to %s...\n", proxyInstance.Name)
	scpCmd := exec.Command("scp", "-i", keyFile, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
		binaryPath, fmt.Sprintf("ubuntu@%s:/home/ubuntu/gotoni", proxyInstance.IP))
	scpCmd.Stdout = os.Stdout
	scpCmd.Stderr = os.Stderr
	if err := scpCmd.Run(); err != nil {
		log.Fatalf("Failed to upload binary: %v", err)
	}
	fmt.Println("Upload complete")

	// Step 5: Connect via SSH and start proxy
	fmt.Printf("\nConnecting to %s...\n", proxyInstance.Name)
	manager := remote.NewSSHClientManager()
	if err := manager.ConnectToInstance(proxyInstance.IP, keyFile); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer manager.CloseAllConnections()

	// Kill any existing gotoni process
	// Use base64 encoding to avoid self-matching
	fmt.Println("Stopping any existing gotoni process...")
	killScript := `#!/bin/bash
for pid in $(pgrep -f "gotoni" 2>/dev/null | head -10); do kill -9 $pid 2>/dev/null || true; done
exit 0
`
	encodedKill := base64.StdEncoding.EncodeToString([]byte(killScript))
	manager.ExecuteCommand(proxyInstance.IP, fmt.Sprintf("echo %s | base64 -d | bash", encodedKill))
	time.Sleep(1 * time.Second)

	// Make binary executable and start proxy
	fmt.Println("Starting proxy...")
	startCmd := fmt.Sprintf("chmod +x ~/gotoni && nohup ~/gotoni proxy start --listen-port %d --ms-per-token %.4f --running-cost-factor %.2f > /tmp/proxy.log 2>&1 &",
		listenPort, msPerToken, runningCostFactor)
	if _, err := manager.ExecuteCommand(proxyInstance.IP, startCmd); err != nil {
		log.Fatalf("Failed to start proxy: %v", err)
	}

	// Wait for proxy to start
	time.Sleep(2 * time.Second)

	// Step 6: Auto-add SGLang servers from other instances
	if autoAddServers {
		fmt.Println("\nAuto-discovering SGLang servers...")
		instances, err := remote.ListRunningInstances(httpClient, apiToken)
		if err != nil {
			log.Printf("Warning: Failed to list instances: %v", err)
		} else {
			proxyURL := fmt.Sprintf("http://%s:%d", proxyInstance.IP, listenPort)
			client := &http.Client{Timeout: 5 * time.Second}

			for _, inst := range instances {
				// Skip the proxy instance itself
				if inst.ID == proxyInstance.ID || inst.IP == proxyInstance.IP {
					continue
				}

				// Add server to proxy pool
				serverAddr := fmt.Sprintf("%s:%d", inst.IP, sglangPort)
				body := fmt.Sprintf(`{"id": "%s", "ip": "%s", "port": %d}`, serverAddr, inst.IP, sglangPort)
				resp, err := client.Post(proxyURL+"/proxy/servers", "application/json", strings.NewReader(body))
				if err != nil {
					fmt.Printf("  Failed to add %s (%s): %v\n", inst.Name, serverAddr, err)
					continue
				}
				resp.Body.Close()
				fmt.Printf("  Added %s (%s)\n", inst.Name, serverAddr)
			}
		}
	}

	// Step 7: Show final status
	fmt.Println("\nProxy deployment complete!")
	fmt.Printf("\nProxy Status:\n")

	client := &http.Client{Timeout: 5 * time.Second}
	statusURL := fmt.Sprintf("http://%s:%d/proxy/status", proxyInstance.IP, listenPort)
	resp, err := client.Get(statusURL)
	if err != nil {
		fmt.Printf("  Warning: Could not get status: %v\n", err)
		return
	}
	defer resp.Body.Close()

	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		fmt.Printf("  Warning: Could not parse status: %v\n", err)
		return
	}

	fmt.Printf("  Running:              %v\n", status["running"])
	fmt.Printf("  Listen port:          %d\n", listenPort)
	fmt.Printf("  ms_per_token:         %v\n", status["ms_per_token"])
	fmt.Printf("  running_cost_factor:  %v\n", status["running_cost_factor"])

	if servers, ok := status["servers"].([]interface{}); ok {
		fmt.Printf("  Servers in pool:      %d\n", len(servers))
		for _, s := range servers {
			server := s.(map[string]interface{})
			fmt.Printf("    - %s:%v\n", server["ip"], server["port"])
		}
	}

	fmt.Printf("\nProxy endpoint: http://%s:%d\n", proxyInstance.IP, listenPort)
	fmt.Printf("Use: gotoni proxy status --remote %s\n", instanceName)
}
