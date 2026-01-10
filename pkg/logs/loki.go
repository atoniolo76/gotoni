/*
Copyright © 2025 ALESSIO TONIOLO

loki.go contains local Loki service management for log aggregation
*/
package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// LokiConfig holds configuration for the local Loki instance
type LokiConfig struct {
	HTTPPort      string
	GRPCPort      string
	DataDir       string
	ConfigFile    string
	DockerImage   string
	ContainerName string
}

// LokiService manages a local Loki instance
type LokiService struct {
	config      LokiConfig
	containerID string
	running     bool
	mockServer  *MockLokiServer // For testing/development when Docker Loki fails
}

// LokiQueryResponse represents a Loki query response
type LokiQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][]interface{}   `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// NewLokiService creates a new Loki service instance
func NewLokiService() *LokiService {
	return NewLokiServiceWithPort("3100")
}

// NewLokiServiceWithPort creates a new Loki service instance with custom port
func NewLokiServiceWithPort(port string) *LokiService {
	return &LokiService{
		config: LokiConfig{
			HTTPPort:      port,
			GRPCPort:      "9096",
			DataDir:       "/tmp/loki-data-" + port,
			ConfigFile:    "/tmp/loki-config-" + port + ".yaml",
			DockerImage:   "grafana/loki:3.0.0", // Try a different version
			ContainerName: "gotoni-loki-" + port,
		},
	}
}

// Start starts the local Loki instance (Docker) or falls back to mock server
func (l *LokiService) Start(ctx context.Context) error {
	log.Println("Starting local Loki service...")

	// For now, use mock server directly for testing
	// TODO: Fix Docker Loki startup issues
	log.Println("Using mock Loki server for testing...")
	return l.startMockLoki(ctx)

	// Try Docker Loki first (commented out until Docker issues are resolved)
	/*
		if err := l.startDockerLoki(ctx); err != nil {
			log.Printf("Docker Loki failed: %v", err)
			log.Println("Falling back to mock Loki server for testing...")

			// Fall back to mock server
			return l.startMockLoki(ctx)
		}

		return nil
	*/
}

// startDockerLoki attempts to start Loki in Docker
func (l *LokiService) startDockerLoki(ctx context.Context) error {
	// Ensure data directory exists
	if err := os.MkdirAll(l.config.DataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Create Loki configuration
	if err := l.createLokiConfig(); err != nil {
		return fmt.Errorf("failed to create Loki config: %w", err)
	}

	// Check if container already exists and remove it
	l.stopContainer()

	// Start Loki container
	cmd := exec.CommandContext(ctx, "docker", "run", "-d",
		"--name", l.config.ContainerName,
		"-p", fmt.Sprintf("%s:3100", l.config.HTTPPort),
		"-p", fmt.Sprintf("%s:9096", l.config.GRPCPort),
		"-v", fmt.Sprintf("%s:/tmp/loki", l.config.DataDir),
		"-v", fmt.Sprintf("%s:/etc/loki/local-config.yaml", l.config.ConfigFile),
		l.config.DockerImage,
		"-config.file=/etc/loki/local-config.yaml",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start Loki container: %w\nOutput: %s", err, string(output))
	}

	l.containerID = strings.TrimSpace(string(output))
	l.running = true

	containerID := l.containerID
	if len(containerID) > 16 {
		containerID = containerID[:16] + "..."
	}
	log.Printf("Docker Loki service started with container ID: %s", containerID)

	// Give container a moment to start
	time.Sleep(2 * time.Second)

	// Check container status
	checkCmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("id=%s", l.containerID), "--format", "{{.Status}}")
	if statusOutput, err := checkCmd.Output(); err == nil {
		log.Printf("Container status: %s", strings.TrimSpace(string(statusOutput)))
	}

	// Wait for Loki to be ready
	return l.waitForReady(ctx)
}

// startMockLoki starts a simple mock Loki server for testing
func (l *LokiService) startMockLoki(ctx context.Context) error {
	mock := &MockLokiServer{
		logs: make([]LogEntry, 0),
		port: l.config.HTTPPort,
	}

	// Create HTTP server
	mux := http.NewServeMux()

	// Loki push API endpoint
	mux.HandleFunc("/loki/api/v1/push", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusBadRequest)
			return
		}

		// Parse Loki push format (simplified)
		var pushData struct {
			Streams []struct {
				Stream map[string]string `json:"stream"`
				Values [][]interface{}   `json:"values"`
			} `json:"streams"`
		}

		if err := json.Unmarshal(body, &pushData); err != nil {
			log.Printf("Failed to parse push data: %v", err)
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		mock.mutex.Lock()
		for _, stream := range pushData.Streams {
			for _, value := range stream.Values {
				if len(value) >= 2 {
					entry := LogEntry{
						Timestamp: fmt.Sprintf("%v", value[0]),
						Message:   fmt.Sprintf("%v", value[1]),
						Labels:    stream.Stream,
					}
					mock.logs = append(mock.logs, entry)
					log.Printf("Mock Loki received log: %s", entry.Message)
				}
			}
		}
		mock.mutex.Unlock()

		w.WriteHeader(http.StatusNoContent)
	})

	// Loki query API endpoint
	mux.HandleFunc("/loki/api/v1/query_range", func(w http.ResponseWriter, r *http.Request) {
		mock.mutex.RLock()
		defer mock.mutex.RUnlock()

		// Convert to Loki response format
		response := LokiQueryResponse{
			Status: "success",
			Data: struct {
				ResultType string `json:"resultType"`
				Result     []struct {
					Stream map[string]string `json:"stream"`
					Values [][]interface{}   `json:"values"`
				} `json:"result"`
			}{
				ResultType: "streams",
				Result: make([]struct {
					Stream map[string]string `json:"stream"`
					Values [][]interface{}   `json:"values"`
				}, 0),
			},
		}

		// Group logs by stream (labels)
		streamMap := make(map[string][]LogEntry)
		for _, entry := range mock.logs {
			// Create a key from the labels
			key := fmt.Sprintf("%v", entry.Labels)
			streamMap[key] = append(streamMap[key], entry)
		}

		for _, entries := range streamMap {
			if len(entries) > 0 {
				stream := struct {
					Stream map[string]string `json:"stream"`
					Values [][]interface{}   `json:"values"`
				}{
					Stream: entries[0].Labels,
					Values: make([][]interface{}, 0),
				}

				for _, entry := range entries {
					stream.Values = append(stream.Values, []interface{}{entry.Timestamp, entry.Message})
				}

				response.Data.Result = append(response.Data.Result, stream)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	})

	// Health endpoint
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
	})

	server := &http.Server{
		Addr:    ":" + l.config.HTTPPort,
		Handler: mux,
	}

	mock.server = server
	l.mockServer = mock
	l.running = true

	// Start server in background
	go func() {
		log.Printf("Starting mock Loki server on port %s", l.config.HTTPPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Mock Loki server error: %v", err)
		}
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	log.Printf("Mock Loki service started on http://localhost:%s", l.config.HTTPPort)
	return nil
}

// Stop stops the Loki service (both Docker and mock)
func (l *LokiService) Stop() error {
	log.Println("Stopping Loki service...")

	// Stop Docker container if it exists
	if l.containerID != "" {
		l.stopContainer()
	}

	// Stop mock server if it exists
	if l.mockServer != nil && l.mockServer.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := l.mockServer.server.Shutdown(ctx); err != nil {
			log.Printf("Error shutting down mock server: %v", err)
		}
		l.mockServer = nil
	}

	l.running = false
	return nil
}

// IsRunning checks if the Loki service is running
func (l *LokiService) IsRunning() bool {
	if !l.running {
		return false
	}

	cmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("name=%s", l.config.ContainerName), "--filter", "status=running", "-q")
	output, err := cmd.Output()
	return err == nil && len(strings.TrimSpace(string(output))) > 0
}

// GetConfig returns the Loki configuration
func (l *LokiService) GetConfig() LokiConfig {
	return l.config
}

// QueryLogs queries Loki for logs with the given query
func (l *LokiService) QueryLogs(ctx context.Context, query string, limit int) ([]LogEntry, error) {
	if !l.IsRunning() {
		return nil, fmt.Errorf("Loki service is not running")
	}

	// Build query URL
	queryURL := fmt.Sprintf("http://localhost:%s/loki/api/v1/query_range?query=%s&limit=%d&start=%d&end=%d",
		l.config.HTTPPort,
		query,
		limit,
		time.Now().Add(-1*time.Hour).UnixNano(), // Last hour
		time.Now().UnixNano(),
	)

	// Make HTTP request
	req, err := http.NewRequestWithContext(ctx, "GET", queryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query Loki: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Loki query failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var queryResp LokiQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&queryResp); err != nil {
		return nil, fmt.Errorf("failed to decode Loki response: %w", err)
	}

	// Convert to LogEntry format
	var entries []LogEntry
	for _, result := range queryResp.Data.Result {
		for _, value := range result.Values {
			if len(value) >= 2 {
				timestamp := value[0].(string)
				logLine := value[1].(string)

				entry := LogEntry{
					Timestamp: timestamp,
					Message:   logLine,
					Labels:    result.Stream,
				}
				entries = append(entries, entry)
			}
		}
	}

	return entries, nil
}

// GetInstanceLogs retrieves logs for a specific instance
func (l *LokiService) GetInstanceLogs(ctx context.Context, instanceID string, limit int) ([]LogEntry, error) {
	query := fmt.Sprintf(`{container_name=~".*", instance_id="%s"}`, instanceID)
	return l.QueryLogs(ctx, query, limit)
}

// GetAllLogs retrieves all logs with instance metadata
func (l *LokiService) GetAllLogs(ctx context.Context, limit int) ([]LogEntry, error) {
	// If using mock server, return logs directly
	if l.mockServer != nil {
		l.mockServer.mutex.RLock()
		defer l.mockServer.mutex.RUnlock()

		if len(l.mockServer.logs) == 0 {
			return []LogEntry{}, nil
		}

		// Return last 'limit' entries
		start := len(l.mockServer.logs) - limit
		if start < 0 {
			start = 0
		}

		result := make([]LogEntry, len(l.mockServer.logs[start:]))
		copy(result, l.mockServer.logs[start:])
		return result, nil
	}

	// Use regular Loki query
	query := `{container_name=~".*"}`
	return l.QueryLogs(ctx, query, limit)
}

// DisplayLogs prints logs to stdout with formatting
func (l *LokiService) DisplayLogs(entries []LogEntry) {
	for _, entry := range entries {
		fmt.Printf("[%s] ", entry.Timestamp)

		// Show instance metadata
		if instanceID, ok := entry.Labels["instance_id"]; ok {
			fmt.Printf("Instance: %s ", instanceID)
		}
		if containerName, ok := entry.Labels["container_name"]; ok {
			fmt.Printf("Container: %s ", containerName)
		}
		if host, ok := entry.Labels["host"]; ok {
			fmt.Printf("Host: %s ", host)
		}

		fmt.Printf("| %s\n", entry.Message)
	}
}

// LogEntry represents a single log entry with metadata
type LogEntry struct {
	Timestamp string            `json:"timestamp"`
	Message   string            `json:"message"`
	Labels    map[string]string `json:"labels"`
}

// MockLokiServer provides a simple in-memory Loki-compatible server for testing
type MockLokiServer struct {
	server *http.Server
	logs   []LogEntry
	mutex  sync.RWMutex
	port   string
}

// Private methods

func (l *LokiService) createLokiConfig() error {
	// Simplified Loki configuration for local development
	config := `auth_enabled: false

