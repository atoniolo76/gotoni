/*
Copyright © 2025 ALESSIO TONIOLO

sglang_metrics.go handles polling and parsing metrics from SGLang servers.
This is used by the load balancer to make routing decisions based on real
server state (running requests, waiting requests, GPU cache usage).
*/
package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// SGLangMetrics represents the server metrics from SGLang's /get_server_info endpoint
type SGLangMetrics struct {
	NumRunningReqs   int     `json:"num_running_reqs"` // Currently in GPU batch
	NumWaitingReqs   int     `json:"num_waiting_reqs"` // Queued waiting for batch slot
	NumTotalReqs     int     `json:"num_total_reqs"`   // Total = running + waiting
	GPUCacheUsage    float64 `json:"gpu_cache_usage"`  // KV cache utilization (0.0-1.0)
	MaxRunningReqs   int     `json:"max_running_reqs"` // Server's configured max
	MaxTotalTokens   int     `json:"max_total_tokens"` // Server's token limit
	LastUpdated      time.Time
	Healthy          bool
	ConsecutiveFails int
	Latency          int64 // point-to-point latency in miliseconds
}

// SGLangServerInfo is the raw response from SGLang's /get_server_info endpoint
// Field names match SGLang's actual JSON response
type SGLangServerInfo struct {
	NumRunningReqs int     `json:"num_running_reqs"`
	NumWaitingReqs int     `json:"num_waiting_reqs"`
	MaxRunningReqs int     `json:"max_running_reqs"`
	MaxTotalTokens int     `json:"max_total_num_tokens"`
	GPUCacheUsage  float64 `json:"token_usage"` // SGLang reports cache usage as token_usage
}

// MetricsConfig holds configuration for metrics polling
type MetricsConfig struct {
	Enabled            bool          `json:"metrics_enabled"`
	PollInterval       time.Duration `json:"metrics_poll_interval"`
	Endpoint           string        `json:"metrics_endpoint"` // e.g., "/get_server_info"
	Timeout            time.Duration `json:"metrics_timeout"`
	UnhealthyThreshold int           `json:"unhealthy_threshold"` // Consecutive failures before marking unhealthy
	GPUCacheThreshold  float64       `json:"gpu_cache_threshold"` // Mark unavailable if cache usage above this
}

// DefaultMetricsConfig returns sensible defaults for metrics polling
func DefaultMetricsConfig() MetricsConfig {
	return MetricsConfig{
		Enabled:            true,
		PollInterval:       1 * time.Second,
		Endpoint:           "/get_server_info",
		Timeout:            500 * time.Millisecond,
		UnhealthyThreshold: 3,
		GPUCacheThreshold:  0.95,
	}
}

// pollMetricsLoop periodically polls SGLang metrics from local backend and all peers
func (lb *LoadBalancer) pollMetricsLoop() {
	defer lb.wg.Done()

	ticker := time.NewTicker(lb.config.MetricsPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-lb.ctx.Done():
			return
		case <-ticker.C:
			// Poll local SGLang first (most important for routing decisions)
			lb.pollLocalMetrics()
			// Then poll all peers concurrently
			lb.pollAllPeers()
		}
	}
}

// pollLocalMetrics fetches metrics from the local SGLang backend
func (lb *LoadBalancer) pollLocalMetrics() {
	ctx, cancel := context.WithTimeout(lb.ctx, lb.config.MetricsTimeout)
	defer cancel()

	metricsURL := fmt.Sprintf("http://127.0.0.1:%d%s", lb.config.ApplicationPort, lb.config.MetricsEndpoint)

	pollStart := time.Now()
	metrics, err := lb.fetchSGLangMetrics(ctx, metricsURL)
	pollDuration := time.Since(pollStart)

	if err != nil {
		lb.markLocalUnhealthy()
		// Record unhealthy event
		if lb.observability != nil {
			lb.observability.RecordEvent(LBEvent{
				Timestamp: time.Now(),
				EventType: EventLocalUnhealthy,
				Metadata: map[string]interface{}{
					"error":       err.Error(),
					"poll_ms":     float64(pollDuration.Microseconds()) / 1000.0,
					"metrics_url": metricsURL,
				},
			})
		}
		return
	}

	// Update local metrics
	lb.localMetricsMu.Lock()
	lb.localMetrics = *metrics
	lb.localMetricsMu.Unlock()

	// Push metrics to observability (for Grafana dashboard)
	if lb.observability != nil {
		lb.observability.RecordMetrics(*metrics, "local", true)
	}
}

