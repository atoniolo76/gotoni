/*
Copyright © 2025 ALESSIO TONIOLO

load_balancer.go implements a distributed load balancer that runs on each node.
When a node reaches its max concurrent requests threshold, it forwards
requests to other healthy nodes in the cluster.

Metrics polling is handled in sglang_metrics.go
*/
package serve

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/atoniolo76/gotoni/pkg/remote"
)

// LoadBalancingStrategy defines the interface for load balancing algorithms
type LoadBalancingStrategy interface {
	Select(peers []*PeerNode, availabilityMap map[*PeerNode]AvailabilityStatus, requestPath string) *PeerNode
}

// LoadBalancerConfig configures the distributed load balancer
type LoadBalancerConfig struct {
	MaxConcurrentRequests int                   `json:"max_concurrent_requests"`
	ApplicationPort       int                   `json:"application_port"`
	LoadBalancerPort      int                   `json:"load_balancer_port"`
	RequestTimeout        time.Duration         `json:"request_timeout"`
	QueueEnabled          bool                  `json:"queue_enabled"`
	QueueTimeout          time.Duration         `json:"queue_timeout"`
	Strategy              LoadBalancingStrategy `json:"-"` // Not serializable

	// SGLang metrics polling (see sglang_metrics.go for polling logic)
	MetricsEnabled      bool          `json:"metrics_enabled"`
	MetricsPollInterval time.Duration `json:"metrics_poll_interval"`
	MetricsEndpoint     string        `json:"metrics_endpoint"`
	MetricsTimeout      time.Duration `json:"metrics_timeout"`
	UnhealthyThreshold  int           `json:"unhealthy_threshold"`
	GPUCacheThreshold   float64       `json:"gpu_cache_threshold"`
}

// DefaultLoadBalancerConfig returns sensible defaults
func DefaultLoadBalancerConfig() *LoadBalancerConfig {
	metricsConfig := DefaultMetricsConfig()
	return &LoadBalancerConfig{
		MaxConcurrentRequests: 10,
		ApplicationPort:       8080,
		LoadBalancerPort:      8000,
		RequestTimeout:        30 * time.Second,
		QueueEnabled:          true,
		QueueTimeout:          30 * time.Second,
		Strategy:              &LeastLoadedPolicy{},

		// SGLang metrics config
		MetricsEnabled:      metricsConfig.Enabled,
		MetricsPollInterval: metricsConfig.PollInterval,
		MetricsEndpoint:     metricsConfig.Endpoint,
		MetricsTimeout:      metricsConfig.Timeout,
		UnhealthyThreshold:  metricsConfig.UnhealthyThreshold,
		GPUCacheThreshold:   metricsConfig.GPUCacheThreshold,
	}
}

type LeastLoadedPolicy struct{}

type PrefixTreePolicy struct {
	mu   sync.RWMutex
	root *prefixNode
}

type prefixNode struct {
	children map[byte]*prefixNode
	peers    []*PeerNode
	prefix   string // variable length
}

type AvailabilityStatus struct {
	Available       bool
	RunningRequests int     // Actual requests in GPU batch (from SGLang)
	WaitingRequests int     // Actual queued requests (from SGLang)
	TotalRequests   int     // Running + Waiting
	GPUCacheUsage   float64 // KV cache utilization
	Healthy         bool    // Whether the peer is responding
}

type MatchedPrefixNodes struct {
	node      prefixNode
	matchRate float64
}

func NewPrefixTreePolicy() *PrefixTreePolicy {
	return &PrefixTreePolicy{
		root: &prefixNode{
			children: make(map[byte]*prefixNode),
			peers:    []*PeerNode{},
			prefix:   "",
		},
	}
}

func (p *PrefixTreePolicy) Select(peers []*PeerNode, availabilityMap map[*PeerNode]AvailabilityStatus, requestPath string) *PeerNode {
	if p.root == nil {
		// No existing prefix trie (first request), fall back to least-loaded
		return (&LeastLoadedPolicy{}).Select(peers, availabilityMap, requestPath)
	}

	if len(requestPath) == 0 {
		return nil
	}

	matches := []MatchedPrefixNodes{}
	currentNode := p.root

	i := 0
	for i < len(requestPath) {
		currentChar := requestPath[i]
		remainingText := requestPath[i:]
		lookup, ok := currentNode.children[currentChar]
		if !ok {
			break
		}

		currentNode = lookup
		sharedLen := sharedPrefixLength(remainingText, lookup.prefix)
		i += sharedLen

		// Check if this node has available replicas
		availablePeers := []*PeerNode{}
		for _, peerNode := range lookup.peers {
			if status, ok := availabilityMap[peerNode]; ok && status.Available && status.Healthy {
				availablePeers = append(availablePeers, peerNode)
			}
		}

		if len(availablePeers) > 0 {
			matchRate := float64(i) / float64(len(requestPath))
			node := prefixNode{
				prefix: lookup.prefix,
				peers:  availablePeers,
			}
			matches = append(matches, MatchedPrefixNodes{
				node:      node,
				matchRate: matchRate,
			})
		}

		if sharedLen < len(lookup.prefix) {
			// partial match, no need to search further
			break
		}
	}
	sort.Slice(matches, func(i2, j int) bool {
		return matches[i2].matchRate > matches[j].matchRate
	})

	if len(matches) > 0 {
		bestPeer := matches[0].node.peers[0]
		bestPeer.CurrentLoad++
		return bestPeer
	}

	return nil
}

