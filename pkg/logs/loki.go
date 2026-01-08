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
	return &LokiService{
		config: LokiConfig{
			HTTPPort:      "3100",
			GRPCPort:      "9096",
			DataDir:       "/tmp/loki-data",
			ConfigFile:    "/tmp/loki-config.yaml",
			DockerImage:   "grafana/loki:latest",
			ContainerName: "gotoni-loki",
		},
	}
}

// Start starts the local Loki instance in a Docker container
func (l *LokiService) Start(ctx context.Context) error {
	log.Println("Starting local Loki service...")

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

	log.Printf("Loki service started with container ID: %s", l.containerID)

	// Wait for Loki to be ready
	return l.waitForReady(ctx)
}

// Stop stops the Loki service
func (l *LokiService) Stop() error {
	log.Println("Stopping Loki service...")
	return l.stopContainer()
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

// Private methods

func (l *LokiService) createLokiConfig() error {
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
  chunk_idle_period: 1h
  max_chunk_age: 1h
  chunk_target_size: 1048576
  chunk_retain_period: 30s
  max_transfer_retries: 0

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
    active_index_directory: /tmp/loki/boltdb-shipper-active
    cache_location: /tmp/loki/boltdb-shipper-cache
    cache_ttl: 24h
    shared_store: filesystem
  filesystem:
    directory: /tmp/loki/chunks

chunk_store_config:
  max_look_back_period: 0s

table_manager:
  retention_deletes_enabled: false
  retention_period: 0s

limits_config:
  reject_old_samples: true
  reject_old_samples_max_age: 168h

querier:
  max_concurrent: 10
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
	url := fmt.Sprintf("http://localhost:%s/ready", l.config.HTTPPort)

	for i := 0; i < 30; i++ { // Wait up to 30 seconds
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			resp, err := client.Get(url)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					log.Println("Loki service is ready")
					return nil
				}
			}
			time.Sleep(1 * time.Second)
		}
	}

	return fmt.Errorf("Loki service failed to become ready within timeout")
}
