/*
Copyright © 2025 ALESSIO TONIOLO

loadbalancer.go implements a distributed load balancer that runs on each node.
When a node reaches its max concurrent requests threshold, it forwards
requests to other healthy nodes in the cluster.
*/
package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/atoniolo76/gotoni/pkg/remote"
)

// LoadBalancerConfig configures the distributed load balancer
type LoadBalancerConfig struct {
	// MaxConcurrentRequests is the threshold before forwarding to peers
	MaxConcurrentRequests int `json:"max_concurrent_requests"`

	// LocalPort is the port the local backend service listens on
	LocalPort int `json:"local_port"`

	// ListenPort is the port the load balancer listens on
	ListenPort int `json:"listen_port"`

	// PeerPort is the port other nodes' load balancers listen on (usually same as ListenPort)
	PeerPort int `json:"peer_port"`

	// HealthCheckInterval is how often to check peer health
	HealthCheckInterval time.Duration `json:"health_check_interval"`

	// HealthCheckTimeout is the timeout for health check requests
	HealthCheckTimeout time.Duration `json:"health_check_timeout"`

	// RequestTimeout is the timeout for forwarded requests
	RequestTimeout time.Duration `json:"request_timeout"`

	// Strategy is the load balancing strategy: "round-robin", "least-loaded", "random"
	Strategy string `json:"strategy"`

	// QueueEnabled enables request queuing when all nodes are at capacity
	QueueEnabled bool `json:"queue_enabled"`

	// MaxQueueSize is the maximum number of requests to queue
	MaxQueueSize int `json:"max_queue_size"`

	// QueueTimeout is how long a request can wait in queue
	QueueTimeout time.Duration `json:"queue_timeout"`

	// NodeID is a unique identifier for this node
	NodeID string `json:"node_id"`

	// HealthEndpoint is the path to check for health (default: /health)
	HealthEndpoint string `json:"health_endpoint"`

	// MetricsEnabled enables metrics collection
	MetricsEnabled bool `json:"metrics_enabled"`
}

// DefaultLoadBalancerConfig returns sensible defaults
func DefaultLoadBalancerConfig() *LoadBalancerConfig {
	return &LoadBalancerConfig{
		MaxConcurrentRequests: 10,
		LocalPort:             8080,
		ListenPort:            8000,
		PeerPort:              8000,
		HealthCheckInterval:   5 * time.Second,
		HealthCheckTimeout:    2 * time.Second,
		RequestTimeout:        30 * time.Second,
		Strategy:              "least-loaded",
		QueueEnabled:          true,
		MaxQueueSize:          100,
		QueueTimeout:          30 * time.Second,
		NodeID:                "",
		HealthEndpoint:        "/health",
		MetricsEnabled:        true,
	}
}

// LoadBalancerPolicy defines the interface for peer selection strategies
type LoadBalancerPolicy interface {
	// Select chooses a peer from the available peers
	Select(peers []*PeerNode) *PeerNode
	// Name returns the name of the policy
	Name() string
}

// LeastLoadedPolicy selects the peer with the lowest current load
type LeastLoadedPolicy struct{}

func (p *LeastLoadedPolicy) Name() string {
	return "least-loaded"
}

func (p *LeastLoadedPolicy) Select(peers []*PeerNode) *PeerNode {
	if len(peers) == 0 {
		return nil
	}

	var best *PeerNode
	var lowestLoad int64 = -1

	for _, peer := range peers {
		// Skip unhealthy peers
		if !peer.Healthy {
			continue
		}

		// Skip peers at capacity
		if peer.CurrentLoad >= int64(peer.MaxLoad) {
			continue
		}

		if lowestLoad == -1 || peer.CurrentLoad < lowestLoad {
			lowestLoad = peer.CurrentLoad
			best = peer
		}
	}

	return best
}

// NewLoadBalancerPolicy creates a new load balancer policy based on the strategy name
func NewLoadBalancerPolicy(strategy string) LoadBalancerPolicy {
	switch strategy {
	case "least-loaded":
		return &LeastLoadedPolicy{}
	default:
		// Default to least-loaded
		return &LeastLoadedPolicy{}
	}
}