func (p *PrefixTreePolicy) Insert(prefix string, peer *PeerNode) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(prefix) == 0 {
		return
	}

	currentNode := p.root

	i := 0

	// TODO note this for loop never hits its own exit condition bc of :262
	for i < len(prefix) {
		currentChar := prefix[i]
		remainingText := prefix[i:]
		lookup, ok := currentNode.children[currentChar]

		if !ok {
			// No child exists — create new node with entire remaining string
			newNode := &prefixNode{
				children: make(map[byte]*prefixNode),
				peers:    []*PeerNode{peer},
				prefix:   remainingText,
			}
			currentNode.children[currentChar] = newNode
			return
		}

		sharedLen := sharedPrefixLength(remainingText, lookup.prefix)

		if sharedLen < len(lookup.prefix) {
			matchingPart := lookup.prefix[:sharedLen]
			oldRemainingPart := lookup.prefix[sharedLen:]

			newNode := &prefixNode{
				children: make(map[byte]*prefixNode),
				peers:    []*PeerNode{peer},
				prefix:   matchingPart,
			}

			lookup.prefix = oldRemainingPart
			lookup.children[oldRemainingPart[0]] = lookup
			currentNode.children[currentChar] = newNode

			i += sharedLen

			if len(remainingText[i:]) > 0 {
				finalNode := &prefixNode{
					children: make(map[byte]*prefixNode),
					peers:    []*PeerNode{peer},
					prefix:   remainingText[i:],
				}
				newNode.children[finalNode.prefix[0]] = finalNode
			}
			return
		}

		i += sharedLen
		if i >= len(prefix) {
			lookup.peers = append(lookup.peers, peer)
			return
		}
		currentNode = lookup
	}
}

func sharedPrefixLength(a, b string) int {
	minLength := len(a)
	if len(b) < minLength {
		minLength = len(b)
	}
	for i := 0; i < minLength; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return minLength
}

func (p *LeastLoadedPolicy) Select(peers []*PeerNode, availabilityMap map[*PeerNode]AvailabilityStatus, requestPath string) *PeerNode {
	if len(peers) == 0 {
		return nil
	}

	var best *PeerNode
	lowestLoad := -1

	for _, peer := range peers {
		status, exists := availabilityMap[peer]

		// Skip unavailable/unhealthy peers
		if !exists || !status.Available || !status.Healthy {
			continue
		}

		// Use real SGLang metrics: total requests = running + waiting
		totalLoad := status.TotalRequests

		// Skip peers at capacity (use MaxRunningReqs from SGLang if available)
		maxLoad := peer.MaxLoad
		if peer.Metrics.MaxRunningReqs > 0 {
			maxLoad = peer.Metrics.MaxRunningReqs
		}
		if totalLoad >= maxLoad {
			continue
		}

		if lowestLoad == -1 || totalLoad < lowestLoad {
			lowestLoad = totalLoad
			best = peer
		}
	}

	return best
}

// PeerNode represents a peer node in the cluster
type PeerNode struct {
	Instance    *remote.RunningInstance `json:"instance"`
	Port        int                     `json:"port"`
	CurrentLoad int64                   `json:"current_load"` // Deprecated: use Metrics instead
	MaxLoad     int                     `json:"max_load"`

	// Real-time metrics from SGLang
	Metrics SGLangMetrics `json:"metrics"`
}

// queuedRequest represents a request waiting in the queue
type queuedRequest struct {
	w          http.ResponseWriter
	r          *http.Request
	done       chan struct{}
	err        error
	enqueuedAt time.Time
}

// LoadBalancer is a distributed load balancer instance for a single node
type LoadBalancer struct {
	config *LoadBalancerConfig

	// Self reference for peer exclusion
	self *remote.RunningInstance

	// Peer management - maps peer nodes to their availability status
	peers   map[*PeerNode]AvailabilityStatus
	peersMu sync.RWMutex

	// Load balancing strategy (used when forwarding to peers)
	strategy LoadBalancingStrategy

	// Local SGLang metrics (polled from local backend)
	localMetrics   SGLangMetrics
	localMetricsMu sync.RWMutex

	// Request queue (unbounded slice-based queue)
	queue   []*queuedRequest
	queueMu sync.Mutex

	// HTTP clients
	httpClient *http.Client
	localProxy *httputil.ReverseProxy

	// Control
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running atomic.Bool
}