server:
  http_listen_port: 3100
  grpc_listen_port: 9096

ingester:
  lifecycler:
    address: 127.0.0.1
    ring:
      kvstore:
        store: inmemory
      replication_factor: 1
    final_sleep: 0s
  chunk_idle_period: 5m
  chunk_retain_period: 30s

schema_config:
  configs:
    - from: 2020-10-24
      store: boltdb-shipper
      object_store: filesystem
      schema: v11
      index:
        prefix: index_
        period: 24h

storage_config:
  boltdb_shipper:
    active_index_directory: /tmp/loki/index
    cache_location: /tmp/loki/cache
    cache_ttl: 24h
    shared_store: filesystem
  filesystem:
    directory: /tmp/loki/chunks

limits_config:
  reject_old_samples: true
  reject_old_samples_max_age: 168h

querier:
  max_concurrent: 4
`

	return os.WriteFile(l.config.ConfigFile, []byte(config), 0644)
}

func (l *LokiService) stopContainer() error {
	cmd := exec.Command("docker", "rm", "-f", l.config.ContainerName)
	_ = cmd.Run() // Ignore errors if container doesn't exist
	l.running = false
	return nil
}

func (l *LokiService) waitForReady(ctx context.Context) error {
	client := &http.Client{Timeout: 5 * time.Second}

	// Try multiple endpoints that Loki might use
	endpoints := []string{
		fmt.Sprintf("http://localhost:%s/ready", l.config.HTTPPort),
		fmt.Sprintf("http://localhost:%s/health", l.config.HTTPPort),
		fmt.Sprintf("http://localhost:%s/", l.config.HTTPPort),
	}

	log.Printf("Waiting for Loki to be ready...")

	for i := 0; i < 60; i++ { // Wait up to 60 seconds
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			for _, url := range endpoints {
				resp, err := client.Get(url)
				if err == nil {
					resp.Body.Close()
					log.Printf("Loki endpoint %s responded with HTTP %d (attempt %d/60)", url, resp.StatusCode, i+1)
					if resp.StatusCode == http.StatusOK {
						log.Println("Loki service is ready!")
						return nil
					}
				}
			}
			time.Sleep(1 * time.Second)
		}
	}

	// If we get here, let's check the container logs for debugging
	if l.containerID != "" {
		logCmd := exec.Command("docker", "logs", l.containerID)
		if logsOutput, err := logCmd.Output(); err == nil {
			log.Printf("Loki container logs:\n%s", string(logsOutput))
		}
	}

	return fmt.Errorf("Loki service failed to become ready within 60 seconds")
}