// PeerNode represents a peer node in the cluster
type PeerNode struct {
	Instance         *remote.RunningInstance `json:"instance"`
	Port             int                     `json:"port"`
	Healthy          bool                    `json:"healthy"`
	LastHealthCheck  time.Time               `json:"last_health_check"`
	CurrentLoad      int64                   `json:"current_load"`
	MaxLoad          int                     `json:"max_load"`
	ResponseTimeMs   int64                   `json:"response_time_ms"`
	ConsecutiveFails int                     `json:"consecutive_fails"`
	TotalRequests    int64                   `json:"total_requests"`
	FailedRequests   int64                   `json:"failed_requests"`
}

// PeerStatus is the status response from a peer's /lb/status endpoint
type PeerStatus struct {
	NodeID      string `json:"node_id"`
	CurrentLoad int64  `json:"current_load"`
	MaxLoad     int    `json:"max_load"`
	QueueSize   int    `json:"queue_size"`
	Healthy     bool   `json:"healthy"`
}

// LoadBalancerMetrics tracks load balancer performance
type LoadBalancerMetrics struct {
	TotalRequests     int64     `json:"total_requests"`
	LocalRequests     int64     `json:"local_requests"`
	ForwardedRequests int64     `json:"forwarded_requests"`
	QueuedRequests    int64     `json:"queued_requests"`
	DroppedRequests   int64     `json:"dropped_requests"`
	FailedForwards    int64     `json:"failed_forwards"`
	AvgResponseTimeMs float64   `json:"avg_response_time_ms"`
	CurrentConcurrent int64     `json:"current_concurrent"`
	PeakConcurrent    int64     `json:"peak_concurrent"`
	LastUpdated       time.Time `json:"last_updated"`
}

// queuedRequest represents a request waiting in the queue
type queuedRequest struct {
	w          http.ResponseWriter
	r          *http.Request
	done       chan struct{}
	err        error
	enqueuedAt time.Time
}

// NodeLoadBalancer is a distributed load balancer instance for a single node
type NodeLoadBalancer struct {
	config *LoadBalancerConfig

	// Peer management
	peers   map[string]*PeerNode
	peersMu sync.RWMutex

	// Load balancing policy
	policy LoadBalancerPolicy

	// Request tracking
	currentRequests atomic.Int64
	peakRequests    atomic.Int64

	// Metrics
	metrics   LoadBalancerMetrics
	metricsMu sync.RWMutex

	// Request queue
	queue     chan *queuedRequest
	queueSize atomic.Int64

	// HTTP clients
	httpClient *http.Client
	localProxy *httputil.ReverseProxy

	// Control
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running atomic.Bool

	// Response time tracking (exponential moving average)
	avgResponseTime atomic.Int64
}

