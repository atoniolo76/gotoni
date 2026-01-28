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
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
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

	// Observability - Loki/Grafana integration
	ObservabilityEnabled bool   `json:"observability_enabled"`
	LokiEndpoint         string `json:"loki_endpoint"`
	NodeID               string `json:"node_id"`
	ClusterName          string `json:"cluster_name"`
}

// DefaultLoadBalancerConfig returns sensible defaults
func DefaultLoadBalancerConfig() *LoadBalancerConfig {
	metricsConfig := DefaultMetricsConfig()
	obsConfig := DefaultObservabilityConfig()
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

		// Observability config
		ObservabilityEnabled: obsConfig.Enabled,
		LokiEndpoint:         obsConfig.LokiEndpoint,
		NodeID:               obsConfig.NodeID,
		ClusterName:          obsConfig.ClusterName,
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

type GORGOPolicy struct {
	mu   sync.RWMutex
	root *GORGONode
}

type GORGONode struct {
	children map[byte]*GORGONode
	peers    []*PeerNode
	prefix   string // variable length
}

const GORGO_MS_PER_TOKEN = 0.094 // Cold-start prefill rate on 8xA100 with Mistral-7B

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

func (p *GORGOPolicy) Select(peers []*PeerNode, availabilityMap map[*PeerNode]AvailabilityStatus, requestPath string) *PeerNode {
	return p.SelectWithTrace(peers, availabilityMap, requestPath, nil, nil)
}

// SelectWithTrace selects a peer and records trace data for waterfall diagrams
func (p *GORGOPolicy) SelectWithTrace(peers []*PeerNode, availabilityMap map[*PeerNode]AvailabilityStatus, requestPath string, tc *TraceContext, obs *LBObservability) *PeerNode {
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

	// Core logic for GORGO: best match may not always be the best choice considering geographic proximity
	// Cost = network latency + prefill time for unmatched tokens
	lowestCost := int(^uint(0) >> 1)
	var bestMatchedPeer *PeerNode = nil
	var bestMatchRate float64 = 0.0

	for _, matchedPrefixNode := range matches {
		// Start tokenizer span for timing measurement
		var tokenizerSpanID string
		if tc != nil {
			tokenizerSpanID = tc.StartSpan(EventTokenizerStart, map[string]interface{}{
				"text_length": len(requestPath) - len(matchedPrefixNode.node.prefix),
			})
		}

		// Calculate unmatched portion that needs fresh prefill
		unmatchedText := requestPath[len(matchedPrefixNode.node.prefix):]
		unmatchedTokens := GetTokenCount(unmatchedText)

		// End tokenizer span
		if tc != nil {
			tc.EndSpan(tokenizerSpanID, EventTokenizerEnd, map[string]interface{}{
				"token_count": unmatchedTokens,
			})
		}

		for _, peer := range matchedPrefixNode.node.peers {
			// Total cost = network latency (ms) + prefill cost (tokens × ms/token)
			prefillCost := float64(unmatchedTokens) * GORGO_MS_PER_TOKEN
			totalCost := int(peer.Metrics.Latency) + int(prefillCost)

			// Record GORGO cost calculation for observability
			if obs != nil {
				obs.RecordGORGODecision(tc, peer.Instance.ID,
					peer.Metrics.Latency,
					unmatchedTokens,
					prefillCost,
					float64(totalCost),
					matchedPrefixNode.matchRate,
				)
			}

			if totalCost < lowestCost {
				lowestCost = totalCost
				bestMatchedPeer = peer
				bestMatchRate = matchedPrefixNode.matchRate
			}
		}
	}

	// Record final selection
	if tc != nil && bestMatchedPeer != nil {
		tc.AddEvent(EventGORGOCostCalc, map[string]interface{}{
			"final_selection":    bestMatchedPeer.Instance.ID,
			"final_cost_ms":      lowestCost,
			"final_match_rate":   bestMatchRate,
			"candidates_checked": len(matches),
		})
	}

	return bestMatchedPeer
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

	// Observability - Loki/Grafana integration
	observability *LBObservability

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

	// Initialize observability
	nodeID := config.NodeID
	if nodeID == "" && self != nil {
		nodeID = self.ID[:16] // Use truncated instance ID
	}
	obsConfig := LBObservabilityConfig{
		LokiEndpoint:  config.LokiEndpoint,
		NodeID:        nodeID,
		ClusterName:   config.ClusterName,
		PushInterval:  5 * time.Second,
		MaxBufferSize: 100,
		Enabled:       config.ObservabilityEnabled,
	}
	observability := NewLBObservability(obsConfig)

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
		localProxy:    localProxy,
		observability: observability,
		ctx:           ctx,
		cancel:        cancel,
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

// AddPeerByIP adds a peer by IP address and port (for standalone/CLI usage)
func (lb *LoadBalancer) AddPeerByIP(ip string, port int) {
	lb.peersMu.Lock()
	defer lb.peersMu.Unlock()

	// Check if already exists
	for peerNode := range lb.peers {
		if peerNode.Instance != nil && peerNode.Instance.IP == ip && peerNode.Port == port {
			log.Printf("[LB] Peer %s:%d already registered", ip, port)
			return
		}
	}

	// Create minimal instance for the peer
	peerNode := &PeerNode{
		Instance: &remote.RunningInstance{IP: ip},
		Port:     port,
		MaxLoad:  lb.config.MaxConcurrentRequests,
	}

	lb.peers[peerNode] = AvailabilityStatus{
		Available: true,
		Healthy:   true,
	}
	log.Printf("[LB] Added peer: %s:%d", ip, port)
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

	// Start observability pusher if enabled
	if lb.observability != nil {
		lb.observability.Start()
	}

	log.Printf("[LB] Load balancer started (max concurrent: %d, metrics polling: %v, observability: %v)",
		lb.config.MaxConcurrentRequests, lb.config.MetricsEnabled, lb.config.ObservabilityEnabled)

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

	// Stop observability pusher
	if lb.observability != nil {
		lb.observability.Stop()
	}

	log.Printf("[LB] Load balancer stopped")
}

// Handler returns an HTTP handler that implements the load balancing logic
func (lb *LoadBalancer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handle load balancer status endpoint
		if r.URL.Path == "/lb/status" {
			lb.handleStatusEndpoint(w, r)
			return
		}

		// Handle load balancer health endpoint
		if r.URL.Path == "/lb/health" {
			lb.handleHealthEndpoint(w, r)
			return
		}

		// Handle load balancer metrics endpoint
		if r.URL.Path == "/lb/metrics" {
			lb.handleMetricsEndpoint(w, r)
			return
		}

		// Handle load balancer config endpoint (GET/POST for runtime config changes)
		if r.URL.Path == "/lb/config" {
			lb.handleConfigEndpoint(w, r)
			return
		}

		// Handle policy switching endpoint (convenience shortcut)
		if r.URL.Path == "/lb/policy" {
			lb.handlePolicyEndpoint(w, r)
			return
		}

		// Handle tokenizer test endpoint (verify CGO tokenizer is working)
		if r.URL.Path == "/lb/tokenizer" {
			lb.handleTokenizerEndpoint(w, r)
			return
		}

		// Handle observability config endpoint
		if r.URL.Path == "/lb/observability" {
			lb.handleObservabilityEndpoint(w, r)
			return
		}

		// Handle logs endpoint (recent request traces)
		if r.URL.Path == "/lb/logs" {
			lb.handleLogsEndpoint(w, r)
			return
		}

		// Handle peers endpoint (GET/POST to manage peer nodes)
		if r.URL.Path == "/lb/peers" {
			lb.handlePeersEndpoint(w, r)
			return
		}

		// Create trace context for this request (for waterfall diagrams)
		tc := NewTraceContext()
		requestStart := time.Now()

		// Record request received
		tc.AddEvent(EventRequestReceived, map[string]interface{}{
			"path":   r.URL.Path,
			"method": r.Method,
			"remote": r.RemoteAddr,
		})

		// Wrap response writer to capture status code
		wrappedWriter := &responseWriterWrapper{ResponseWriter: w, statusCode: 200}

		// Check if LOCAL SGLang has capacity using real metrics
		if lb.localHasCapacity() {
			// Process locally - user is routed here, KV cache is warm
			log.Printf("[LB] Request received: %s %s from %s -> processing locally", r.Method, r.URL.Path, r.RemoteAddr)
			spanID := tc.StartSpan(EventLocalProxyStart, map[string]interface{}{"decision": "local_capacity"})
			proxyStart := time.Now()
			lb.localProxy.ServeHTTP(wrappedWriter, r)
			proxyDuration := time.Since(proxyStart)
			tc.EndSpan(spanID, EventLocalProxyEnd, map[string]interface{}{
				"status_code": wrappedWriter.statusCode,
			})
			log.Printf("[LB] Request completed: %s %s -> local (status=%d, duration=%s)",
				r.Method, r.URL.Path, wrappedWriter.statusCode, proxyDuration)

			// Record request completion
			tc.AddEvent(EventRequestCompleted, map[string]interface{}{
				"total_duration_ms": float64(time.Since(requestStart).Microseconds()) / 1000.0,
				"status_code":       wrappedWriter.statusCode,
				"served_by":         "local",
			})

			// Send trace to observability
			lb.observability.RecordTrace(tc)
			return
		}

		// Local is at capacity - try to forward to a peer
		lb.localMetricsMu.RLock()
		localLoad := lb.localMetrics.NumTotalReqs
		lb.localMetricsMu.RUnlock()
		log.Printf("[LB] Local at capacity (running=%d, waiting=%d, total=%d), forwarding",
			lb.localMetrics.NumRunningReqs, lb.localMetrics.NumWaitingReqs, localLoad)

		if lb.forwardToPeerWithTrace(wrappedWriter, r, tc) {
			// Record request completion
			tc.AddEvent(EventRequestCompleted, map[string]interface{}{
				"total_duration_ms": float64(time.Since(requestStart).Microseconds()) / 1000.0,
				"status_code":       wrappedWriter.statusCode,
				"served_by":         "peer",
			})
			lb.observability.RecordTrace(tc)
			return
		}

		// No healthy peers available - try queue locally or reject
		if lb.config.QueueEnabled {
			log.Printf("[LB] Request received: %s %s from %s -> queueing (no capacity, no healthy peers)", r.Method, r.URL.Path, r.RemoteAddr)
			tc.AddEvent(EventRequestQueued, map[string]interface{}{
				"reason": "no_capacity_no_peers",
			})
			lb.enqueueRequest(wrappedWriter, r)

			tc.AddEvent(EventRequestCompleted, map[string]interface{}{
				"total_duration_ms": float64(time.Since(requestStart).Microseconds()) / 1000.0,
				"served_by":         "queue",
			})
			lb.observability.RecordTrace(tc)
			return
		}

		// All options exhausted
		tc.AddEvent(EventRequestCompleted, map[string]interface{}{
			"total_duration_ms": float64(time.Since(requestStart).Microseconds()) / 1000.0,
			"status_code":       503,
			"served_by":         "rejected",
		})
		lb.observability.RecordTrace(tc)
		http.Error(w, "Service at capacity", http.StatusServiceUnavailable)
	})
}