// NewLoadBalancer creates a new load balancer for this node
func NewLoadBalancer(config *LoadBalancerConfig, self *remote.RunningInstance) *LoadBalancer {
	if config == nil {
		config = DefaultLoadBalancerConfig()
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Create local backend proxy
	localURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", config.ApplicationPort))
	localProxy := httputil.NewSingleHostReverseProxy(localURL)

	// Customize proxy error handler
	localProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[LB] Local proxy error: %v", err)
		http.Error(w, "Backend unavailable", http.StatusBadGateway)
	}

	// Use default strategy if none provided
	if config.Strategy == nil {
		config.Strategy = &LeastLoadedPolicy{}
	}

	lb := &LoadBalancer{
		config:   config,
		self:     self,
		peers:    make(map[*PeerNode]AvailabilityStatus),
		strategy: config.Strategy,
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
		lb.queue = make([]*queuedRequest, 0)
	}

	return lb
}

// AddPeer adds a peer node to the load balancer
func (lb *LoadBalancer) AddPeer(instance *remote.RunningInstance, port int) {
	lb.peersMu.Lock()
	defer lb.peersMu.Unlock()

	peerNode := &PeerNode{
		Instance: instance,
		Port:     port,
		MaxLoad:  lb.config.MaxConcurrentRequests,
		Metrics: SGLangMetrics{
			Healthy:     true, // Assume healthy initially until first poll
			LastUpdated: time.Now(),
		},
	}

	lb.peers[peerNode] = AvailabilityStatus{
		Available: true, // Assume available initially until first metrics poll
		Healthy:   true,
	}

	log.Printf("[LB] Added peer: %s (%s:%d)", instance.ID, instance.IP, port)
}

// RemovePeer removes a peer node from the load balancer
func (lb *LoadBalancer) RemovePeer(id string) {
	lb.peersMu.Lock()
	defer lb.peersMu.Unlock()

	// Find and remove the peer with matching ID
	for peerNode := range lb.peers {
		if peerNode.Instance.ID == id {
			delete(lb.peers, peerNode)
			log.Printf("[LB] Removed peer: %s", id)
			break
		}
	}
}

// AddPeersFromCluster adds all instances from a cluster as peers
func (lb *LoadBalancer) AddPeersFromCluster(cluster *Cluster) {
	for i := range cluster.Instances {
		inst := &cluster.Instances[i]
		// Skip self by comparing pointers
		if inst == lb.self {
			continue
		}
		lb.AddPeer(inst, lb.config.LoadBalancerPort)
	}
}

// Start starts the load balancer
func (lb *LoadBalancer) Start() error {
	if lb.running.Load() {
		return fmt.Errorf("load balancer already running")
	}

	lb.running.Store(true)

	// Start queue processor if enabled
	if lb.config.QueueEnabled {
		lb.wg.Add(1)
		go lb.processQueue()
	}

	// Start metrics poller if enabled
	if lb.config.MetricsEnabled {
		lb.wg.Add(1)
		go lb.pollMetricsLoop()
	}

	log.Printf("[LB] Load balancer started (max concurrent: %d, metrics polling: %v)",
		lb.config.MaxConcurrentRequests, lb.config.MetricsEnabled)

	return nil
}

// Stop stops the load balancer
func (lb *LoadBalancer) Stop() {
	if !lb.running.Load() {
		return
	}

	lb.running.Store(false)
	lb.cancel()
	lb.wg.Wait()

	log.Printf("[LB] Load balancer stopped")
}

// Handler returns an HTTP handler that implements the load balancing logic
func (lb *LoadBalancer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if LOCAL SGLang has capacity using real metrics
		if lb.localHasCapacity() {
			// Process locally - user is routed here, KV cache is warm
			lb.localProxy.ServeHTTP(w, r)
			return
		}

		// Local is at capacity - try to forward to a peer
		lb.localMetricsMu.RLock()
		localLoad := lb.localMetrics.NumTotalReqs
		lb.localMetricsMu.RUnlock()
		log.Printf("[LB] Local at capacity (running=%d, waiting=%d, total=%d), forwarding",
			lb.localMetrics.NumRunningReqs, lb.localMetrics.NumWaitingReqs, localLoad)

		if lb.forwardToPeer(w, r) {
			return
		}

		// No healthy peers available - try queue locally or reject
		if lb.config.QueueEnabled {
			lb.enqueueRequest(w, r)
			return
		}

		// All options exhausted
		http.Error(w, "Service at capacity", http.StatusServiceUnavailable)
	})
}

