/*
Copyright © 2025 ALESSIO TONIOLO

lb.go implements local load balancer commands.
These commands are designed to run on the instance itself (after gotoni binary is deployed).
*/
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	serve "github.com/atoniolo76/gotoni/pkg/cluster"
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

  # Check load balancer status
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

// lbStatusCmd checks load balancer status
var lbStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check load balancer status",
	Long:  `Query the load balancer's /lb/status endpoint to check if it's running and healthy.`,
	Run:   runLBStatus,
}

func init() {
	rootCmd.AddCommand(lbCmd)
	lbCmd.AddCommand(lbStartCmd)
	lbCmd.AddCommand(lbStopCmd)
	lbCmd.AddCommand(lbStatusCmd)

	// Flags for lb start
	lbStartCmd.Flags().String("config", "", "Path to JSON config file")
	lbStartCmd.Flags().String("node-id", "", "Unique node identifier (defaults to hostname)")
	lbStartCmd.Flags().Int("local-port", 8080, "Port of the local backend service")
	lbStartCmd.Flags().Int("listen-port", 8000, "Port for the load balancer to listen on")
	lbStartCmd.Flags().Int("peer-port", 8000, "Port that peer load balancers listen on")
	lbStartCmd.Flags().Int("max-concurrent", 10, "Max concurrent requests before forwarding to peers")
	lbStartCmd.Flags().String("strategy", "least-loaded", "Load balancing strategy: least-loaded, round-robin, random")
	lbStartCmd.Flags().Bool("queue-enabled", true, "Enable request queuing when all nodes at capacity")
	lbStartCmd.Flags().Int("max-queue-size", 100, "Maximum queue size")
	lbStartCmd.Flags().Duration("queue-timeout", 30*time.Second, "Queue timeout")
	lbStartCmd.Flags().Duration("health-check-interval", 5*time.Second, "Health check interval")
	lbStartCmd.Flags().Duration("request-timeout", 30*time.Second, "Request timeout for forwarded requests")
	lbStartCmd.Flags().StringSlice("peers", []string{}, "Peer addresses in format ip:port (can specify multiple)")
	lbStartCmd.Flags().String("pid-file", "/tmp/gotoni-lb.pid", "Path to PID file")

	// Flags for lb status
	lbStatusCmd.Flags().Int("port", 8000, "Load balancer port to check")
	lbStatusCmd.Flags().String("host", "localhost", "Load balancer host to check")

	// Flags for lb stop
	lbStopCmd.Flags().String("pid-file", "/tmp/gotoni-lb.pid", "Path to PID file")
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
	if cmd.Flags().Changed("node-id") {
		config.NodeID, _ = cmd.Flags().GetString("node-id")
	}
	if cmd.Flags().Changed("local-port") {
		config.LocalPort, _ = cmd.Flags().GetInt("local-port")
	}
	if cmd.Flags().Changed("listen-port") {
		config.ListenPort, _ = cmd.Flags().GetInt("listen-port")
	}
	if cmd.Flags().Changed("peer-port") {
		config.PeerPort, _ = cmd.Flags().GetInt("peer-port")
	}
	if cmd.Flags().Changed("max-concurrent") {
		config.MaxConcurrentRequests, _ = cmd.Flags().GetInt("max-concurrent")
	}
	if cmd.Flags().Changed("strategy") {
		config.Strategy, _ = cmd.Flags().GetString("strategy")
	}
	if cmd.Flags().Changed("queue-enabled") {
		config.QueueEnabled, _ = cmd.Flags().GetBool("queue-enabled")
	}
	if cmd.Flags().Changed("max-queue-size") {
		config.MaxQueueSize, _ = cmd.Flags().GetInt("max-queue-size")
	}
	if cmd.Flags().Changed("queue-timeout") {
		config.QueueTimeout, _ = cmd.Flags().GetDuration("queue-timeout")
	}
	if cmd.Flags().Changed("health-check-interval") {
		config.HealthCheckInterval, _ = cmd.Flags().GetDuration("health-check-interval")
	}
	if cmd.Flags().Changed("request-timeout") {
		config.RequestTimeout, _ = cmd.Flags().GetDuration("request-timeout")
	}

	// Default node ID to hostname if not set
	if config.NodeID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			config.NodeID = fmt.Sprintf("node-%d", os.Getpid())
		} else {
			config.NodeID = hostname
		}
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

	// Create load balancer
	lb := serve.NewNodeLoadBalancer(config)

	// Add peers if specified
	peers, _ := cmd.Flags().GetStringSlice("peers")
	for _, peer := range peers {
		// Parse peer address (format: ip:port or just ip)
		// For now, we'd need instance info - skip for basic implementation
		fmt.Printf("Peer specified: %s (manual peer addition not yet implemented)\n", peer)
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
	fmt.Printf("  Node ID:          %s\n", config.NodeID)
	fmt.Printf("  Listen port:      %d\n", config.ListenPort)
	fmt.Printf("  Local backend:    localhost:%d\n", config.LocalPort)
	fmt.Printf("  Max concurrent:   %d\n", config.MaxConcurrentRequests)
	fmt.Printf("  Strategy:         %s\n", config.Strategy)
	fmt.Printf("  Queue enabled:    %v\n", config.QueueEnabled)
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
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")

	statusURL := fmt.Sprintf("http://%s:%d/lb/status", host, port)
	metricsURL := fmt.Sprintf("http://%s:%d/lb/metrics", host, port)
	peersURL := fmt.Sprintf("http://%s:%d/lb/peers", host, port)

	fmt.Printf("Checking load balancer at %s:%d...\n\n", host, port)

	client := &http.Client{Timeout: 5 * time.Second}

	// Check status
	fmt.Println("=== Status ===")
	resp, err := client.Get(statusURL)
	if err != nil {
		fmt.Printf("❌ Load balancer is NOT running or unreachable: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var status serve.PeerStatus
	if err := json.Unmarshal(body, &status); err != nil {
		fmt.Printf("Raw response: %s\n", string(body))
	} else {
		fmt.Printf("✅ Load balancer is running\n")
		fmt.Printf("  Node ID:       %s\n", status.NodeID)
		fmt.Printf("  Current Load:  %d / %d\n", status.CurrentLoad, status.MaxLoad)
		fmt.Printf("  Queue Size:    %d\n", status.QueueSize)
		fmt.Printf("  Healthy:       %v\n", status.Healthy)
	}

	// Check metrics
	fmt.Println("\n=== Metrics ===")
	resp, err = client.Get(metricsURL)
	if err != nil {
		fmt.Printf("Failed to get metrics: %v\n", err)
	} else {
		defer resp.Body.Close()
		body, _ = io.ReadAll(resp.Body)
		var metrics serve.LoadBalancerMetrics
		if err := json.Unmarshal(body, &metrics); err != nil {
			fmt.Printf("Raw response: %s\n", string(body))
		} else {
			fmt.Printf("  Total Requests:     %d\n", metrics.TotalRequests)
			fmt.Printf("  Local Requests:     %d\n", metrics.LocalRequests)
			fmt.Printf("  Forwarded Requests: %d\n", metrics.ForwardedRequests)
			fmt.Printf("  Queued Requests:    %d\n", metrics.QueuedRequests)
			fmt.Printf("  Dropped Requests:   %d\n", metrics.DroppedRequests)
			fmt.Printf("  Failed Forwards:    %d\n", metrics.FailedForwards)
			fmt.Printf("  Avg Response Time:  %.2f ms\n", metrics.AvgResponseTimeMs)
			fmt.Printf("  Peak Concurrent:    %d\n", metrics.PeakConcurrent)
		}
	}

	// Check peers
	fmt.Println("\n=== Peers ===")
	resp, err = client.Get(peersURL)
	if err != nil {
		fmt.Printf("Failed to get peers: %v\n", err)
	} else {
		defer resp.Body.Close()
		body, _ = io.ReadAll(resp.Body)
		var peers []*serve.PeerNode
		if err := json.Unmarshal(body, &peers); err != nil {
			fmt.Printf("Raw response: %s\n", string(body))
		} else if len(peers) == 0 {
			fmt.Printf("  No peers configured\n")
		} else {
			for _, peer := range peers {
				healthStatus := "❌"
				if peer.Healthy {
					healthStatus = "✅"
				}
				fmt.Printf("  %s %s (%s:%d) - Load: %d/%d, Response: %dms\n",
					healthStatus,
					peer.Instance.ID[:16],
					peer.Instance.IP,
					peer.Port,
					peer.CurrentLoad,
					peer.MaxLoad,
					peer.ResponseTimeMs,
				)
			}
		}
	}
}
