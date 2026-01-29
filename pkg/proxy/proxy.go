/*
Copyright © 2025 ALESSIO TONIOLO

proxy.go implements a centralized HTTP proxy that routes requests to a pool of SGLang servers.
Unlike the distributed loadbalancer (which runs on each node and only tracks local state),
this proxy has a global view of ALL requests across ALL servers in the pool.

Cost calculation uses GORGO-style weighted costs:
- Queued requests: tokenCount * msPerToken
- Running requests: tokenCount * msPerToken * runningCostFactor (qhat weight)

The proxy tracks:
- Requests it has dispatched to each server (running requests with token counts)
- Requests waiting in each server's queue (from SGLang metrics polling)
- Prefix cache locations for GORGO routing decisions
*/
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProxyConfig configures the centralized proxy
type ProxyConfig struct {
	// Proxy server settings
	ListenPort int `json:"listen_port"`

	// Request handling
	RequestTimeout time.Duration `json:"request_timeout"`
	QueueEnabled   bool          `json:"queue_enabled"`
	QueueTimeout   time.Duration `json:"queue_timeout"`
	MaxQueueSize   int           `json:"max_queue_size"`

	// GORGO cost parameters
	MsPerToken        float64 `json:"ms_per_token"`
	RunningCostFactor float64 `json:"running_cost_factor"` // qhat weight for running requests

	// Metrics polling
	MetricsEnabled      bool          `json:"metrics_enabled"`
	MetricsPollInterval time.Duration `json:"metrics_poll_interval"`
	MetricsEndpoint     string        `json:"metrics_endpoint"`
	MetricsTimeout      time.Duration `json:"metrics_timeout"`
	UnhealthyThreshold  int           `json:"unhealthy_threshold"`
	GPUCacheThreshold   float64       `json:"gpu_cache_threshold"`

	// Latency probing (separate from metrics for more accurate measurement)
	LatencyProbeEnabled  bool          `json:"latency_probe_enabled"`
	LatencyProbeInterval time.Duration `json:"latency_probe_interval"`
	LatencyProbeEndpoint string        `json:"latency_probe_endpoint"` // lightweight endpoint for latency measurement
	LatencyHistorySize   int           `json:"latency_history_size"`   // number of samples to keep for averaging
	LatencyEWMAAlpha     float64       `json:"latency_ewma_alpha"`     // exponential weighted moving average alpha (0-1)
}

// DefaultProxyConfig returns sensible defaults
func DefaultProxyConfig() *ProxyConfig {
	return &ProxyConfig{
		ListenPort:          8000,
		RequestTimeout:      60 * time.Second,
		QueueEnabled:        true,
		QueueTimeout:        60 * time.Second,
		MaxQueueSize:        10000,
		MsPerToken:          0.094,  // Default for 8xA100 with Mistral-7B
		RunningCostFactor:   0.5,    // qhat weight
		MetricsEnabled:      true,
		MetricsPollInterval: 500 * time.Millisecond,
		MetricsEndpoint:     "/metrics",
		MetricsTimeout:      500 * time.Millisecond,
		UnhealthyThreshold:  3,
		GPUCacheThreshold:   0.95,
		// Latency probing - runs more frequently than metrics for accurate RTT
		LatencyProbeEnabled:  true,
		LatencyProbeInterval: 100 * time.Millisecond, // probe every 100ms for responsive routing
		LatencyProbeEndpoint: "/health",              // lightweight endpoint
		LatencyHistorySize:   20,                     // keep last 20 samples
		LatencyEWMAAlpha:     0.3,                    // weight recent samples more heavily
	}
}

// SGLangServer represents a single SGLang backend server
type SGLangServer struct {
	ID   string `json:"id"`
	IP   string `json:"ip"`
	Port int    `json:"port"`

	// Metrics from SGLang (polled periodically)
	Metrics SGLangMetrics `json:"metrics"`

	// Proxy-tracked running requests (requests we dispatched that are still processing)
	// This is MORE accurate than polling since we track exact token counts
	runningRequests   map[string]*trackedRequest
	runningRequestsMu sync.RWMutex

	// Network latency tracking
	Latency          time.Duration   `json:"latency"`           // Current/latest latency
	LatencyAvg       time.Duration   `json:"latency_avg"`       // Exponential moving average
	LatencyMin       time.Duration   `json:"latency_min"`       // Min observed latency
	LatencyMax       time.Duration   `json:"latency_max"`       // Max observed latency
	latencyHistory   []time.Duration                            // Recent latency samples
	latencyMu        sync.RWMutex
}