// NewNodeLoadBalancer creates a new load balancer for this node
func NewNodeLoadBalancer(config *LoadBalancerConfig) *NodeLoadBalancer {
	if config == nil {
		config = DefaultLoadBalancerConfig()
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Create local backend proxy
	localURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", config.LocalPort))
	localProxy := httputil.NewSingleHostReverseProxy(localURL)

	// Customize proxy error handler
	localProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[LB] Local proxy error: %v", err)
		http.Error(w, "Backend unavailable", http.StatusBadGateway)
	}

	lb := &NodeLoadBalancer{
		config: config,
		peers:  make(map[string]*PeerNode),
		policy: NewLoadBalancerPolicy(config.Strategy),
		httpClient: &http.Client{
			Timeout: config.RequestTimeout,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		localProxy: localProxy,
		ctx:        ctx,
		cancel:     cancel,
	}

	if config.QueueEnabled {
		lb.queue = make(chan *queuedRequest, config.MaxQueueSize)
	}

	return lb
}

// AddPeer adds a peer node to the load balancer
func (lb *NodeLoadBalancer) AddPeer(instance *remote.RunningInstance, port int) {
	lb.peersMu.Lock()
	defer lb.peersMu.Unlock()

	lb.peers[instance.ID] = &PeerNode{
		Instance: instance,
		Port:     port,
		Healthy:  true, // Assume healthy until proven otherwise
		MaxLoad:  lb.config.MaxConcurrentRequests,
	}

	log.Printf("[LB] Added peer: %s (%s:%d)", instance.ID, instance.IP, port)
}

// RemovePeer removes a peer node from the load balancer
func (lb *NodeLoadBalancer) RemovePeer(id string) {
	lb.peersMu.Lock()
	defer lb.peersMu.Unlock()

	delete(lb.peers, id)
	log.Printf("[LB] Removed peer: %s", id)
}

// AddPeersFromCluster adds all instances from a cluster as peers
func (lb *NodeLoadBalancer) AddPeersFromCluster(cluster *Cluster) {
	for i := range cluster.Instances {
		inst := &cluster.Instances[i]
		// Skip self
		if inst.ID == lb.config.NodeID {
			continue
		}
		lb.AddPeer(inst, lb.config.PeerPort)
	}
}

// Start starts the load balancer
func (lb *NodeLoadBalancer) Start() error {
	if lb.running.Load() {
		return fmt.Errorf("load balancer already running")
	}

	lb.running.Store(true)

	// Start health checker
	lb.wg.Add(1)
	go lb.healthCheckLoop()

	// Start queue processor if enabled
	if lb.config.QueueEnabled {
		lb.wg.Add(1)
		go lb.processQueue()
	}

	// Start metrics collector if enabled
	if lb.config.MetricsEnabled {
		lb.wg.Add(1)
		go lb.collectMetrics()
	}

	log.Printf("[LB] Load balancer started (node: %s, max concurrent: %d, strategy: %s)",
		lb.config.NodeID, lb.config.MaxConcurrentRequests, lb.config.Strategy)

	return nil
}

// Stop stops the load balancer
func (lb *NodeLoadBalancer) Stop() {
	if !lb.running.Load() {
		return
	}

	lb.running.Store(false)
	lb.cancel()
	lb.wg.Wait()

	log.Printf("[LB] Load balancer stopped")
}

// Handler returns an HTTP handler that implements the load balancing logic
func (lb *NodeLoadBalancer) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handle internal LB endpoints
		if lb.handleInternalEndpoint(w, r) {
			return
		}

		startTime := time.Now()
		atomic.AddInt64(&lb.metrics.TotalRequests, 1)

		// Try to handle locally first
		currentLoad := lb.currentRequests.Load()

		if currentLoad < int64(lb.config.MaxConcurrentRequests) {
			// Handle locally
			lb.handleLocal(w, r, next, startTime)
			return
		}

		// Local capacity exceeded - try to forward to a peer
		log.Printf("[LB] Local capacity exceeded (%d/%d), attempting forward",
			currentLoad, lb.config.MaxConcurrentRequests)

		if lb.forwardToPeer(w, r, startTime) {
			return
		}

		// No healthy peers available - try queue or reject
		if lb.config.QueueEnabled && lb.queueSize.Load() < int64(lb.config.MaxQueueSize) {
			lb.enqueueRequest(w, r)
			return
		}

		// All options exhausted
		atomic.AddInt64(&lb.metrics.DroppedRequests, 1)
		http.Error(w, "Service at capacity", http.StatusServiceUnavailable)
	})
}

// handleLocal processes the request locally
func (lb *NodeLoadBalancer) handleLocal(w http.ResponseWriter, r *http.Request, next http.Handler, startTime time.Time) {
	// Increment concurrent request counter
	current := lb.currentRequests.Add(1)
	defer lb.currentRequests.Add(-1)

	// Track peak
	for {
		peak := lb.peakRequests.Load()
		if current <= peak || lb.peakRequests.CompareAndSwap(peak, current) {
			break
		}
	}

	atomic.AddInt64(&lb.metrics.LocalRequests, 1)

	// Process with the actual handler
	if next != nil {
		next.ServeHTTP(w, r)
	} else {
		lb.localProxy.ServeHTTP(w, r)
	}

	// Update response time (EMA with alpha=0.1)
	elapsed := time.Since(startTime).Milliseconds()
	lb.updateResponseTime(elapsed)
}