// localHasCapacity checks if the local SGLang backend has capacity
// Returns true if we should process locally (queue depth < max)
func (lb *LoadBalancer) localHasCapacity() bool {
	lb.localMetricsMu.RLock()
	defer lb.localMetricsMu.RUnlock()

	// If we haven't polled yet, assume we have capacity
	if lb.localMetrics.LastUpdated.IsZero() {
		return true
	}

	// Check if local SGLang queue is below threshold
	// "Capacity" means: total requests (running + waiting) < max running
	maxLoad := lb.config.MaxConcurrentRequests
	if lb.localMetrics.MaxRunningReqs > 0 {
		maxLoad = lb.localMetrics.MaxRunningReqs
	}

	hasCapacity := lb.localMetrics.NumTotalReqs < maxLoad &&
		lb.localMetrics.GPUCacheUsage < lb.config.GPUCacheThreshold &&
		lb.localMetrics.Healthy

	return hasCapacity
}

func (lb *LoadBalancer) forwardToPeer(w http.ResponseWriter, r *http.Request) bool {
	peer := lb.selectPeer(r.URL.Path)
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
	forwardReq.Header.Set("X-LB-Hop", "1")

	// Pre-execute, add the requestPath to the prefix tree if doing PrefixTreePolicy
	// func (p *PrefixTreePolicy) Insert(prefix string, peer *PeerNode) {
	if lb.strategy.(*PrefixTreePolicy) != nil {
		lb.strategy.(*PrefixTreePolicy).Insert(r.URL.Path, peer)
	}

	// Execute forward
	resp, err := lb.httpClient.Do(forwardReq)
	if err != nil {
		log.Printf("[LB] Forward to %s failed: %v", peer.Instance.ID, err)
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

	log.Printf("[LB] Forwarded request to %s", peer.Instance.ID)
	return true
}

// selectPeer selects a peer using the configured strategy
func (lb *LoadBalancer) selectPeer(requestPath string) *PeerNode {
	lb.peersMu.RLock()
	defer lb.peersMu.RUnlock()

	// Extract peer slice from the map keys
	peers := make([]*PeerNode, 0, len(lb.peers))
	for peer := range lb.peers {
		peers = append(peers, peer)
	}

	return lb.strategy.Select(peers, lb.peers, requestPath)
}

// enqueueRequest adds a request to the queue
func (lb *LoadBalancer) enqueueRequest(w http.ResponseWriter, r *http.Request) {
	qr := &queuedRequest{
		w:          w,
		r:          r,
		done:       make(chan struct{}),
		enqueuedAt: time.Now(),
	}

	// Add to unbounded queue
	lb.queueMu.Lock()
	lb.queue = append(lb.queue, qr)
	lb.queueMu.Unlock()

	log.Printf("[LB] Request queued (queue size: %d)", len(lb.queue))

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
}

// processQueue processes queued requests when local SGLang has capacity
func (lb *LoadBalancer) processQueue() {
	defer lb.wg.Done()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-lb.ctx.Done():
			return
		case <-ticker.C:
			// Check if local SGLang has capacity using real metrics
			if !lb.localHasCapacity() {
				continue
			}

			// Try to dequeue
			lb.queueMu.Lock()
			if len(lb.queue) > 0 {
				qr := lb.queue[0]
				lb.queue = lb.queue[1:] // Remove first element
				lb.queueMu.Unlock()

				// Check if request is still valid
				if time.Since(qr.enqueuedAt) > lb.config.QueueTimeout {
					qr.err = fmt.Errorf("queue timeout")
					close(qr.done)
					continue
				}

				// Process the request locally
				lb.localProxy.ServeHTTP(qr.w, qr.r)
				close(qr.done)
			} else {
				lb.queueMu.Unlock()
				// No requests in queue
			}
		}
	}
}

// CurrentLoad returns the current load from real SGLang metrics (running + waiting)
func (lb *LoadBalancer) CurrentLoad() int {
	lb.localMetricsMu.RLock()
	defer lb.localMetricsMu.RUnlock()
	return lb.localMetrics.NumTotalReqs
}

// IsAtCapacity returns true if the local SGLang is at capacity
func (lb *LoadBalancer) IsAtCapacity() bool {
	return !lb.localHasCapacity()
}

// ListenAndServe starts the load balancer HTTP server
func (lb *LoadBalancer) ListenAndServe() error {
	if err := lb.Start(); err != nil {
		return err
	}

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", lb.config.LoadBalancerPort),
		Handler:      lb.Handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("[LB] Starting HTTP server on port %d", lb.config.LoadBalancerPort)
	return server.ListenAndServe()
}