// SGLangMetrics from the server's /metrics or /get_server_info endpoint
type SGLangMetrics struct {
	NumRunningReqs   int       `json:"num_running_reqs"`
	NumWaitingReqs   int       `json:"num_waiting_reqs"`
	NumTotalReqs     int       `json:"num_total_reqs"`
	GPUCacheUsage    float64   `json:"gpu_cache_usage"`
	MaxRunningReqs   int       `json:"max_running_reqs"`
	LastUpdated      time.Time `json:"last_updated"`
	Healthy          bool      `json:"healthy"`
	ConsecutiveFails int       `json:"consecutive_fails"`
}

// trackedRequest represents a request dispatched by the proxy
type trackedRequest struct {
	RequestID   string
	TokenCount  int
	Prompt      string
	DispatchedAt time.Time
	Server      *SGLangServer
}

// queuedRequest represents a request waiting in the proxy's queue
type queuedRequest struct {
	w           http.ResponseWriter
	r           *http.Request
	done        chan struct{}
	err         error
	enqueuedAt  time.Time
	requestID   string
	bodyBytes   []byte
	tokenCount  int
	prompt      string
}

// GORGONode for prefix tree tracking
type GORGONode struct {
	children map[byte]*GORGONode
	servers  []*SGLangServer // servers that have this prefix cached
	prefix   string
}

// HttpProxy is the centralized proxy with global request tracking
type HttpProxy struct {
	config *ProxyConfig

	// Server pool
	servers   []*SGLangServer
	serversMu sync.RWMutex

	// Global request queue (when all servers at capacity)
	queue   []*queuedRequest
	queueMu sync.Mutex

	// Prefix tree for GORGO routing (tracks which servers have which prefixes cached)
	prefixTree   *GORGONode
	prefixTreeMu sync.RWMutex

	// GORGO tuning parameters (runtime adjustable)
	msPerToken        float64
	runningCostFactor float64
	tuningMu          sync.RWMutex

	// HTTP client for forwarding requests
	httpClient *http.Client

	// Control
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running atomic.Bool

	// Aggregate metrics
	startTime              time.Time
	totalRequestsHandled   atomic.Int64
	totalRequestsForwarded atomic.Int64
	totalRequestsQueued    atomic.Int64
	totalRequestsRejected  atomic.Int64
}

// NewHttpProxy creates a new centralized proxy
func NewHttpProxy(cfg *ProxyConfig) *HttpProxy {
	if cfg == nil {
		cfg = DefaultProxyConfig()
	}

	ctx, cancel := context.WithCancel(context.Background())

	proxy := &HttpProxy{
		config:            cfg,
		servers:           make([]*SGLangServer, 0),
		queue:             make([]*queuedRequest, 0),
		prefixTree:        newGORGONode(),
		msPerToken:        cfg.MsPerToken,
		runningCostFactor: cfg.RunningCostFactor,
		httpClient: &http.Client{
			Timeout: cfg.RequestTimeout,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		ctx:       ctx,
		cancel:    cancel,
		startTime: time.Now(),
	}

	return proxy
}

func newGORGONode() *GORGONode {
	return &GORGONode{
		children: make(map[byte]*GORGONode),
		servers:  make([]*SGLangServer, 0),
		prefix:   "",
	}
}

// AddServer adds an SGLang server to the pool
func (p *HttpProxy) AddServer(id, ip string, port int) {
	p.serversMu.Lock()
	defer p.serversMu.Unlock()

	// Check if already exists
	for _, s := range p.servers {
		if s.IP == ip && s.Port == port {
			log.Printf("[Proxy] Server %s:%d already in pool", ip, port)
			return
		}
	}

	server := &SGLangServer{
		ID:   id,
		IP:   ip,
		Port: port,
		Metrics: SGLangMetrics{
			Healthy:     true,
			LastUpdated: time.Now(),
		},
		runningRequests: make(map[string]*trackedRequest),
	}

	p.servers = append(p.servers, server)
	log.Printf("[Proxy] Added server: %s (%s:%d)", id, ip, port)
}

// RemoveServer removes a server from the pool
func (p *HttpProxy) RemoveServer(id string) {
	p.serversMu.Lock()
	defer p.serversMu.Unlock()

	for i, s := range p.servers {
		if s.ID == id {
			p.servers = append(p.servers[:i], p.servers[i+1:]...)
			log.Printf("[Proxy] Removed server: %s", id)
			return
		}
	}
}

// Start starts the proxy
func (p *HttpProxy) Start() error {
	if p.running.Load() {
		return fmt.Errorf("proxy already running")
	}

	p.running.Store(true)

	// Warn if no servers configured
	p.serversMu.RLock()
	serverCount := len(p.servers)
	p.serversMu.RUnlock()
	if serverCount == 0 {
		log.Printf("[Proxy] ⚠️  WARNING: No servers configured!")
	}

	// Start queue processor if enabled
	if p.config.QueueEnabled {
		p.wg.Add(1)
		go p.processQueue()
	}

	// Start metrics poller if enabled
	if p.config.MetricsEnabled {
		p.wg.Add(1)
		go p.pollMetricsLoop()
	}

	// Start latency prober if enabled
	if p.config.LatencyProbeEnabled {
		p.wg.Add(1)
		go p.probeLatencyLoop()
	}

	log.Printf("[Proxy] Started (servers: %d, max queue: %d, latency probing: %v)",
		serverCount, p.config.MaxQueueSize, p.config.LatencyProbeEnabled)
	return nil
}

// Stop stops the proxy
func (p *HttpProxy) Stop() {
	if !p.running.Load() {
		return
	}

	p.running.Store(false)
	p.cancel()
	p.wg.Wait()

	log.Printf("[Proxy] Stopped")
}

// Handler returns the HTTP handler for the proxy
func (p *HttpProxy) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Route management endpoints
		switch r.URL.Path {
		case "/proxy/status":
			p.handleStatusEndpoint(w, r)
			return
		case "/proxy/health":
			p.handleHealthEndpoint(w, r)
			return
		case "/proxy/servers":
			p.handleServersEndpoint(w, r)
			return
		case "/proxy/tune":
			p.handleTuneEndpoint(w, r)
			return
		case "/proxy/cache/clear":
			p.handleCacheClearEndpoint(w, r)
			return
		case "/proxy/latency":
			p.handleLatencyEndpoint(w, r)
			return
		}

		// Regular request processing
		requestID := fmt.Sprintf("%d", time.Now().UnixNano())
		p.totalRequestsHandled.Add(1)

		// Pre-calculate token count and extract prompt
		var bodyBytes []byte
		var tokenCount int
		var prompt string
		if r.Body != nil {
			bodyBytes, _ = io.ReadAll(r.Body)
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			tokenCount, prompt = extractPromptAndTokenCount(bodyBytes)
		}

		// Select best server using GORGO cost calculation
		server := p.selectServer(prompt, tokenCount)
		if server != nil {
			if p.forwardToServer(w, r, requestID, server, bodyBytes, tokenCount, prompt) {
				p.totalRequestsForwarded.Add(1)
				return
			}
		}

		// No server available or forward failed - queue the request
		if p.config.QueueEnabled {
			if p.enqueueRequest(w, r, requestID, bodyBytes, tokenCount, prompt) {
				p.totalRequestsQueued.Add(1)
			} else {
				p.totalRequestsRejected.Add(1)
			}
			return
		}

		p.totalRequestsRejected.Add(1)
		http.Error(w, "No servers available", http.StatusServiceUnavailable)
	})
}