// forwardToPeer attempts to forward the request to a healthy peer
func (lb *NodeLoadBalancer) forwardToPeer(w http.ResponseWriter, r *http.Request, startTime time.Time) bool {
	peer := lb.selectPeer()
	if peer == nil {
		return false
	}

	// Build forward URL
	targetURL := fmt.Sprintf("http://%s:%d%s", peer.Instance.IP, peer.Port, r.URL.Path)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// Create forwarded request
	ctx, cancel := context.WithTimeout(r.Context(), lb.config.RequestTimeout)
	defer cancel()

	// Read and buffer the body so we can forward it
	var bodyBytes []byte
	if r.Body != nil {
		bodyBytes, _ = io.ReadAll(r.Body)
		r.Body.Close()
	}

	forwardReq, err := http.NewRequestWithContext(ctx, r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		log.Printf("[LB] Failed to create forward request: %v", err)
		atomic.AddInt64(&lb.metrics.FailedForwards, 1)
		return false
	}

	// Copy headers
	for key, values := range r.Header {
		for _, value := range values {
			forwardReq.Header.Add(key, value)
		}
	}

	// Add forwarding headers
	forwardReq.Header.Set("X-Forwarded-For", r.RemoteAddr)
	forwardReq.Header.Set("X-Forwarded-By", lb.config.NodeID)
	forwardReq.Header.Set("X-LB-Hop", "1")

	// Execute forward
	resp, err := lb.httpClient.Do(forwardReq)
	if err != nil {
		log.Printf("[LB] Forward to %s failed: %v", peer.Instance.ID, err)
		atomic.AddInt64(&lb.metrics.FailedForwards, 1)
		atomic.AddInt64(&peer.FailedRequests, 1)
		peer.ConsecutiveFails++
		return false
	}
	defer resp.Body.Close()

	// Copy response
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("X-Served-By", peer.Instance.ID)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	atomic.AddInt64(&lb.metrics.ForwardedRequests, 1)
	atomic.AddInt64(&peer.TotalRequests, 1)
	peer.ConsecutiveFails = 0

	// Update peer response time
	elapsed := time.Since(startTime).Milliseconds()
	atomic.StoreInt64(&peer.ResponseTimeMs, elapsed)

	log.Printf("[LB] Forwarded request to %s (took %dms)", peer.Instance.ID, elapsed)
	return true
}

// selectPeer selects a peer based on the configured policy
func (lb *NodeLoadBalancer) selectPeer() *PeerNode {
	lb.peersMu.RLock()
	defer lb.peersMu.RUnlock()

	// Use the configured policy to select a peer (policy handles filtering)
	return lb.policy.Select(lb.getPeerSlice())
}

// getPeerSlice returns a slice of all peer nodes (internal helper)
func (lb *NodeLoadBalancer) getPeerSlice() []*PeerNode {
	peers := make([]*PeerNode, 0, len(lb.peers))
	for _, peer := range lb.peers {
		peers = append(peers, peer)
	}
	return peers
}

// SetPolicy changes the load balancing policy at runtime
func (lb *NodeLoadBalancer) SetPolicy(policy LoadBalancerPolicy) {
	lb.peersMu.Lock()
	defer lb.peersMu.Unlock()
	lb.policy = policy
	log.Printf("[LB] Policy changed to: %s", policy.Name())
}

// GetPolicy returns the current load balancing policy
func (lb *NodeLoadBalancer) GetPolicy() LoadBalancerPolicy {
	lb.peersMu.RLock()
	defer lb.peersMu.RUnlock()
	return lb.policy
}

// GetPeerCount returns the number of peers
func (lb *NodeLoadBalancer) GetPeerCount() int {
	lb.peersMu.RLock()
	defer lb.peersMu.RUnlock()
	return len(lb.peers)
}

// GetHealthyPeerCount returns the number of healthy peers
func (lb *NodeLoadBalancer) GetHealthyPeerCount() int {
	lb.peersMu.RLock()
	defer lb.peersMu.RUnlock()

	count := 0
	for _, peer := range lb.peers {
		if peer.Healthy {
			count++
		}
	}
	return count
}

// enqueueRequest adds a request to the queue
func (lb *NodeLoadBalancer) enqueueRequest(w http.ResponseWriter, r *http.Request) {
	qr := &queuedRequest{
		w:          w,
		r:          r,
		done:       make(chan struct{}),
		enqueuedAt: time.Now(),
	}

	select {
	case lb.queue <- qr:
		lb.queueSize.Add(1)
		atomic.AddInt64(&lb.metrics.QueuedRequests, 1)
		log.Printf("[LB] Request queued (queue size: %d)", lb.queueSize.Load())

		// Wait for processing or timeout
		select {
		case <-qr.done:
			if qr.err != nil {
				http.Error(w, qr.err.Error(), http.StatusServiceUnavailable)
			}
		case <-time.After(lb.config.QueueTimeout):
			http.Error(w, "Queue timeout", http.StatusGatewayTimeout)
		case <-r.Context().Done():
			http.Error(w, "Request cancelled", http.StatusRequestTimeout)
		}

	default:
		atomic.AddInt64(&lb.metrics.DroppedRequests, 1)
		http.Error(w, "Queue full", http.StatusServiceUnavailable)
	}
}