// pollAllPeers polls metrics from all peers concurrently
func (lb *LoadBalancer) pollAllPeers() {
	lb.peersMu.RLock()
	peers := make([]*PeerNode, 0, len(lb.peers))
	for peer := range lb.peers {
		peers = append(peers, peer)
	}
	lb.peersMu.RUnlock()

	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Add(1)
		go func(p *PeerNode) {
			defer wg.Done()
			lb.pollPeerMetrics(p)
		}(peer)
	}
	wg.Wait()
}

// pollPeerMetrics fetches metrics from a single SGLang peer
func (lb *LoadBalancer) pollPeerMetrics(peer *PeerNode) {
	ctx, cancel := context.WithTimeout(lb.ctx, lb.config.MetricsTimeout)
	defer cancel()

	metricsURL := fmt.Sprintf("http://%s:%d%s", peer.Instance.IP, peer.Port, lb.config.MetricsEndpoint)

	// timestamp the start of the poll
	start := time.Now()

	metrics, err := lb.fetchSGLangMetrics(ctx, metricsURL)
	if err != nil {
		lb.markPeerUnhealthy(peer)

		// Record peer unhealthy event
		if lb.observability != nil {
			elapsed := time.Since(start)
			lb.observability.RecordEvent(LBEvent{
				Timestamp: time.Now(),
				EventType: EventPeerUnhealthy,
				Metadata: map[string]interface{}{
					"peer_id":     peer.Instance.ID,
					"peer_ip":     peer.Instance.IP,
					"error":       err.Error(),
					"poll_ms":     float64(elapsed.Microseconds()) / 1000.0,
					"metrics_url": metricsURL,
				},
			})
		}
		return
	}

	// timestamp the end of the poll
	end := time.Now()
	elapsed := end.Sub(start)
	log.Printf("[LB] Poll peer metrics for %s took %s", peer.Instance.ID, elapsed)

	// Check if peer was previously unhealthy (recovered)
	wasUnhealthy := !peer.Metrics.Healthy && peer.Metrics.ConsecutiveFails > 0

	// calculate point-to-point latency in miliseconds for policy usage
	metrics.Latency = elapsed.Milliseconds()

	// Update peer metrics and availability
	lb.peersMu.Lock()
	peer.Metrics = *metrics

	// Determine availability based on real metrics
	available := metrics.Healthy &&
		metrics.GPUCacheUsage < lb.config.GPUCacheThreshold &&
		metrics.NumTotalReqs < metrics.MaxRunningReqs

	lb.peers[peer] = AvailabilityStatus{
		Available:       available,
		RunningRequests: metrics.NumRunningReqs,
		WaitingRequests: metrics.NumWaitingReqs,
		TotalRequests:   metrics.NumTotalReqs,
		GPUCacheUsage:   metrics.GPUCacheUsage,
		Healthy:         true,
	}
	lb.peersMu.Unlock()

	// Push metrics to observability (for Grafana dashboard)
	if lb.observability != nil {
		lb.observability.RecordMetrics(*metrics, peer.Instance.ID, false)

		// Record recovery event if peer was previously unhealthy
		if wasUnhealthy {
			lb.observability.RecordEvent(LBEvent{
				Timestamp: time.Now(),
				EventType: EventPeerRecovered,
				Metadata: map[string]interface{}{
					"peer_id":  peer.Instance.ID,
					"peer_ip":  peer.Instance.IP,
					"poll_ms":  float64(elapsed.Microseconds()) / 1000.0,
				},
			})
		}
	}
}