// selectServer chooses the best server using GORGO cost calculation
// Returns nil if no healthy servers are available
func (p *HttpProxy) selectServer(prompt string, requestTokens int) *SGLangServer {
	p.serversMu.RLock()
	defer p.serversMu.RUnlock()

	if len(p.servers) == 0 {
		return nil
	}

	p.tuningMu.RLock()
	msPerToken := p.msPerToken
	runningCostFactor := p.runningCostFactor
	p.tuningMu.RUnlock()

	// Find servers with prefix cache match
	matchedServers := p.findPrefixMatches(prompt)

	type serverCost struct {
		server      *SGLangServer
		cost        float64
		matchedLen  int
	}

	candidates := make([]serverCost, 0, len(p.servers))

	// Calculate cost for each healthy server
	for _, server := range p.servers {
		if !server.Metrics.Healthy {
			continue
		}

		// Check capacity
		if server.Metrics.GPUCacheUsage >= p.config.GPUCacheThreshold {
			continue
		}

		cost := p.calculateServerCost(server, prompt, requestTokens, msPerToken, runningCostFactor)

		// Check if this server has a prefix match
		matchedLen := 0
		for _, match := range matchedServers {
			if match.server == server && match.matchedLen > matchedLen {
				matchedLen = match.matchedLen
			}
		}

		// Subtract matched prefix cost (already cached)
		if matchedLen > 0 {
			matchedTokens := estimateTokenCount(prompt[:matchedLen])
			cost -= float64(matchedTokens) * msPerToken
		}

		candidates = append(candidates, serverCost{
			server:     server,
			cost:       cost,
			matchedLen: matchedLen,
		})
	}

	if len(candidates) == 0 {
		return nil
	}

	// Sort by cost (ascending)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].cost < candidates[j].cost
	})

	return candidates[0].server
}