// responseWriterWrapper wraps http.ResponseWriter to capture status code
type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
}

func (w *responseWriterWrapper) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
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
	return lb.forwardToPeerWithTrace(w, r, nil)
}

func (lb *LoadBalancer) forwardToPeerWithTrace(w http.ResponseWriter, r *http.Request, tc *TraceContext) bool {
	// Start policy selection span
	var policySpanID string
	if tc != nil {
		policySpanID = tc.StartSpan(EventPolicySelect, map[string]interface{}{
			"policy": lb.getStrategyName(),
			"path":   r.URL.Path,
		})
	}

	peer := lb.selectPeerWithTrace(r.URL.Path, tc)

	if peer != nil {
		log.Printf("[LB] Request received: %s %s from %s -> forwarding to peer %s (%s:%d)",
			r.Method, r.URL.Path, r.RemoteAddr, peer.Instance.ID, peer.Instance.IP, peer.Port)
	}

	// End policy selection span
	if tc != nil {
		tc.EndSpan(policySpanID, EventPolicySelect, map[string]interface{}{
			"peer_selected": peer != nil,
			"peer_id": func() string {
				if peer != nil {
					return peer.Instance.ID
				} else {
					return ""
				}
			}(),
		})
	}

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
	if tc != nil {
		forwardReq.Header.Set("X-Trace-ID", tc.TraceID)
	}

	// Pre-execute, add the requestPath to the prefix tree if doing PrefixTreePolicy
	if prefixPolicy, ok := lb.strategy.(*PrefixTreePolicy); ok && prefixPolicy != nil {
		prefixPolicy.Insert(r.URL.Path, peer)
	}

	// Record forward event
	if tc != nil {
		lb.observability.RecordForward(tc, peer.Instance.ID, peer.Instance.IP, peer.Port, lb.getStrategyName(), map[string]interface{}{
			"target_url":     targetURL,
			"body_size":      len(bodyBytes),
			"peer_load":      peer.Metrics.NumTotalReqs,
			"peer_gpu_cache": peer.Metrics.GPUCacheUsage,
		})
	}

	// Start HTTP forward span
	var forwardSpanID string
	if tc != nil {
		forwardSpanID = tc.StartSpan(EventHTTPForwardStart, map[string]interface{}{
			"target_peer": peer.Instance.ID,
			"target_ip":   peer.Instance.IP,
			"target_port": peer.Port,
		})
	}

	// Execute forward
	resp, err := lb.httpClient.Do(forwardReq)

	// End HTTP forward span
	if tc != nil {
		metadata := map[string]interface{}{
			"target_peer": peer.Instance.ID,
		}
		if err != nil {
			metadata["error"] = err.Error()
		} else {
			metadata["status_code"] = resp.StatusCode
		}
		tc.EndSpan(forwardSpanID, EventHTTPForwardEnd, metadata)
	}

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

	log.Printf("[LB] Request completed: forwarded to peer %s (%s:%d) -> status=%d",
		peer.Instance.ID, peer.Instance.IP, peer.Port, resp.StatusCode)
	return true
}