// processQueue processes queued requests when capacity becomes available
func (lb *NodeLoadBalancer) processQueue() {
	defer lb.wg.Done()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-lb.ctx.Done():
			return
		case <-ticker.C:
			// Check if we have capacity
			if lb.currentRequests.Load() >= int64(lb.config.MaxConcurrentRequests) {
				continue
			}

			// Try to dequeue
			select {
			case qr := <-lb.queue:
				lb.queueSize.Add(-1)

				// Check if request is still valid
				if time.Since(qr.enqueuedAt) > lb.config.QueueTimeout {
					qr.err = fmt.Errorf("queue timeout")
					close(qr.done)
					continue
				}

				// Process the request
				startTime := time.Now()
				lb.handleLocal(qr.w, qr.r, nil, startTime)
				close(qr.done)

			default:
				// No requests in queue
			}
		}
	}
}

// healthCheckLoop periodically checks peer health
func (lb *NodeLoadBalancer) healthCheckLoop() {
	defer lb.wg.Done()

	ticker := time.NewTicker(lb.config.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-lb.ctx.Done():
			return
		case <-ticker.C:
			lb.checkAllPeers()
		}
	}
}

// checkAllPeers checks the health of all peers
func (lb *NodeLoadBalancer) checkAllPeers() {
	lb.peersMu.RLock()
	peers := make([]*PeerNode, 0, len(lb.peers))
	for _, peer := range lb.peers {
		peers = append(peers, peer)
	}
	lb.peersMu.RUnlock()

	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Add(1)
		go func(p *PeerNode) {
			defer wg.Done()
			lb.checkPeerHealth(p)
		}(peer)
	}
	wg.Wait()
}

// checkPeerHealth checks a single peer's health
func (lb *NodeLoadBalancer) checkPeerHealth(peer *PeerNode) {
	ctx, cancel := context.WithTimeout(lb.ctx, lb.config.HealthCheckTimeout)
	defer cancel()

	// Check the peer's load balancer status endpoint
	statusURL := fmt.Sprintf("http://%s:%d/lb/status", peer.Instance.IP, peer.Port)
	req, err := http.NewRequestWithContext(ctx, "GET", statusURL, nil)
	if err != nil {
		lb.markPeerUnhealthy(peer)
		return
	}

	startTime := time.Now()
	resp, err := lb.httpClient.Do(req)
	if err != nil {
		lb.markPeerUnhealthy(peer)
		return
	}
	defer resp.Body.Close()

	responseTime := time.Since(startTime).Milliseconds()

	if resp.StatusCode != http.StatusOK {
		lb.markPeerUnhealthy(peer)
		return
	}

	// Parse peer status
	var status PeerStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		lb.markPeerUnhealthy(peer)
		return
	}

	// Update peer info
	lb.peersMu.Lock()
	peer.Healthy = status.Healthy
	peer.CurrentLoad = status.CurrentLoad
	peer.MaxLoad = status.MaxLoad
	peer.ResponseTimeMs = responseTime
	peer.LastHealthCheck = time.Now()
	peer.ConsecutiveFails = 0
	lb.peersMu.Unlock()
}

// markPeerUnhealthy marks a peer as unhealthy
func (lb *NodeLoadBalancer) markPeerUnhealthy(peer *PeerNode) {
	lb.peersMu.Lock()
	defer lb.peersMu.Unlock()

	peer.ConsecutiveFails++
	if peer.ConsecutiveFails >= 3 {
		if peer.Healthy {
			log.Printf("[LB] Peer %s marked unhealthy after %d consecutive failures",
				peer.Instance.ID, peer.ConsecutiveFails)
		}
		peer.Healthy = false
	}
	peer.LastHealthCheck = time.Now()
}

// handleInternalEndpoint handles internal load balancer endpoints
func (lb *NodeLoadBalancer) handleInternalEndpoint(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/lb/status":
		lb.handleStatus(w, r)
		return true
	case "/lb/metrics":
		lb.handleMetrics(w, r)
		return true
	case "/lb/peers":
		lb.handlePeers(w, r)
		return true
	}
	return false
}