// calculateServerCost computes the GORGO cost for a server
// Cost = (queued_tokens * msPerToken) + (running_tokens * msPerToken * runningCostFactor) + (request_tokens * msPerToken) + latency
func (p *HttpProxy) calculateServerCost(server *SGLangServer, prompt string, requestTokens int, msPerToken, runningCostFactor float64) float64 {
	var cost float64

	// Cost from running requests (tracked by proxy)
	server.runningRequestsMu.RLock()
	for _, req := range server.runningRequests {
		cost += float64(req.TokenCount) * msPerToken * runningCostFactor
	}
	runningCount := len(server.runningRequests)
	server.runningRequestsMu.RUnlock()

	// Cost from queued requests at the server (from SGLang metrics)
	// We use the difference between SGLang's reported running and our tracked running
	// to estimate the actual server-side state
	queuedAtServer := server.Metrics.NumWaitingReqs

	// Estimate tokens for queued requests (we don't have exact counts)
	// Use average token count based on running requests or default
	avgTokens := 500 // default estimate
	if runningCount > 0 {
		server.runningRequestsMu.RLock()
		totalTokens := 0
		for _, req := range server.runningRequests {
			totalTokens += req.TokenCount
		}
		server.runningRequestsMu.RUnlock()
		avgTokens = totalTokens / runningCount
	}

	cost += float64(queuedAtServer*avgTokens) * msPerToken

	// Cost for the incoming request itself (full prefill)
	cost += float64(requestTokens) * msPerToken

	// Add network latency (use EWMA for more stable routing decisions)
	server.latencyMu.RLock()
	latencyToUse := server.LatencyAvg
	if latencyToUse == 0 {
		latencyToUse = server.Latency // fallback to current if no EWMA yet
	}
	server.latencyMu.RUnlock()
	cost += float64(latencyToUse.Milliseconds())

	return cost
}

// forwardToServer forwards a request to a specific server
func (p *HttpProxy) forwardToServer(w http.ResponseWriter, r *http.Request, requestID string, server *SGLangServer, bodyBytes []byte, tokenCount int, prompt string) bool {
	// Build target URL
	targetURL := fmt.Sprintf("http://%s:%d%s", server.IP, server.Port, r.URL.Path)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// Create forwarded request
	ctx, cancel := context.WithTimeout(r.Context(), p.config.RequestTimeout)
	defer cancel()

	forwardReq, err := http.NewRequestWithContext(ctx, r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		log.Printf("[Proxy] Failed to create forward request %s: %v", requestID, err)
		return false
	}

	// Copy headers
	for key, values := range r.Header {
		for _, value := range values {
			forwardReq.Header.Add(key, value)
		}
	}
	forwardReq.Header.Set("X-Forwarded-For", r.RemoteAddr)
	forwardReq.Header.Set("X-Request-ID", requestID)

	// Track this request as running
	tracked := &trackedRequest{
		RequestID:    requestID,
		TokenCount:   tokenCount,
		Prompt:       prompt,
		DispatchedAt: time.Now(),
		Server:       server,
	}
	server.runningRequestsMu.Lock()
	server.runningRequests[requestID] = tracked
	server.runningRequestsMu.Unlock()

	// Execute request
	resp, err := p.httpClient.Do(forwardReq)

	// Untrack request when done
	server.runningRequestsMu.Lock()
	delete(server.runningRequests, requestID)
	server.runningRequestsMu.Unlock()

	if err != nil {
		log.Printf("[Proxy] Failed to forward request %s to %s: %v", requestID, server.IP, err)
		return false
	}
	defer resp.Body.Close()

	// Record prefix in tree on success
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && prompt != "" {
		p.insertPrefix(prompt, server)
	}

	// Copy response back
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	log.Printf("[Proxy] Forwarded request %s to %s (status: %d)", requestID, server.IP, resp.StatusCode)
	return true
}

// enqueueRequest adds a request to the proxy's global queue
func (p *HttpProxy) enqueueRequest(w http.ResponseWriter, r *http.Request, requestID string, bodyBytes []byte, tokenCount int, prompt string) bool {
	p.queueMu.Lock()
	if p.config.MaxQueueSize > 0 && len(p.queue) >= p.config.MaxQueueSize {
		p.queueMu.Unlock()
		log.Printf("[Proxy] Queue full (%d/%d), rejecting request", len(p.queue), p.config.MaxQueueSize)
		http.Error(w, "Queue full", http.StatusServiceUnavailable)
		return false
	}

	qr := &queuedRequest{
		w:          w,
		r:          r,
		done:       make(chan struct{}),
		enqueuedAt: time.Now(),
		requestID:  requestID,
		bodyBytes:  bodyBytes,
		tokenCount: tokenCount,
		prompt:     prompt,
	}

	p.queue = append(p.queue, qr)
	queueSize := len(p.queue)
	p.queueMu.Unlock()

	log.Printf("[Proxy] Request %s queued (queue size: %d, tokens: %d)", requestID, queueSize, tokenCount)

	// Wait for processing or timeout
	select {
	case <-qr.done:
		if qr.err != nil {
			http.Error(w, qr.err.Error(), http.StatusServiceUnavailable)
		}
	case <-time.After(p.config.QueueTimeout):
		http.Error(w, "Queue timeout", http.StatusGatewayTimeout)
	case <-r.Context().Done():
		http.Error(w, "Request cancelled", http.StatusRequestTimeout)
	}
	return true
}