// fetchSGLangMetrics makes an HTTP request to fetch metrics from an SGLang server
func (lb *LoadBalancer) fetchSGLangMetrics(ctx context.Context, url string) (*SGLangMetrics, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := lb.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var serverInfo SGLangServerInfo
	if err := json.NewDecoder(resp.Body).Decode(&serverInfo); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &SGLangMetrics{
		NumRunningReqs:   serverInfo.NumRunningReqs,
		NumWaitingReqs:   serverInfo.NumWaitingReqs,
		NumTotalReqs:     serverInfo.NumRunningReqs + serverInfo.NumWaitingReqs,
		GPUCacheUsage:    serverInfo.GPUCacheUsage,
		MaxRunningReqs:   serverInfo.MaxRunningReqs,
		MaxTotalTokens:   serverInfo.MaxTotalTokens,
		LastUpdated:      time.Now(),
		Healthy:          true,
		ConsecutiveFails: 0,
	}, nil
}

// markLocalUnhealthy marks the local SGLang as unhealthy after failed poll
func (lb *LoadBalancer) markLocalUnhealthy() {
	lb.localMetricsMu.Lock()
	defer lb.localMetricsMu.Unlock()

	lb.localMetrics.ConsecutiveFails++
	if lb.localMetrics.ConsecutiveFails >= lb.config.UnhealthyThreshold {
		if lb.localMetrics.Healthy {
			log.Printf("[LB] Local SGLang marked unhealthy after %d consecutive failures",
				lb.localMetrics.ConsecutiveFails)
		}
		lb.localMetrics.Healthy = false
	}
	lb.localMetrics.LastUpdated = time.Now()
}

// markPeerUnhealthy marks a peer as unhealthy after failed metrics poll
func (lb *LoadBalancer) markPeerUnhealthy(peer *PeerNode) {
	lb.peersMu.Lock()
	defer lb.peersMu.Unlock()

	peer.Metrics.ConsecutiveFails++
	if peer.Metrics.ConsecutiveFails >= lb.config.UnhealthyThreshold {
		if peer.Metrics.Healthy {
			log.Printf("[LB] Peer %s marked unhealthy after %d consecutive failures",
				peer.Instance.ID, peer.Metrics.ConsecutiveFails)
		}
		peer.Metrics.Healthy = false

		lb.peers[peer] = AvailabilityStatus{
			Available:       false,
			RunningRequests: peer.Metrics.NumRunningReqs,
			WaitingRequests: peer.Metrics.NumWaitingReqs,
			TotalRequests:   peer.Metrics.NumTotalReqs,
			GPUCacheUsage:   peer.Metrics.GPUCacheUsage,
			Healthy:         false,
		}
	}
	peer.Metrics.LastUpdated = time.Now()
}

// GetLocalMetrics returns the local SGLang metrics (for monitoring)
func (lb *LoadBalancer) GetLocalMetrics() SGLangMetrics {
	lb.localMetricsMu.RLock()
	defer lb.localMetricsMu.RUnlock()
	return lb.localMetrics
}

// GetPeerMetrics returns metrics for all peers (for monitoring/debugging)
func (lb *LoadBalancer) GetPeerMetrics() map[string]SGLangMetrics {
	lb.peersMu.RLock()
	defer lb.peersMu.RUnlock()

	metrics := make(map[string]SGLangMetrics)
	for peer := range lb.peers {
		metrics[peer.Instance.ID] = peer.Metrics
	}
	return metrics
}

// GetPeerAvailability returns availability status for all peers
func (lb *LoadBalancer) GetPeerAvailability() map[string]AvailabilityStatus {
	lb.peersMu.RLock()
	defer lb.peersMu.RUnlock()

	availability := make(map[string]AvailabilityStatus)
	for peer, status := range lb.peers {
		availability[peer.Instance.ID] = status
	}
	return availability
}

// FlushSGLangCache sends a POST request to flush the KV cache (RadixAttention cache)
// This is essential for accurate TTFT benchmarking as it removes any prefix cache hits
func FlushSGLangCache(serverURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}

	// SGLang's flush_cache endpoint
	flushURL := serverURL + "/flush_cache"

	req, err := http.NewRequest("POST", flushURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create flush request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("flush request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("flush returned status %d", resp.StatusCode)
	}

	return nil
}

// GetSGLangCacheStats retrieves current cache statistics from SGLang
func GetSGLangCacheStats(serverURL string, timeout time.Duration) (map[string]interface{}, error) {
	client := &http.Client{Timeout: timeout}

	statsURL := serverURL + "/get_server_info"

	resp, err := client.Get(statsURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get server info: %w", err)
	}
	defer resp.Body.Close()

	var stats map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return stats, nil
}