// handleStatus returns the current status of this node's load balancer
func (lb *NodeLoadBalancer) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := PeerStatus{
		NodeID:      lb.config.NodeID,
		CurrentLoad: lb.currentRequests.Load(),
		MaxLoad:     lb.config.MaxConcurrentRequests,
		QueueSize:   int(lb.queueSize.Load()),
		Healthy:     lb.running.Load(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleMetrics returns detailed metrics
func (lb *NodeLoadBalancer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	lb.metricsMu.RLock()
	metrics := lb.metrics
	lb.metricsMu.RUnlock()

	metrics.CurrentConcurrent = lb.currentRequests.Load()
	metrics.PeakConcurrent = lb.peakRequests.Load()
	metrics.LastUpdated = time.Now()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// handlePeers returns information about peer nodes
func (lb *NodeLoadBalancer) handlePeers(w http.ResponseWriter, r *http.Request) {
	lb.peersMu.RLock()
	peers := make([]*PeerNode, 0, len(lb.peers))
	for _, peer := range lb.peers {
		peers = append(peers, peer)
	}
	lb.peersMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(peers)
}

// collectMetrics periodically updates aggregate metrics
func (lb *NodeLoadBalancer) collectMetrics() {
	defer lb.wg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-lb.ctx.Done():
			return
		case <-ticker.C:
			lb.metricsMu.Lock()
			lb.metrics.CurrentConcurrent = lb.currentRequests.Load()
			lb.metrics.PeakConcurrent = lb.peakRequests.Load()
			lb.metrics.AvgResponseTimeMs = float64(lb.avgResponseTime.Load())
			lb.metrics.LastUpdated = time.Now()
			lb.metricsMu.Unlock()
		}
	}
}

// updateResponseTime updates the exponential moving average of response time
func (lb *NodeLoadBalancer) updateResponseTime(elapsed int64) {
	// EMA with alpha = 0.1
	for {
		current := lb.avgResponseTime.Load()
		if current == 0 {
			if lb.avgResponseTime.CompareAndSwap(0, elapsed) {
				break
			}
		} else {
			newAvg := int64(float64(current)*0.9 + float64(elapsed)*0.1)
			if lb.avgResponseTime.CompareAndSwap(current, newAvg) {
				break
			}
		}
	}
}

// GetMetrics returns current metrics
func (lb *NodeLoadBalancer) GetMetrics() LoadBalancerMetrics {
	lb.metricsMu.RLock()
	defer lb.metricsMu.RUnlock()

	metrics := lb.metrics
	metrics.CurrentConcurrent = lb.currentRequests.Load()
	metrics.PeakConcurrent = lb.peakRequests.Load()
	return metrics
}

// GetPeers returns current peer information
func (lb *NodeLoadBalancer) GetPeers() []*PeerNode {
	lb.peersMu.RLock()
	defer lb.peersMu.RUnlock()

	peers := make([]*PeerNode, 0, len(lb.peers))
	for _, peer := range lb.peers {
		peerCopy := *peer
		peers = append(peers, &peerCopy)
	}
	return peers
}

// GetHealthyPeers returns only healthy peers
func (lb *NodeLoadBalancer) GetHealthyPeers() []*PeerNode {
	lb.peersMu.RLock()
	defer lb.peersMu.RUnlock()

	var healthy []*PeerNode
	for _, peer := range lb.peers {
		if peer.Healthy {
			peerCopy := *peer
			healthy = append(healthy, &peerCopy)
		}
	}
	return healthy
}

// CurrentLoad returns the current number of concurrent requests
func (lb *NodeLoadBalancer) CurrentLoad() int64 {
	return lb.currentRequests.Load()
}

// IsAtCapacity returns true if the local node is at capacity
func (lb *NodeLoadBalancer) IsAtCapacity() bool {
	return lb.currentRequests.Load() >= int64(lb.config.MaxConcurrentRequests)
}

// ListenAndServe starts the load balancer HTTP server
func (lb *NodeLoadBalancer) ListenAndServe() error {
	if err := lb.Start(); err != nil {
		return err
	}

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", lb.config.ListenPort),
		Handler:      lb.Handler(nil),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("[LB] Starting HTTP server on port %d", lb.config.ListenPort)
	return server.ListenAndServe()
}

// WrapHandler wraps an existing handler with load balancing
func (lb *NodeLoadBalancer) WrapHandler(handler http.Handler) http.Handler {
	return lb.Handler(handler)
}