// processQueue continuously processes queued requests
func (p *HttpProxy) processQueue() {
	defer p.wg.Done()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.processQueuedRequests()
		}
	}
}

func (p *HttpProxy) processQueuedRequests() {
	for {
		p.queueMu.Lock()
		if len(p.queue) == 0 {
			p.queueMu.Unlock()
			return
		}
		qr := p.queue[0]
		p.queueMu.Unlock()

		// Try to select a server
		server := p.selectServer(qr.prompt, qr.tokenCount)
		if server == nil {
			// No server available, stop processing queue
			return
		}

		// Remove from queue
		p.queueMu.Lock()
		if len(p.queue) > 0 && p.queue[0] == qr {
			p.queue = p.queue[1:]
		}
		p.queueMu.Unlock()

		// Forward the request
		go func(qr *queuedRequest, server *SGLangServer) {
			// Restore body for forwarding
			qr.r.Body = io.NopCloser(bytes.NewReader(qr.bodyBytes))

			success := p.forwardToServer(qr.w, qr.r, qr.requestID, server, qr.bodyBytes, qr.tokenCount, qr.prompt)
			if !success {
				qr.err = fmt.Errorf("forward failed")
			}
			close(qr.done)
		}(qr, server)
	}
}

// =============================================================================
// METRICS POLLING
// =============================================================================

func (p *HttpProxy) pollMetricsLoop() {
	defer p.wg.Done()

	ticker := time.NewTicker(p.config.MetricsPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.pollAllServers()
		}
	}
}

func (p *HttpProxy) pollAllServers() {
	p.serversMu.RLock()
	servers := make([]*SGLangServer, len(p.servers))
	copy(servers, p.servers)
	p.serversMu.RUnlock()

	var wg sync.WaitGroup
	for _, server := range servers {
		wg.Add(1)
		go func(s *SGLangServer) {
			defer wg.Done()
			p.pollServerMetrics(s)
		}(server)
	}
	wg.Wait()
}

func (p *HttpProxy) pollServerMetrics(server *SGLangServer) {
	ctx, cancel := context.WithTimeout(p.ctx, p.config.MetricsTimeout)
	defer cancel()

	metricsURL := fmt.Sprintf("http://%s:%d%s", server.IP, server.Port, p.config.MetricsEndpoint)

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", metricsURL, nil)
	if err != nil {
		p.markServerUnhealthy(server)
		return
	}

	resp, err := p.httpClient.Do(req)
	latency := time.Since(start)

	if err != nil {
		p.markServerUnhealthy(server)
		log.Printf("[Proxy] Failed to poll %s: %v", server.IP, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.markServerUnhealthy(server)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		p.markServerUnhealthy(server)
		return
	}

	metrics, err := parseMetrics(string(body))
	if err != nil {
		p.markServerUnhealthy(server)
		return
	}

	// Update server metrics
	p.serversMu.Lock()
	server.Metrics = *metrics
	server.Metrics.Healthy = true
	server.Metrics.ConsecutiveFails = 0
	server.Metrics.LastUpdated = time.Now()
	server.Latency = latency
	p.serversMu.Unlock()
}

func (p *HttpProxy) markServerUnhealthy(server *SGLangServer) {
	p.serversMu.Lock()
	defer p.serversMu.Unlock()

	server.Metrics.ConsecutiveFails++
	if server.Metrics.ConsecutiveFails >= p.config.UnhealthyThreshold {
		if server.Metrics.Healthy {
			log.Printf("[Proxy] Server %s marked unhealthy after %d failures", server.IP, server.Metrics.ConsecutiveFails)
		}
		server.Metrics.Healthy = false
	}
	server.Metrics.LastUpdated = time.Now()
}

// =============================================================================
// LATENCY PROBING
// =============================================================================

// probeLatencyLoop continuously probes network latency to all servers
func (p *HttpProxy) probeLatencyLoop() {
	defer p.wg.Done()

	ticker := time.NewTicker(p.config.LatencyProbeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.probeAllServers()
		}
	}
}

// probeAllServers probes latency to all servers concurrently
func (p *HttpProxy) probeAllServers() {
	p.serversMu.RLock()
	servers := make([]*SGLangServer, len(p.servers))
	copy(servers, p.servers)
	p.serversMu.RUnlock()

	var wg sync.WaitGroup
	for _, server := range servers {
		wg.Add(1)
		go func(s *SGLangServer) {
			defer wg.Done()
			p.probeServerLatency(s)
		}(server)
	}
	wg.Wait()
}

