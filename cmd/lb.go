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
	Long:  `Check if the load balancer is running and responding to requests.`,
	Run:   runLBStatus,
}

func init() {
	rootCmd.AddCommand(lbCmd)
	lbCmd.AddCommand(lbStartCmd)
	lbCmd.AddCommand(lbStopCmd)
	lbCmd.AddCommand(lbStatusCmd)

	// Flags for lb start
	lbStartCmd.Flags().String("config", "", "Path to JSON config file")
	lbStartCmd.Flags().Int("local-port", 8080, "Port of the local SGLang backend service")
	lbStartCmd.Flags().Int("listen-port", 8000, "Port for the load balancer to listen on")
	lbStartCmd.Flags().Int("max-concurrent", 10, "Max concurrent requests before forwarding to peers")
	lbStartCmd.Flags().Bool("queue-enabled", true, "Enable request queuing when all nodes at capacity")
	lbStartCmd.Flags().Duration("queue-timeout", 30*time.Second, "Queue timeout")
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
	if cmd.Flags().Changed("request-timeout") {
		config.RequestTimeout, _ = cmd.Flags().GetDuration("request-timeout")
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
	fmt.Printf("  Listen port:      %d\n", config.LoadBalancerPort)
	fmt.Printf("  Local SGLang:     localhost:%d\n", config.ApplicationPort)
	fmt.Printf("  Max concurrent:   %d\n", config.MaxConcurrentRequests)
	fmt.Printf("  Queue enabled:    %v\n", config.QueueEnabled)
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
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")

	// Try to connect to the load balancer
	testURL := fmt.Sprintf("http://%s:%d/", host, port)
	client := &http.Client{Timeout: 5 * time.Second}

	fmt.Printf("Checking load balancer at %s:%d...\n\n", host, port)

	// Basic connectivity check
	resp, err := client.Get(testURL)
	if err != nil {
		fmt.Printf("❌ Load balancer is NOT running or unreachable: %v\n", err)
		fmt.Printf("   Make sure the load balancer is started with 'gotoni lb start'\n")
		os.Exit(1)
	}
	defer resp.Body.Close()

	fmt.Printf("✅ Load balancer is responding (HTTP %d)\n", resp.StatusCode)

	// Since the new load balancer doesn't have status endpoints,
	// we can only confirm it's responding to requests
	fmt.Printf("\nNote: Detailed status/metrics not available in new SGLang-integrated version\n")
	fmt.Printf("The load balancer is running and can handle requests.\n")
}