// selectPeer selects a peer using the configured strategy
func (lb *LoadBalancer) selectPeer(requestPath string) *PeerNode {
	return lb.selectPeerWithTrace(requestPath, nil)
}

// selectPeerWithTrace selects a peer and records trace data for GORGO policy
func (lb *LoadBalancer) selectPeerWithTrace(requestPath string, tc *TraceContext) *PeerNode {
	lb.peersMu.RLock()
	defer lb.peersMu.RUnlock()

	// Extract peer slice from the map keys
	peers := make([]*PeerNode, 0, len(lb.peers))
	for peer := range lb.peers {
		peers = append(peers, peer)
	}

	// For GORGO policy, we need to pass observability context
	if gorgoPolicy, ok := lb.strategy.(*GORGOPolicy); ok {
		return gorgoPolicy.SelectWithTrace(peers, lb.peers, requestPath, tc, lb.observability)
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

// handleStatusEndpoint returns the load balancer status as JSON
func (lb *LoadBalancer) handleStatusEndpoint(w http.ResponseWriter, r *http.Request) {
	lb.localMetricsMu.RLock()
	localMetrics := lb.localMetrics
	lb.localMetricsMu.RUnlock()

	lb.peersMu.RLock()
	peerCount := len(lb.peers)
	healthyPeers := 0
	for _, status := range lb.peers {
		if status.Healthy && status.Available {
			healthyPeers++
		}
	}
	lb.peersMu.RUnlock()

	// Get queue size
	lb.queueMu.Lock()
	queueSize := len(lb.queue)
	lb.queueMu.Unlock()

	status := map[string]interface{}{
		"running":         lb.running.Load(),
		"current_load":    localMetrics.NumTotalReqs,
		"max_load":        lb.config.MaxConcurrentRequests,
		"running_reqs":    localMetrics.NumRunningReqs,
		"waiting_reqs":    localMetrics.NumWaitingReqs,
		"gpu_cache":       localMetrics.GPUCacheUsage,
		"healthy":         localMetrics.Healthy,
		"peer_count":      peerCount,
		"healthy_peers":   healthyPeers,
		"queue_size":      queueSize,
		"queue_enabled":   lb.config.QueueEnabled,
		"listen_port":     lb.config.LoadBalancerPort,
		"backend_port":    lb.config.ApplicationPort,
		"metrics_enabled": lb.config.MetricsEnabled,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleHealthEndpoint returns a simple health check
func (lb *LoadBalancer) handleHealthEndpoint(w http.ResponseWriter, r *http.Request) {
	if !lb.running.Load() {
		http.Error(w, "Load balancer not running", http.StatusServiceUnavailable)
		return
	}

	lb.localMetricsMu.RLock()
	healthy := lb.localMetrics.Healthy
	lb.localMetricsMu.RUnlock()

	if !healthy {
		http.Error(w, "Backend unhealthy", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "healthy",
		"running": true,
	})
}

// handleMetricsEndpoint returns detailed metrics for all peers
func (lb *LoadBalancer) handleMetricsEndpoint(w http.ResponseWriter, r *http.Request) {
	lb.localMetricsMu.RLock()
	localMetrics := lb.localMetrics
	lb.localMetricsMu.RUnlock()

	lb.peersMu.RLock()
	peers := make(map[string]interface{})
	for peer, status := range lb.peers {
		peers[peer.Instance.ID] = map[string]interface{}{
			"ip":           peer.Instance.IP,
			"port":         peer.Port,
			"available":    status.Available,
			"healthy":      status.Healthy,
			"running_reqs": status.RunningRequests,
			"waiting_reqs": status.WaitingRequests,
			"total_reqs":   status.TotalRequests,
			"gpu_cache":    status.GPUCacheUsage,
			"max_running":  peer.Metrics.MaxRunningReqs,
			"latency_ms":   peer.Metrics.Latency,
		}
	}
	lb.peersMu.RUnlock()

	metrics := map[string]interface{}{
		"local": map[string]interface{}{
			"running_reqs": localMetrics.NumRunningReqs,
			"waiting_reqs": localMetrics.NumWaitingReqs,
			"total_reqs":   localMetrics.NumTotalReqs,
			"gpu_cache":    localMetrics.GPUCacheUsage,
			"max_running":  localMetrics.MaxRunningReqs,
			"healthy":      localMetrics.Healthy,
			"last_updated": localMetrics.LastUpdated,
		},
		"peers": peers,
		"config": map[string]interface{}{
			"max_concurrent":  lb.config.MaxConcurrentRequests,
			"queue_enabled":   lb.config.QueueEnabled,
			"queue_timeout":   lb.config.QueueTimeout.String(),
			"request_timeout": lb.config.RequestTimeout.String(),
			"gpu_threshold":   lb.config.GPUCacheThreshold,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// getStrategyName returns the name of the current strategy
func (lb *LoadBalancer) getStrategyName() string {
	switch lb.strategy.(type) {
	case *LeastLoadedPolicy:
		return "least-loaded"
	case *PrefixTreePolicy:
		return "prefix-tree"
	case *GORGOPolicy:
		return "gorgo"
	default:
		return "unknown"
	}
}

// handleConfigEndpoint handles runtime configuration changes
// GET: returns current config
// POST/PUT: updates config (supports {"policy": "gorgo|least-loaded|prefix-tree"})
func (lb *LoadBalancer) handleConfigEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet {
		// Return current config
		config := map[string]interface{}{
			"policy":              lb.getStrategyName(),
			"max_concurrent":      lb.config.MaxConcurrentRequests,
			"queue_enabled":       lb.config.QueueEnabled,
			"queue_timeout":       lb.config.QueueTimeout.String(),
			"request_timeout":     lb.config.RequestTimeout.String(),
			"metrics_enabled":     lb.config.MetricsEnabled,
			"gpu_cache_threshold": lb.config.GPUCacheThreshold,
			"listen_port":         lb.config.LoadBalancerPort,
			"backend_port":        lb.config.ApplicationPort,
		}
		json.NewEncoder(w).Encode(config)
		return
	}

	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		// Parse request body
		var req struct {
			Policy            string   `json:"policy"`
			MaxConcurrent     *int     `json:"max_concurrent"`
			QueueEnabled      *bool    `json:"queue_enabled"`
			GPUCacheThreshold *float64 `json:"gpu_cache_threshold"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "invalid JSON: %v"}`, err), http.StatusBadRequest)
			return
		}

		changes := []string{}

		// Update policy if specified
		if req.Policy != "" {
			oldPolicy := lb.getStrategyName()
			if err := lb.SetStrategy(req.Policy); err != nil {
				http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusBadRequest)
				return
			}
			changes = append(changes, fmt.Sprintf("policy: %s -> %s", oldPolicy, req.Policy))
			log.Printf("[LB] Policy changed: %s -> %s", oldPolicy, req.Policy)
		}

		// Update max concurrent if specified
		if req.MaxConcurrent != nil {
			old := lb.config.MaxConcurrentRequests
			lb.config.MaxConcurrentRequests = *req.MaxConcurrent
			changes = append(changes, fmt.Sprintf("max_concurrent: %d -> %d", old, *req.MaxConcurrent))
		}

		// Update queue enabled if specified
		if req.QueueEnabled != nil {
			old := lb.config.QueueEnabled
			lb.config.QueueEnabled = *req.QueueEnabled
			changes = append(changes, fmt.Sprintf("queue_enabled: %v -> %v", old, *req.QueueEnabled))
		}

		// Update GPU cache threshold if specified
		if req.GPUCacheThreshold != nil {
			old := lb.config.GPUCacheThreshold
			lb.config.GPUCacheThreshold = *req.GPUCacheThreshold
			changes = append(changes, fmt.Sprintf("gpu_cache_threshold: %.2f -> %.2f", old, *req.GPUCacheThreshold))
		}

		response := map[string]interface{}{
			"success": true,
			"changes": changes,
			"config": map[string]interface{}{
				"policy":              lb.getStrategyName(),
				"max_concurrent":      lb.config.MaxConcurrentRequests,
				"queue_enabled":       lb.config.QueueEnabled,
				"gpu_cache_threshold": lb.config.GPUCacheThreshold,
			},
		}
		json.NewEncoder(w).Encode(response)
		return
	}

	http.Error(w, `{"error": "method not allowed, use GET or POST"}`, http.StatusMethodNotAllowed)
}

// handlePolicyEndpoint is a convenience endpoint for switching policies
// GET: returns available policies and current policy
// POST: switches policy (body: {"policy": "gorgo"} or just the policy name as plain text)
func (lb *LoadBalancer) handlePolicyEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	availablePolicies := []string{"least-loaded", "prefix-tree", "gorgo"}

	if r.Method == http.MethodGet {
		// Also check query parameter for quick switching: /lb/policy?set=gorgo
		if newPolicy := r.URL.Query().Get("set"); newPolicy != "" {
			oldPolicy := lb.getStrategyName()
			if err := lb.SetStrategy(newPolicy); err != nil {
				http.Error(w, fmt.Sprintf(`{"error": "%v", "available": %v}`, err, availablePolicies), http.StatusBadRequest)
				return
			}
			log.Printf("[LB] Policy changed via query param: %s -> %s", oldPolicy, newPolicy)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success":    true,
				"old_policy": oldPolicy,
				"new_policy": newPolicy,
				"available":  availablePolicies,
			})
			return
		}

		// Return current policy info
		json.NewEncoder(w).Encode(map[string]interface{}{
			"current":   lb.getStrategyName(),
			"available": availablePolicies,
			"hint":      "POST with {\"policy\": \"gorgo\"} or GET /lb/policy?set=gorgo to switch",
		})
		return
	}

	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		// Try to parse as JSON first
		var req struct {
			Policy string `json:"policy"`
		}

		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			// Try plain text (just the policy name)
			req.Policy = strings.TrimSpace(string(body))
		}

		if req.Policy == "" {
			http.Error(w, fmt.Sprintf(`{"error": "policy required", "available": %v}`, availablePolicies), http.StatusBadRequest)
			return
		}

		oldPolicy := lb.getStrategyName()
		if err := lb.SetStrategy(req.Policy); err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "%v", "available": %v}`, err, availablePolicies), http.StatusBadRequest)
			return
		}

		log.Printf("[LB] Policy changed: %s -> %s", oldPolicy, req.Policy)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":    true,
			"old_policy": oldPolicy,
			"new_policy": req.Policy,
		})
		return
	}

	http.Error(w, `{"error": "method not allowed, use GET or POST"}`, http.StatusMethodNotAllowed)
}

// SetStrategy changes the load balancing strategy at runtime
func (lb *LoadBalancer) SetStrategy(name string) error {
	var newStrategy LoadBalancingStrategy

	switch strings.ToLower(name) {
	case "least-loaded", "leastloaded", "least_loaded":
		newStrategy = &LeastLoadedPolicy{}
	case "prefix-tree", "prefixtree", "prefix_tree", "prefix":
		newStrategy = NewPrefixTreePolicy()
	case "gorgo":
		newStrategy = &GORGOPolicy{
			root: &GORGONode{
				children: make(map[byte]*GORGONode),
				peers:    []*PeerNode{},
				prefix:   "",
			},
		}
	default:
		return fmt.Errorf("unknown policy: %s (available: least-loaded, prefix-tree, gorgo)", name)
	}

	lb.peersMu.Lock()
	lb.strategy = newStrategy
	lb.config.Strategy = newStrategy
	lb.peersMu.Unlock()

	return nil
}

// handleTokenizerEndpoint tests the tokenizer and returns results
// GET: test with default phrases
// POST: test with custom text (body: {"text": "your text here"})
func (lb *LoadBalancer) handleTokenizerEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Default test phrases
	testPhrases := []string{
		"Hello, world!",
		"The quick brown fox jumps over the lazy dog.",
	}

	// Check for custom text in POST body or query param
	customText := r.URL.Query().Get("text")
	if r.Method == http.MethodPost {
		var req struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Text != "" {
			customText = req.Text
		}
	}

	if customText != "" {
		testPhrases = append(testPhrases, customText)
	}

	// Run tokenizer tests
	results := make([]map[string]interface{}, 0, len(testPhrases))
	allPassed := true
	isCGO := false

	for _, phrase := range testPhrases {
		tokenCount := GetTokenCount(phrase)
		charCount := len(phrase)

		// Estimate what we'd get with fallback (chars/4)
		fallbackEstimate := charCount / 4

		// If token count differs significantly from fallback, CGO tokenizer is working
		// CGO tokenizer gives more accurate counts (usually different from simple char/4)
		likelyCGO := tokenCount != fallbackEstimate
		if likelyCGO {
			isCGO = true
		}

		// Sanity check: tokens should be reasonable (1-50% of char count, roughly)
		reasonable := tokenCount >= 1 && tokenCount <= charCount
		if !reasonable {
			allPassed = false
		}

		results = append(results, map[string]interface{}{
			"text":              phrase,
			"token_count":       tokenCount,
			"char_count":        charCount,
			"fallback_estimate": fallbackEstimate,
			"likely_cgo":        likelyCGO,
			"reasonable":        reasonable,
		})
	}

	// Summary
	response := map[string]interface{}{
		"success":     allPassed,
		"cgo_enabled": isCGO,
		"results":     results,
		"message":     "",
	}

	if isCGO {
		response["message"] = "CGO tokenizer is working correctly (token counts differ from char/4 fallback)"
	} else {
		response["message"] = "WARNING: Likely using fallback tokenizer (char/4). CGO may not be enabled."
	}

	if !allPassed {
		w.WriteHeader(http.StatusInternalServerError)
		response["message"] = "Tokenizer test failed: unreasonable token counts detected"
	}

	json.NewEncoder(w).Encode(response)
}

// handleObservabilityEndpoint manages observability settings
// GET: returns current observability status
// POST: updates settings (enable/disable, change Loki endpoint)
func (lb *LoadBalancer) handleObservabilityEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet {
		// Return current status
		status := map[string]interface{}{
			"enabled":       lb.config.ObservabilityEnabled,
			"loki_endpoint": lb.config.LokiEndpoint,
			"node_id":       lb.config.NodeID,
			"cluster_name":  lb.config.ClusterName,
			"buffer_size":   0,
		}
		if lb.observability != nil {
			status["buffer_size"] = lb.observability.GetBufferSize()
		}
		json.NewEncoder(w).Encode(status)
		return
	}

	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		var req struct {
			Enabled      *bool   `json:"enabled"`
			LokiEndpoint *string `json:"loki_endpoint"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "invalid JSON: %v"}`, err), http.StatusBadRequest)
			return
		}

		changes := []string{}

		if req.Enabled != nil {
			old := lb.config.ObservabilityEnabled
			lb.config.ObservabilityEnabled = *req.Enabled
			if lb.observability != nil {
				lb.observability.SetEnabled(*req.Enabled)
			}
			changes = append(changes, fmt.Sprintf("enabled: %v -> %v", old, *req.Enabled))
		}

		if req.LokiEndpoint != nil {
			old := lb.config.LokiEndpoint
			lb.config.LokiEndpoint = *req.LokiEndpoint
			if lb.observability != nil {
				lb.observability.SetLokiEndpoint(*req.LokiEndpoint)
			}
			changes = append(changes, fmt.Sprintf("loki_endpoint: %s -> %s", old, *req.LokiEndpoint))
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"changes": changes,
		})
		return
	}

	http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
}

// handleLogsEndpoint returns recent request traces for viewing LB activity
// GET /lb/logs - returns last 20 traces
// GET /lb/logs?limit=50 - returns last 50 traces
// GET /lb/logs?format=text - returns human-readable text format
func (lb *LoadBalancer) handleLogsEndpoint(w http.ResponseWriter, r *http.Request) {
	// Parse query params
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	format := r.URL.Query().Get("format")

	if lb.observability == nil {
		http.Error(w, `{"error": "observability not initialized"}`, http.StatusInternalServerError)
		return
	}

	traces := lb.observability.GetRecentTraces(limit)

	if format == "text" {
		// Human-readable text format
		w.Header().Set("Content-Type", "text/plain")
		for i, tc := range traces {
			if tc == nil {
				continue
			}
			fmt.Fprintf(w, "=== Request %d [%s] ===\n", i+1, tc.TraceID[:8])
			for _, event := range tc.Events {
				fmt.Fprintf(w, "  [%s] %s\n", event.Timestamp.Format("15:04:05.000"), event.EventType)
				for k, v := range event.Metadata {
					fmt.Fprintf(w, "    %s: %v\n", k, v)
				}
			}
			fmt.Fprintln(w)
		}
		return
	}

	// JSON format (default)
	w.Header().Set("Content-Type", "application/json")

	// Convert traces to a simpler format for JSON
	type SimpleEvent struct {
		Time      string                 `json:"time"`
		EventType string                 `json:"event_type"`
		Metadata  map[string]interface{} `json:"metadata,omitempty"`
	}
	type SimpleTrace struct {
		TraceID string        `json:"trace_id"`
		Events  []SimpleEvent `json:"events"`
	}

	result := make([]SimpleTrace, 0, len(traces))
	for _, tc := range traces {
		if tc == nil {
			continue
		}
		st := SimpleTrace{
			TraceID: tc.TraceID,
			Events:  make([]SimpleEvent, 0),
		}
		for _, event := range tc.Events {
			st.Events = append(st.Events, SimpleEvent{
				Time:      event.Timestamp.Format("15:04:05.000"),
				EventType: string(event.EventType),
				Metadata:  event.Metadata,
			})
		}
		result = append(result, st)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"count":  len(result),
		"traces": result,
	})
}

// handlePeersEndpoint manages peer registration
// GET /lb/peers - list all peers
// POST /lb/peers - add a new peer {"ip": "1.2.3.4", "port": 8000}
// DELETE /lb/peers - remove a peer {"ip": "1.2.3.4", "port": 8000}
func (lb *LoadBalancer) handlePeersEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		// List all peers
		lb.peersMu.RLock()
		peers := make([]map[string]interface{}, 0, len(lb.peers))
		for peer, status := range lb.peers {
			ip := ""
			if peer.Instance != nil {
				ip = peer.Instance.IP
			}
			peerInfo := map[string]interface{}{
				"ip":           ip,
				"port":         peer.Port,
				"available":    status.Available,
				"healthy":      status.Healthy,
				"running_reqs": status.RunningRequests,
				"waiting_reqs": status.WaitingRequests,
				"total_reqs":   status.TotalRequests,
				"gpu_cache":    status.GPUCacheUsage,
				"max_load":     peer.MaxLoad,
			}
			peers = append(peers, peerInfo)
		}
		lb.peersMu.RUnlock()

		json.NewEncoder(w).Encode(map[string]interface{}{
			"count": len(peers),
			"peers": peers,
		})

	case http.MethodPost:
		// Add a new peer
		var req struct {
			IP   string `json:"ip"`
			Port int    `json:"port"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error": "invalid JSON"}`, http.StatusBadRequest)
			return
		}
		if req.IP == "" || req.Port == 0 {
			http.Error(w, `{"error": "ip and port are required"}`, http.StatusBadRequest)
			return
		}

		// Check if peer already exists
		lb.peersMu.Lock()
		for peer := range lb.peers {
			if peer.Instance != nil && peer.Instance.IP == req.IP && peer.Port == req.Port {
				lb.peersMu.Unlock()
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status":  "exists",
					"message": fmt.Sprintf("peer %s:%d already registered", req.IP, req.Port),
				})
				return
			}
		}

		// Create a minimal peer instance
		peer := &PeerNode{
			Instance: &remote.RunningInstance{IP: req.IP},
			Port:     req.Port,
			MaxLoad:  10, // Default max load
		}
		lb.peers[peer] = AvailabilityStatus{Available: true, Healthy: true}
		lb.peersMu.Unlock()

		log.Printf("[LB] Added peer: %s:%d", req.IP, req.Port)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "added",
			"message": fmt.Sprintf("peer %s:%d added successfully", req.IP, req.Port),
			"peers":   len(lb.peers),
		})

	case http.MethodDelete:
		// Remove a peer
		var req struct {
			IP   string `json:"ip"`
			Port int    `json:"port"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error": "invalid JSON"}`, http.StatusBadRequest)
			return
		}

		lb.peersMu.Lock()
		var toDelete *PeerNode
		for peer := range lb.peers {
			if peer.Instance != nil && peer.Instance.IP == req.IP && (req.Port == 0 || peer.Port == req.Port) {
				toDelete = peer
				break
			}
		}
		if toDelete != nil {
			delete(lb.peers, toDelete)
		}
		lb.peersMu.Unlock()

		if toDelete != nil {
			log.Printf("[LB] Removed peer: %s:%d", req.IP, req.Port)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "removed",
				"message": fmt.Sprintf("peer %s:%d removed", req.IP, req.Port),
			})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "not_found",
				"message": fmt.Sprintf("peer %s:%d not found", req.IP, req.Port),
			})
		}

	default:
		http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
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