// probeServerLatency measures network latency to a single server using a lightweight request
func (p *HttpProxy) probeServerLatency(server *SGLangServer) {
	ctx, cancel := context.WithTimeout(p.ctx, p.config.MetricsTimeout)
	defer cancel()

	// Use lightweight endpoint for latency measurement
	probeURL := fmt.Sprintf("http://%s:%d%s", server.IP, server.Port, p.config.LatencyProbeEndpoint)

	// Measure round-trip time
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", probeURL, nil)
	if err != nil {
		return
	}

	resp, err := p.httpClient.Do(req)
	latency := time.Since(start)

	if err != nil {
		// Don't mark unhealthy here - metrics poller handles health
		return
	}
	resp.Body.Close()

	// Only count successful probes
	if resp.StatusCode < 500 {
		p.updateServerLatency(server, latency)
	}
}

// updateServerLatency updates the latency statistics for a server
func (p *HttpProxy) updateServerLatency(server *SGLangServer, latency time.Duration) {
	server.latencyMu.Lock()
	defer server.latencyMu.Unlock()

	// Update current latency
	server.Latency = latency

	// Update min/max
	if server.LatencyMin == 0 || latency < server.LatencyMin {
		server.LatencyMin = latency
	}
	if latency > server.LatencyMax {
		server.LatencyMax = latency
	}

	// Update history
	if server.latencyHistory == nil {
		server.latencyHistory = make([]time.Duration, 0, p.config.LatencyHistorySize)
	}
	server.latencyHistory = append(server.latencyHistory, latency)
	if len(server.latencyHistory) > p.config.LatencyHistorySize {
		server.latencyHistory = server.latencyHistory[1:]
	}

	// Calculate exponential weighted moving average
	if server.LatencyAvg == 0 {
		server.LatencyAvg = latency
	} else {
		alpha := p.config.LatencyEWMAAlpha
		server.LatencyAvg = time.Duration(alpha*float64(latency) + (1-alpha)*float64(server.LatencyAvg))
	}
}

// GetServerLatency returns latency stats for a server (thread-safe)
func (p *HttpProxy) GetServerLatency(serverID string) (current, avg, min, max time.Duration, ok bool) {
	p.serversMu.RLock()
	defer p.serversMu.RUnlock()

	for _, s := range p.servers {
		if s.ID == serverID {
			s.latencyMu.RLock()
			current = s.Latency
			avg = s.LatencyAvg
			min = s.LatencyMin
			max = s.LatencyMax
			s.latencyMu.RUnlock()
			return current, avg, min, max, true
		}
	}
	return 0, 0, 0, 0, false
}

func parseMetrics(body string) (*SGLangMetrics, error) {
	metrics := &SGLangMetrics{}

	// Try Prometheus format first
	if strings.HasPrefix(body, "#") || strings.Contains(body, "sglang:") {
		lines := strings.Split(body, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}

			metricName := parts[0]
			if idx := strings.Index(metricName, "{"); idx != -1 {
				metricName = metricName[:idx]
			}

			var value float64
			fmt.Sscanf(parts[1], "%f", &value)

			switch metricName {
			case "sglang:num_running_reqs":
				metrics.NumRunningReqs = int(value)
			case "sglang:num_waiting_reqs":
				metrics.NumWaitingReqs = int(value)
			case "sglang:token_usage":
				metrics.GPUCacheUsage = value
			case "sglang:max_running_reqs":
				metrics.MaxRunningReqs = int(value)
			}
		}
		metrics.NumTotalReqs = metrics.NumRunningReqs + metrics.NumWaitingReqs
		return metrics, nil
	}

	// Try JSON format
	var serverInfo struct {
		NumRunningReqs int     `json:"num_running_reqs"`
		NumWaitingReqs int     `json:"num_waiting_reqs"`
		MaxRunningReqs int     `json:"max_running_reqs"`
		GPUCacheUsage  float64 `json:"token_usage"`
	}
	if err := json.Unmarshal([]byte(body), &serverInfo); err != nil {
		return nil, err
	}

	metrics.NumRunningReqs = serverInfo.NumRunningReqs
	metrics.NumWaitingReqs = serverInfo.NumWaitingReqs
	metrics.NumTotalReqs = serverInfo.NumRunningReqs + serverInfo.NumWaitingReqs
	metrics.GPUCacheUsage = serverInfo.GPUCacheUsage
	metrics.MaxRunningReqs = serverInfo.MaxRunningReqs

	return metrics, nil
}

// =============================================================================
// PREFIX TREE (GORGO)
// =============================================================================

type prefixMatch struct {
	server     *SGLangServer
	matchedLen int
}

func (p *HttpProxy) findPrefixMatches(prompt string) []prefixMatch {
	if prompt == "" {
		return nil
	}

	p.prefixTreeMu.RLock()
	defer p.prefixTreeMu.RUnlock()

	matches := []prefixMatch{}
	currentNode := p.prefixTree
	i := 0
	matchedLen := 0

	for i < len(prompt) {
		currentChar := prompt[i]
		lookup, ok := currentNode.children[currentChar]
		if !ok {
			break
		}

		sharedLen := sharedPrefixLength(prompt[i:], lookup.prefix)
		i += sharedLen
		matchedLen = i

		// Record matches at this node
		for _, server := range lookup.servers {
			matches = append(matches, prefixMatch{
				server:     server,
				matchedLen: matchedLen,
			})
		}

		if sharedLen < len(lookup.prefix) {
			break
		}
		currentNode = lookup
	}

	return matches
}

func (p *HttpProxy) insertPrefix(prefix string, server *SGLangServer) {
	if prefix == "" || server == nil {
		return
	}

	p.prefixTreeMu.Lock()
	defer p.prefixTreeMu.Unlock()

	currentNode := p.prefixTree
	i := 0

	for i < len(prefix) {
		currentChar := prefix[i]
		remainingText := prefix[i:]
		lookup, ok := currentNode.children[currentChar]

		if !ok {
			newNode := &GORGONode{
				children: make(map[byte]*GORGONode),
				servers:  []*SGLangServer{server},
				prefix:   remainingText,
			}
			currentNode.children[currentChar] = newNode
			return
		}

		sharedLen := sharedPrefixLength(remainingText, lookup.prefix)

		if sharedLen < len(lookup.prefix) {
			// Split node
			matchingPart := lookup.prefix[:sharedLen]
			oldRemainingPart := lookup.prefix[sharedLen:]

			newNode := &GORGONode{
				children: make(map[byte]*GORGONode),
				servers:  []*SGLangServer{},
				prefix:   matchingPart,
			}

			lookup.prefix = oldRemainingPart
			newNode.children[oldRemainingPart[0]] = lookup
			currentNode.children[currentChar] = newNode

			i += sharedLen

			if i < len(prefix) {
				finalNode := &GORGONode{
					children: make(map[byte]*GORGONode),
					servers:  []*SGLangServer{server},
					prefix:   prefix[i:],
				}
				newNode.children[prefix[i]] = finalNode
			} else {
				newNode.servers = append(newNode.servers, server)
			}
			return
		}

		i += sharedLen
		if i >= len(prefix) {
			// Check for duplicates
			for _, s := range lookup.servers {
				if s == server {
					return
				}
			}
			lookup.servers = append(lookup.servers, server)
			return
		}
		currentNode = lookup
	}
}

func sharedPrefixLength(a, b string) int {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	for i := 0; i < minLen; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return minLen
}

// ClearPrefixCache resets the prefix tree
func (p *HttpProxy) ClearPrefixCache() int {
	p.prefixTreeMu.Lock()
	defer p.prefixTreeMu.Unlock()
	count := countNodes(p.prefixTree)
	p.prefixTree = newGORGONode()
	return count
}

func countNodes(n *GORGONode) int {
	if n == nil {
		return 0
	}
	count := 1
	for _, child := range n.children {
		count += countNodes(child)
	}
	return count
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func extractPromptAndTokenCount(bodyBytes []byte) (int, string) {
	if len(bodyBytes) == 0 {
		return 0, ""
	}

	// Try chat completions format
	var chatReq struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(bodyBytes, &chatReq); err == nil && len(chatReq.Messages) > 0 {
		var builder strings.Builder
		for _, msg := range chatReq.Messages {
			builder.WriteString(msg.Content)
			builder.WriteString(" ")
		}
		prompt := strings.TrimSpace(builder.String())
		return estimateTokenCount(prompt), prompt
	}

	// Try legacy completions format
	var legacyReq struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(bodyBytes, &legacyReq); err == nil && legacyReq.Prompt != "" {
		return estimateTokenCount(legacyReq.Prompt), legacyReq.Prompt
	}

	return 0, ""
}

func estimateTokenCount(text string) int {
	// Simple estimation: ~4 chars per token
	return (len(text) + 3) / 4
}

// =============================================================================
// MANAGEMENT ENDPOINTS
// =============================================================================

func (p *HttpProxy) handleStatusEndpoint(w http.ResponseWriter, r *http.Request) {
	p.serversMu.RLock()
	servers := make([]map[string]interface{}, 0, len(p.servers))
	for _, s := range p.servers {
		s.runningRequestsMu.RLock()
		runningCount := len(s.runningRequests)
		s.runningRequestsMu.RUnlock()

		s.latencyMu.RLock()
		latencyStats := map[string]interface{}{
			"current_ms": s.Latency.Milliseconds(),
			"avg_ms":     s.LatencyAvg.Milliseconds(),
			"min_ms":     s.LatencyMin.Milliseconds(),
			"max_ms":     s.LatencyMax.Milliseconds(),
		}
		s.latencyMu.RUnlock()

		servers = append(servers, map[string]interface{}{
			"id":                    s.ID,
			"ip":                    s.IP,
			"port":                  s.Port,
			"healthy":               s.Metrics.Healthy,
			"running_reqs":          s.Metrics.NumRunningReqs,
			"waiting_reqs":          s.Metrics.NumWaitingReqs,
			"gpu_cache_usage":       s.Metrics.GPUCacheUsage,
			"latency":               latencyStats,
			"proxy_tracked_running": runningCount,
		})
	}
	p.serversMu.RUnlock()

	p.queueMu.Lock()
	queueSize := len(p.queue)
	p.queueMu.Unlock()

	status := map[string]interface{}{
		"running":            p.running.Load(),
		"uptime_seconds":     time.Since(p.startTime).Seconds(),
		"servers":            servers,
		"queue_size":         queueSize,
		"total_handled":      p.totalRequestsHandled.Load(),
		"total_forwarded":    p.totalRequestsForwarded.Load(),
		"total_queued":       p.totalRequestsQueued.Load(),
		"total_rejected":     p.totalRequestsRejected.Load(),
		"ms_per_token":       p.msPerToken,
		"running_cost_factor": p.runningCostFactor,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (p *HttpProxy) handleHealthEndpoint(w http.ResponseWriter, r *http.Request) {
	p.serversMu.RLock()
	healthyCount := 0
	for _, s := range p.servers {
		if s.Metrics.Healthy {
			healthyCount++
		}
	}
	totalCount := len(p.servers)
	p.serversMu.RUnlock()

	if healthyCount == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, "unhealthy: 0/%d servers available", totalCount)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "healthy: %d/%d servers available", healthyCount, totalCount)
}

func (p *HttpProxy) handleServersEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		// Add server
		var req struct {
			ID   string `json:"id"`
			IP   string `json:"ip"`
			Port int    `json:"port"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.ID == "" {
			req.ID = fmt.Sprintf("%s:%d", req.IP, req.Port)
		}
		p.AddServer(req.ID, req.IP, req.Port)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "Added server %s", req.ID)
		return
	}

	if r.Method == http.MethodDelete {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		p.RemoveServer(id)
		fmt.Fprintf(w, "Removed server %s", id)
		return
	}

	// GET - list servers
	p.serversMu.RLock()
	defer p.serversMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p.servers)
}

func (p *HttpProxy) handleTuneEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		MsPerToken        *float64 `json:"ms_per_token"`
		RunningCostFactor *float64 `json:"running_cost_factor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	p.tuningMu.Lock()
	if req.MsPerToken != nil {
		p.msPerToken = *req.MsPerToken
	}
	if req.RunningCostFactor != nil {
		p.runningCostFactor = *req.RunningCostFactor
	}
	msPerToken := p.msPerToken
	runningCostFactor := p.runningCostFactor
	p.tuningMu.Unlock()

	log.Printf("[Proxy] Tuning updated: ms_per_token=%.4f, running_cost_factor=%.4f", msPerToken, runningCostFactor)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]float64{
		"ms_per_token":        msPerToken,
		"running_cost_factor": runningCostFactor,
	})
}

func (p *HttpProxy) handleCacheClearEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	count := p.ClearPrefixCache()
	log.Printf("[Proxy] Cleared prefix cache (%d nodes)", count)
	fmt.Fprintf(w, "Cleared %d nodes from prefix cache", count)
}

func (p *HttpProxy) handleLatencyEndpoint(w http.ResponseWriter, r *http.Request) {
	p.serversMu.RLock()
	defer p.serversMu.RUnlock()

	latencies := make(map[string]map[string]interface{})
	for _, s := range p.servers {
		s.latencyMu.RLock()
		latencies[s.ID] = map[string]interface{}{
			"ip":         s.IP,
			"port":       s.Port,
			"current_ms": s.Latency.Milliseconds(),
			"avg_ms":     s.LatencyAvg.Milliseconds(),
			"min_ms":     s.LatencyMin.Milliseconds(),
			"max_ms":     s.LatencyMax.Milliseconds(),
			"healthy":    s.Metrics.Healthy,
		}
		s.latencyMu.RUnlock()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(latencies)
}

// SetTuningParams allows runtime adjustment of GORGO parameters
func (p *HttpProxy) SetTuningParams(msPerToken, runningCostFactor float64) {
	p.tuningMu.Lock()
	defer p.tuningMu.Unlock()
	p.msPerToken = msPerToken
	p.runningCostFactor = runningCostFactor
}

// GetTuningParams returns current GORGO parameters
func (p *HttpProxy) GetTuningParams() (msPerToken, runningCostFactor float64) {
	p.tuningMu.RLock()
	defer p.tuningMu.RUnlock()
	return p.msPerToken, p.runningCostFactor
}
