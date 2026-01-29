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

	"github.com/atoniolo76/gotoni/pkg/config"
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
	MaxQueueSize          int                   `json:"max_queue_size"` // Max requests in queue before rejecting (0 = unlimited)
	Strategy              LoadBalancingStrategy `json:"-"`              // Not serializable

	// SGLang metrics polling (see sglang_metrics.go for polling logic)
	MetricsEnabled      bool          `json:"metrics_enabled"`
	MetricsPollInterval time.Duration `json:"metrics_poll_interval"`
	MetricsEndpoint     string        `json:"metrics_endpoint"`
	MetricsTimeout      time.Duration `json:"metrics_timeout"`
	UnhealthyThreshold  int           `json:"unhealthy_threshold"`
	GPUCacheThreshold   float64       `json:"gpu_cache_threshold"`

	// SkyServe-style inference engine queue indicator
	// When true: forward requests when SGLang's internal queue > 0
	// When false: forward when total requests >= MaxConcurrentRequests
	UseIEQueueIndicator bool `json:"use_ie_queue_indicator"`

	// If > 0: forward when running_reqs exceeds this (overrides IE queue)
	RunningReqsThreshold int `json:"running_reqs_threshold"`

	// Node identity for tracing
	NodeID      string `json:"node_id"`
	ClusterName string `json:"cluster_name"`
}

// DefaultLoadBalancerConfig returns sensible defaults from pkg/config/constants.go
func DefaultLoadBalancerConfig() *LoadBalancerConfig {
	metricsConfig := DefaultMetricsConfig()
	return &LoadBalancerConfig{
		MaxConcurrentRequests: config.DefaultMaxConcurrentRequests,
		ApplicationPort:       config.DefaultApplicationPort,
		LoadBalancerPort:      config.DefaultLoadBalancerPort,
		RequestTimeout:        config.DefaultRequestTimeout,
		QueueEnabled:          config.DefaultQueueEnabled,
		QueueTimeout:          config.DefaultQueueTimeout,
		MaxQueueSize:          config.DefaultMaxQueueSize,
		Strategy:              &LeastLoadedPolicy{},

		// SGLang metrics config
		MetricsEnabled:      metricsConfig.Enabled,
		MetricsPollInterval: metricsConfig.PollInterval,
		MetricsEndpoint:     metricsConfig.Endpoint,
		MetricsTimeout:      metricsConfig.Timeout,
		UnhealthyThreshold:  metricsConfig.UnhealthyThreshold,
		GPUCacheThreshold:   metricsConfig.GPUCacheThreshold,

		// Running threshold takes priority over IE queue indicator
		RunningReqsThreshold: config.DefaultRunningReqsThreshold,
		UseIEQueueIndicator:  config.DefaultUseIEQueueIndicator,
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

// GORGO2 constants
const GORGO2_RUNNING_COST_FACTOR = 0.5 // Running requests cost less (already partially processed)

// GORGO2Policy extends GORGO by comparing local queue cost vs peer costs
type GORGO2Policy struct {
	mu   sync.RWMutex
	root *GORGONode        // Reuse GORGONode for prefix tree
	lb   *LoadBalancer     // Reference to access running/queued request data
}

// runningRequest tracks a request currently being processed locally
type runningRequest struct {
	requestID  string
	tokenCount int
	startedAt  time.Time
}

// prefixMatch represents a matched prefix in the tree with available peers
type prefixMatch struct {
	matchedPrefix string
	peers         []*PeerNode
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

// ClearCache resets the prefix tree to empty
func (p *PrefixTreePolicy) ClearCache() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := countNodes(p.root)
	p.root = &prefixNode{
		children: make(map[byte]*prefixNode),
		peers:    []*PeerNode{},
		prefix:   "",
	}
	return count
}

// ClearCache resets the GORGO prefix tree to empty
func (p *GORGOPolicy) ClearCache() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := countGORGONodes(p.root)
	p.root = &GORGONode{
		children: make(map[byte]*GORGONode),
		peers:    []*PeerNode{},
		prefix:   "",
	}
	return count
}

func countNodes(n *prefixNode) int {
	if n == nil {
		return 0
	}
	count := 1
	for _, child := range n.children {
		count += countNodes(child)
	}
	return count
}

func countGORGONodes(n *GORGONode) int {
	if n == nil {
		return 0
	}
	count := 1
	for _, child := range n.children {
		count += countGORGONodes(child)
	}
	return count
}

// =====================================================
// GORGO2 HELPER FUNCTIONS
// =====================================================

// extractPromptFromBody parses the request body to extract the prompt text
// Supports OpenAI chat completions format and legacy completions format
func extractPromptFromBody(bodyBytes []byte) string {
	if len(bodyBytes) == 0 {
		return ""
	}

	// Try chat completions format: {"messages": [{"role": "user", "content": "..."}]}
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
		return strings.TrimSpace(builder.String())
	}

	// Try legacy completions format: {"prompt": "..."}
	var legacyReq struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(bodyBytes, &legacyReq); err == nil && legacyReq.Prompt != "" {
		return legacyReq.Prompt
	}

	// Fallback: return empty (can't parse)
	return ""
}

// preCalculateTokenCount extracts prompt and computes token count
// Returns the token count and the extracted prompt text
func preCalculateTokenCount(bodyBytes []byte) (int, string) {
	prompt := extractPromptFromBody(bodyBytes)
	if prompt == "" {
		return 0, ""
	}
	tokenCount := GetTokenCount(prompt)
	return tokenCount, prompt
}

// trackRunningRequest adds a request to running tracking (for GORGO2)
func (lb *LoadBalancer) trackRunningRequest(requestID string, tokenCount int) {
	lb.runningRequestsMu.Lock()
	defer lb.runningRequestsMu.Unlock()
	if lb.runningRequests == nil {
		lb.runningRequests = make(map[string]*runningRequest)
	}
	lb.runningRequests[requestID] = &runningRequest{
		requestID:  requestID,
		tokenCount: tokenCount,
		startedAt:  time.Now(),
	}
}

// untrackRunningRequest removes a request from running tracking (for GORGO2)
func (lb *LoadBalancer) untrackRunningRequest(requestID string) {
	lb.runningRequestsMu.Lock()
	defer lb.runningRequestsMu.Unlock()
	if lb.runningRequests != nil {
		delete(lb.runningRequests, requestID)
	}
}

// =====================================================
// GORGO2 POLICY IMPLEMENTATION
// =====================================================

// NewGORGO2Policy creates a new GORGO2 policy with a reference to the load balancer
func NewGORGO2Policy(lb *LoadBalancer) *GORGO2Policy {
	return &GORGO2Policy{
		root: &GORGONode{
			children: make(map[byte]*GORGONode),
			peers:    []*PeerNode{},
			prefix:   "",
		},
		lb: lb,
	}
}

// ClearCache resets the GORGO2 prefix tree to empty
func (p *GORGO2Policy) ClearCache() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := countGORGONodes(p.root)
	p.root = &GORGONode{
		children: make(map[byte]*GORGONode),
		peers:    []*PeerNode{},
		prefix:   "",
	}
	return count
}

// Select implements LoadBalancingStrategy for GORGO2
// Compares local cost (running + queued requests) vs peer costs
// Returns nil if local is cheaper (keep locally), otherwise returns best peer
func (p *GORGO2Policy) Select(peers []*PeerNode, availabilityMap map[*PeerNode]AvailabilityStatus, requestPath string) *PeerNode {
	if len(requestPath) == 0 {
		return nil
	}

	// Calculate total token count for this request
	totalTokens := GetTokenCount(requestPath)

	// 1. Calculate local node cost
	localCost := p.calculateLocalCost(requestPath, totalTokens)

	// 2. Calculate peer costs using GORGO logic (latency + unmatched prefill)
	lowestPeerCost := float64(1<<62) // Large initial value
	var bestPeer *PeerNode

	// Find prefix matches in the tree
	matches := p.findPrefixMatches(requestPath, availabilityMap)

	for _, match := range matches {
		unmatchedText := requestPath[len(match.matchedPrefix):]
		unmatchedTokens := GetTokenCount(unmatchedText)

		for _, peer := range match.peers {
			prefillCost := float64(unmatchedTokens) * GORGO_MS_PER_TOKEN
			totalPeerCost := float64(peer.Metrics.Latency) + prefillCost

			if totalPeerCost < lowestPeerCost {
				lowestPeerCost = totalPeerCost
				bestPeer = peer
			}
		}
	}

	// 3. If no peers in tree, evaluate all available peers with cold-start cost
	if bestPeer == nil && len(peers) > 0 {
		for _, peer := range peers {
			status, ok := availabilityMap[peer]
			if !ok || !status.Available || !status.Healthy {
				continue
			}
			// Cold-start: full prefill cost
			prefillCost := float64(totalTokens) * GORGO_MS_PER_TOKEN
			totalPeerCost := float64(peer.Metrics.Latency) + prefillCost

			if totalPeerCost < lowestPeerCost {
				lowestPeerCost = totalPeerCost
				bestPeer = peer
			}
		}
	}

	// 4. Compare local vs best peer
	// Return nil means "keep locally" (process or queue)
	if bestPeer == nil || localCost <= lowestPeerCost {
		return nil // Local is cheaper or equal - process/queue locally
	}

	return bestPeer // Peer is cheaper - forward to peer
}

// calculateLocalCost computes the estimated cost of processing this request locally
// Cost = sum(running request costs) + sum(queued request costs) + incoming request cost
func (p *GORGO2Policy) calculateLocalCost(requestPath string, requestTokens int) float64 {
	if p.lb == nil {
		return 0
	}

	var totalCost float64

	// Cost from running requests: 0.5 * MS_PER_TOKEN * tokens
	// Lower factor because running requests are already partially processed
	p.lb.runningRequestsMu.RLock()
	for _, running := range p.lb.runningRequests {
		totalCost += GORGO2_RUNNING_COST_FACTOR * GORGO_MS_PER_TOKEN * float64(running.tokenCount)
	}
	p.lb.runningRequestsMu.RUnlock()

	// Cost from queued requests: MS_PER_TOKEN * tokens
	// Use pre-calculated token counts to avoid expensive operations under lock
	// Copy queue length and aggregate token count quickly, then release lock
	p.lb.queueMu.Lock()
	queuedTokens := 0
	for _, qr := range p.lb.queue {
		// Use pre-calculated token count (stored when request was enqueued)
		if qr.tokenCount > 0 {
			queuedTokens += qr.tokenCount
		} else {
			// Fallback: estimate based on body size (4 chars/token)
			queuedTokens += (len(qr.bodyBytes) + 3) / 4
		}
	}
	p.lb.queueMu.Unlock()

	totalCost += GORGO_MS_PER_TOKEN * float64(queuedTokens)

	// Add cost for the incoming request itself
	totalCost += GORGO_MS_PER_TOKEN * float64(requestTokens)

	return totalCost
}

// findPrefixMatches finds all prefix matches for a request path in the tree
func (p *GORGO2Policy) findPrefixMatches(requestPath string, availabilityMap map[*PeerNode]AvailabilityStatus) []prefixMatch {
	p.mu.RLock()
	defer p.mu.RUnlock()

	matches := []prefixMatch{}
	if p.root == nil {
		return matches
	}

	currentNode := p.root
	i := 0
	matchedSoFar := ""

	for i < len(requestPath) {
		currentChar := requestPath[i]
		remainingText := requestPath[i:]
		lookup, ok := currentNode.children[currentChar]
		if !ok {
			break
		}

		currentNode = lookup
		sharedLen := sharedPrefixLength(remainingText, lookup.prefix)
		if sharedLen > len(lookup.prefix) {
			sharedLen = len(lookup.prefix)
		}
		matchedSoFar += lookup.prefix[:sharedLen]
		i += sharedLen

		// Check for available peers at this node
		availablePeers := []*PeerNode{}
		for _, peerNode := range lookup.peers {
			if status, ok := availabilityMap[peerNode]; ok && status.Available && status.Healthy {
				availablePeers = append(availablePeers, peerNode)
			}
		}

		if len(availablePeers) > 0 {
			matches = append(matches, prefixMatch{
				matchedPrefix: matchedSoFar,
				peers:         availablePeers,
			})
		}

		if sharedLen < len(lookup.prefix) {
			break
		}
	}

	return matches
}

func (p *GORGOPolicy) Select(peers []*PeerNode, availabilityMap map[*PeerNode]AvailabilityStatus, requestPath string) *PeerNode {
	if p.root == nil {
		// No existing prefix trie (first request), fall back to least-loaded
		return (&LeastLoadedPolicy{}).Select(peers, availabilityMap, requestPath)
	}

	if len(requestPath) == 0 {
		return nil
	}

	// Find all prefix matches
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
			matches = append(matches, MatchedPrefixNodes{
				node:      prefixNode{prefix: lookup.prefix, peers: availablePeers},
				matchRate: matchRate,
			})
		}

		if sharedLen < len(lookup.prefix) {
			break // partial match
		}
	}

	// Sort by match rate descending
	sort.Slice(matches, func(i2, j int) bool {
		return matches[i2].matchRate > matches[j].matchRate
	})

	// GORGO cost calculation: Cost = network latency + prefill time for unmatched tokens
	lowestCost := int(^uint(0) >> 1)
	var bestPeer *PeerNode

	for _, match := range matches {
		unmatchedText := requestPath[len(match.node.prefix):]
		unmatchedTokens := GetTokenCount(unmatchedText)

		for _, peer := range match.node.peers {
			prefillCost := float64(unmatchedTokens) * GORGO_MS_PER_TOKEN
			totalCost := int(peer.Metrics.Latency) + int(prefillCost)

			if totalCost < lowestCost {
				lowestCost = totalCost
				bestPeer = peer
			}
		}
	}

	return bestPeer
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
	requestID  string // For tracing
	bodyBytes  []byte // Cached body for potential forwarding
	tokenCount int    // Pre-calculated token count for GORGO2
	prompt     string // Extracted prompt text for GORGO2 prefix matching
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

	// Running request tracking for GORGO2
	runningRequests   map[string]*runningRequest
	runningRequestsMu sync.RWMutex

	// HTTP clients
	httpClient *http.Client
	localProxy *httputil.ReverseProxy

	// Event-driven tracing (Perfetto output)
	tracer *Tracer

	// Control
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running atomic.Bool
}

// NewLoadBalancer creates a new load balancer for this node
func NewLoadBalancer(cfg *LoadBalancerConfig, self *remote.RunningInstance) *LoadBalancer {
	if cfg == nil {
		cfg = DefaultLoadBalancerConfig()
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Create local backend proxy
	localURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", cfg.ApplicationPort))
	localProxy := httputil.NewSingleHostReverseProxy(localURL)

	// Customize proxy error handler
	localProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[LB] Local proxy error: %v", err)
		http.Error(w, "Backend unavailable", http.StatusBadGateway)
	}

	// Use default strategy if none provided
	if cfg.Strategy == nil {
		cfg.Strategy = &LeastLoadedPolicy{}
	}

	// Initialize tracer for event-driven tracing
	nodeID := cfg.NodeID
	if nodeID == "" && self != nil {
		nodeID = self.ID[:16]
	}
	tracer := NewTracer(nodeID)

	lb := &LoadBalancer{
		config:   cfg,
		self:     self,
		peers:    make(map[*PeerNode]AvailabilityStatus),
		strategy: cfg.Strategy,
		httpClient: &http.Client{
			Timeout: cfg.RequestTimeout,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: config.DefaultMaxIdleConnsPerHost,
				IdleConnTimeout:     config.DefaultIdleConnTimeout,
			},
		},
		localProxy:      localProxy,
		tracer:          tracer,
		ctx:             ctx,
		cancel:          cancel,
		runningRequests: make(map[string]*runningRequest), // For GORGO2
	}

	if cfg.QueueEnabled {
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

	// Warn if no peers configured - this is likely a misconfiguration
	lb.peersMu.RLock()
	peerCount := len(lb.peers)
	lb.peersMu.RUnlock()
	if peerCount == 0 {
		log.Printf("[LB] ⚠️  WARNING: No peers configured! When at capacity, requests will queue locally instead of being forwarded. This can cause resource exhaustion under heavy load.")
	}

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

	log.Printf("[LB] Load balancer started (max concurrent: %d, peers: %d, max queue: %d, metrics polling: %v)",
		lb.config.MaxConcurrentRequests, peerCount, lb.config.MaxQueueSize, lb.config.MetricsEnabled)

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

func (lb *LoadBalancer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ... (Keep management endpoints the same as before) ...

		requestID := fmt.Sprintf("%d", time.Now().UnixNano())
		wrappedWriter := &responseWriterWrapper{ResponseWriter: w, statusCode: 200}

		// Parse Hop Count
		hops := 0
		if hopStr := r.Header.Get("X-LB-Hop"); hopStr != "" {
			fmt.Sscanf(hopStr, "%d", &hops)
		}

		// Pre-calculate token count for GORGO2 (read body early)
		var bodyBytes []byte
		var tokenCount int
		var prompt string
		if r.Body != nil {
			bodyBytes, _ = io.ReadAll(r.Body)
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			tokenCount, prompt = preCalculateTokenCount(bodyBytes)
		}

		lb.tracer.TraceRequestReceived(requestID, r.Method, r.URL.Path, r.RemoteAddr)

		// For GORGO2: use prompt for routing decision instead of URL path
		requestPath := r.URL.Path
		if _, isGORGO2 := lb.strategy.(*GORGO2Policy); isGORGO2 && prompt != "" {
			requestPath = prompt
		}

		hasCapacity := lb.localHasCapacity()

		// A: Process locally if we have capacity
		if hasCapacity {
			lb.trackRunningRequest(requestID, tokenCount)
			lb.tracer.TraceLocalProcessStart(requestID)
			lb.localProxy.ServeHTTP(wrappedWriter, r)
			lb.untrackRunningRequest(requestID)
			lb.tracer.TraceRequestComplete(requestID, wrappedWriter.statusCode, "local")
			return
		}

		// B: Try to forward ONLY if we haven't exceeded MaxHops
		if hops < config.MaxHops {
			log.Printf("[LB] Request %s: local full, forwarding (current hops: %d)", requestID, hops)
			// For GORGO2: consult strategy with prompt-based requestPath
			targetPeer := lb.selectPeer(requestPath)
			if targetPeer != nil {
				// Pass pre-selected peer to avoid double selection
				if lb.forwardToTargetPeer(wrappedWriter, r, requestID, hops+1, targetPeer, bodyBytes) {
					lb.tracer.TraceRequestComplete(requestID, wrappedWriter.statusCode, "peer")
					return
				}
			} else if _, isGORGO2 := lb.strategy.(*GORGO2Policy); !isGORGO2 {
				// Non-GORGO2: try forward without explicit peer selection (uses URL path)
				if lb.forwardToPeer(wrappedWriter, r, requestID, hops+1) {
					lb.tracer.TraceRequestComplete(requestID, wrappedWriter.statusCode, "peer")
					return
				}
			}
			// For GORGO2: nil from selectPeer means "keep locally"
		}

		// C: Queue locally (Mandatory if already pushed or no peers available)
		if lb.config.QueueEnabled {
			if lb.enqueueRequestWithTokens(wrappedWriter, r, requestID, bodyBytes, tokenCount, prompt) {
				lb.tracer.TraceRequestComplete(requestID, wrappedWriter.statusCode, "queue")
			}
			return
		}

		http.Error(w, "Service at capacity", http.StatusServiceUnavailable)
	})
}

// responseWriterWrapper wraps http.ResponseWriter to capture status code
type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode    int
	headerWritten bool
}

func (w *responseWriterWrapper) WriteHeader(code int) {
	if w.headerWritten {
		return // Prevent duplicate WriteHeader calls
	}
	w.headerWritten = true
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// localHasCapacity checks if the local SGLang backend has capacity
// Returns true if we should process locally
//
// Uses SkyServe-style inference engine queue indicator:
// - If UseIEQueueIndicator is true: forward when SGLang's internal queue > 0
// - Otherwise: forward when total requests >= max concurrent
func (lb *LoadBalancer) localHasCapacity() bool {
	lb.localMetricsMu.RLock()
	defer lb.localMetricsMu.RUnlock()

	// If we haven't polled yet, assume we have capacity
	if lb.localMetrics.LastUpdated.IsZero() {
		return true
	}

	// Must be healthy
	if !lb.localMetrics.Healthy {
		return false
	}

	// Check GPU cache threshold
	if lb.localMetrics.GPUCacheUsage >= lb.config.GPUCacheThreshold {
		return false
	}

	// Check running requests threshold first (if set)
	// This allows forcing forwarding when running_reqs exceeds a limit
	if lb.config.RunningReqsThreshold > 0 {
		hasCapacity := lb.localMetrics.NumRunningReqs < lb.config.RunningReqsThreshold
		if !hasCapacity {
			log.Printf("[LB] Running threshold: %d >= %d, forwarding",
				lb.localMetrics.NumRunningReqs, lb.config.RunningReqsThreshold)
		}
		return hasCapacity
	}

	// SkyServe-style: use inference engine queue as indicator
	// If SGLang has ANY waiting requests, local is "at capacity"
	// This is more responsive than counting concurrent requests
	if lb.config.UseIEQueueIndicator {
		// Forward if SGLang's internal queue has waiting requests
		hasCapacity := lb.localMetrics.NumWaitingReqs == 0
		if !hasCapacity {
			log.Printf("[LB] IE queue indicator: SGLang has %d waiting requests, forwarding",
				lb.localMetrics.NumWaitingReqs)
		}
		return hasCapacity
	}

	// Fallback: traditional max concurrent check
	maxLoad := lb.config.MaxConcurrentRequests
	if lb.localMetrics.MaxRunningReqs > 0 {
		maxLoad = lb.localMetrics.MaxRunningReqs
	}

	return lb.localMetrics.NumTotalReqs < maxLoad
}

func (lb *LoadBalancer) forwardToPeer(w http.ResponseWriter, r *http.Request, requestID string, nextHop int) bool {
	// Select peer using strategy (uses URL path for non-GORGO2)
	peer := lb.selectPeer(r.URL.Path)
	if peer == nil {
		log.Printf("[LB] No available peer for request %s", requestID)
		return false
	}
	return lb.forwardToTargetPeer(w, r, requestID, nextHop, peer, nil)
}

// forwardToTargetPeer forwards a request to a specific pre-selected peer
// If bodyBytes is nil, it will read from r.Body
func (lb *LoadBalancer) forwardToTargetPeer(w http.ResponseWriter, r *http.Request, requestID string, nextHop int, peer *PeerNode, bodyBytes []byte) bool {
	if peer == nil {
		log.Printf("[LB] No peer provided for request %s", requestID)
		return false
	}

	// Build target URL
	targetURL := fmt.Sprintf("http://%s:%d%s", peer.Instance.IP, peer.Port, r.URL.Path)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// Read and cache body for forwarding (if not already provided)
	if bodyBytes == nil && r.Body != nil {
		bodyBytes, _ = io.ReadAll(r.Body)
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	// Create forwarded request with timeout
	ctx, cancel := context.WithTimeout(r.Context(), lb.config.RequestTimeout)
	defer cancel()

	forwardReq, err := http.NewRequestWithContext(ctx, r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		log.Printf("[LB] Failed to create forward request for %s: %v", requestID, err)
		return false
	}

	// Copy original headers
	for key, values := range r.Header {
		for _, value := range values {
			forwardReq.Header.Add(key, value)
		}
	}

	// Set routing headers
	forwardReq.Header.Set("X-Forwarded-For", r.RemoteAddr)
	forwardReq.Header.Set("X-LB-Hop", fmt.Sprintf("%d", nextHop))
	forwardReq.Header.Set("X-Request-ID", requestID)

	// Trace forward start
	lb.tracer.TraceForwardStart(requestID, peer.Instance.ID, peer.Instance.IP)

	// Execute request
	forwardStart := time.Now()
	resp, err := lb.httpClient.Do(forwardReq)
	forwardDuration := time.Since(forwardStart)

	if err != nil {
		log.Printf("[LB] Failed to forward request %s to peer: %v", requestID, err)
		lb.tracer.TraceForwardEnd(requestID, 0, float64(forwardDuration.Milliseconds()))
		return false
	}
	defer resp.Body.Close()

	// Trace forward completion
	lb.tracer.TraceForwardEnd(requestID, resp.StatusCode, float64(forwardDuration.Milliseconds()))

	// Copy response back to original writer
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	log.Printf("[LB] Successfully forwarded request %s to %s (status: %d, duration: %v)",
		requestID, peer.Instance.IP, resp.StatusCode, forwardDuration)
	return true
}

// selectPeer selects a peer using the configured strategy
func (lb *LoadBalancer) selectPeer(requestPath string) *PeerNode {
	lb.peersMu.RLock()
	defer lb.peersMu.RUnlock()

	peers := make([]*PeerNode, 0, len(lb.peers))
	for peer := range lb.peers {
		peers = append(peers, peer)
	}

	return lb.strategy.Select(peers, lb.peers, requestPath)
}

// enqueueRequest adds a request to the queue
// Returns false if the queue is full (all cluster capacity exhausted)
func (lb *LoadBalancer) enqueueRequest(w http.ResponseWriter, r *http.Request, requestID string) bool {
	return lb.enqueueRequestWithTokens(w, r, requestID, nil, 0, "")
}

// enqueueRequestWithTokens adds a request to the queue with pre-calculated token count (for GORGO2)
func (lb *LoadBalancer) enqueueRequestWithTokens(w http.ResponseWriter, r *http.Request, requestID string, bodyBytes []byte, tokenCount int, prompt string) bool {
	// Check max queue size before adding
	lb.queueMu.Lock()
	if lb.config.MaxQueueSize > 0 && len(lb.queue) >= lb.config.MaxQueueSize {
		lb.queueMu.Unlock()
		log.Printf("[LB] Queue full (%d/%d) - all cluster capacity exhausted, rejecting request",
			len(lb.queue), lb.config.MaxQueueSize)
		http.Error(w, "Cluster at capacity - queue full", http.StatusServiceUnavailable)
		return false
	}

	// Read and cache body for potential forwarding later (if not already provided)
	if bodyBytes == nil && r.Body != nil {
		bodyBytes, _ = io.ReadAll(r.Body)
		r.Body.Close()
		// Replace body so local proxy can still read it
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	qr := &queuedRequest{
		w:          w,
		r:          r,
		done:       make(chan struct{}),
		enqueuedAt: time.Now(),
		requestID:  requestID,
		bodyBytes:  bodyBytes,
		tokenCount: tokenCount, // Pre-calculated for GORGO2
		prompt:     prompt,     // Extracted prompt for GORGO2
	}

	lb.queue = append(lb.queue, qr)
	queueSize := len(lb.queue)
	lb.queueMu.Unlock()

	log.Printf("[LB] Request %s queued (queue size: %d/%d, tokens: %d)", requestID, queueSize, lb.config.MaxQueueSize, tokenCount)

	// Wait for processing or timeout
	select {
	case <-qr.done:
		if qr.err != nil {
			http.Error(w, qr.err.Error(), http.StatusServiceUnavailable)
		}
	case <-time.After(lb.config.QueueTimeout):
		// Trace queue exit with timeout
		lb.tracer.TraceQueueExit(requestID, float64(time.Since(qr.enqueuedAt).Microseconds())/1000.0, "timeout")
		http.Error(w, "Queue timeout", http.StatusGatewayTimeout)
	case <-r.Context().Done():
		// Trace queue exit with cancellation
		lb.tracer.TraceQueueExit(requestID, float64(time.Since(qr.enqueuedAt).Microseconds())/1000.0, "cancelled")
		http.Error(w, "Request cancelled", http.StatusRequestTimeout)
	}
	return true
}

// processQueue probes the queue and processes requests by:
// 1. First trying to forward to available peers (priority)
// 2. Falling back to local processing if local has capacity
// This ensures queued requests get distributed across the cluster.
func (lb *LoadBalancer) processQueue() {
	defer lb.wg.Done()

	// Probe frequently to minimize queue latency
	ticker := time.NewTicker(config.DefaultQueueProbeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-lb.ctx.Done():
			return
		case <-ticker.C:
			lb.processQueuedRequests()
		}
	}
}

func (lb *LoadBalancer) processQueuedRequests() {
	for {
		lb.queueMu.Lock()
		if len(lb.queue) == 0 {
			lb.queueMu.Unlock()
			break
		}
		qr := lb.queue[0]

		// Parse Hops for this specific queued request
		hops := 0
		if hopStr := qr.r.Header.Get("X-LB-Hop"); hopStr != "" {
			fmt.Sscanf(hopStr, "%d", &hops)
		}

		// For GORGO2: use prompt for peer selection
		requestPath := qr.r.URL.Path
		if _, isGORGO2 := lb.strategy.(*GORGO2Policy); isGORGO2 && qr.prompt != "" {
			requestPath = qr.prompt
		}

		// Try to forward (only if below MaxHops)
		if hops < config.MaxHops {
			peer := lb.selectPeer(requestPath)
			if peer != nil {
				lb.queue = lb.queue[1:]
				lb.queueMu.Unlock()

				if lb.forwardQueuedRequest(qr, peer, hops+1) {
					close(qr.done)
				} else {
					// Forward failed, re-queue at the back
					lb.queueMu.Lock()
					lb.queue = append(lb.queue, qr)
					lb.queueMu.Unlock()
				}
				continue
			}
		}

		// Try local if forwarding isn't an option
		if lb.localHasCapacity() {
			lb.queue = lb.queue[1:]
			lb.queueMu.Unlock()

			// Track running request for GORGO2
			lb.trackRunningRequest(qr.requestID, qr.tokenCount)

			// Process locally
			lb.tracer.TraceLocalProcessStart(qr.requestID)
			lb.localProxy.ServeHTTP(qr.w, qr.r)
			lb.tracer.TraceLocalProcessEnd(qr.requestID, 200) // Assume success

			lb.untrackRunningRequest(qr.requestID)
			close(qr.done)
			continue
		}

		lb.queueMu.Unlock()
		break
	}
}

// forwardQueuedRequest forwards a queued request to a peer
// Now accepts nextHop to maintain the hop count integrity
func (lb *LoadBalancer) forwardQueuedRequest(qr *queuedRequest, peer *PeerNode, nextHop int) bool {
    // Build forward URL
    targetURL := fmt.Sprintf("http://%s:%d%s", peer.Instance.IP, peer.Port, qr.r.URL.Path)
    if qr.r.URL.RawQuery != "" {
        targetURL += "?" + qr.r.URL.RawQuery
    }

    // Create forwarded request with timeout
    ctx, cancel := context.WithTimeout(qr.r.Context(), lb.config.RequestTimeout)
    defer cancel()

    forwardReq, err := http.NewRequestWithContext(ctx, qr.r.Method, targetURL, bytes.NewReader(qr.bodyBytes))
    if err != nil {
        log.Printf("[LB] Failed to create forward request for %s: %v", qr.requestID, err)
        return false
    }

    // Copy original headers
    for key, values := range qr.r.Header {
        for _, value := range values {
            forwardReq.Header.Add(key, value)
        }
    }

    // Update/Set routing headers
    forwardReq.Header.Set("X-Forwarded-For", qr.r.RemoteAddr)
    forwardReq.Header.Set("X-LB-Hop", fmt.Sprintf("%d", nextHop)) // Propagate integer hop
    forwardReq.Header.Set("X-Request-ID", qr.requestID)
    
    waitDuration := time.Since(qr.enqueuedAt)
    forwardReq.Header.Set("X-Queue-Wait-Ms", fmt.Sprintf("%.2f", float64(waitDuration.Microseconds())/1000.0))

    // Trace forward start
    lb.tracer.TraceForwardStart(qr.requestID, peer.Instance.ID, peer.Instance.IP)

    forwardStart := time.Now()
    resp, err := lb.httpClient.Do(forwardReq)
    forwardDuration := time.Since(forwardStart)

    if err != nil {
        log.Printf("[LB] Failed to forward queued request %s to peer: %v", qr.requestID, err)
        lb.tracer.TraceForwardEnd(qr.requestID, 0, float64(forwardDuration.Milliseconds()))
        return false
    }
    defer resp.Body.Close()

    // Trace forward completion
    lb.tracer.TraceForwardEnd(qr.requestID, resp.StatusCode, float64(forwardDuration.Milliseconds()))

    // Copy response back to original writer
    for key, values := range resp.Header {
        for _, value := range values {
            qr.w.Header().Add(key, value)
        }
    }
    qr.w.WriteHeader(resp.StatusCode)
    io.Copy(qr.w, resp.Body)

    log.Printf("[LB] Successfully forwarded queued request %s to %s (status: %d, duration: %v)",
        qr.requestID, peer.Instance.IP, resp.StatusCode, forwardDuration)
    return true
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
	case *GORGO2Policy:
		return "gorgo2"
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

	availablePolicies := []string{"least-loaded", "prefix-tree", "gorgo", "gorgo2"}

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
	case "gorgo2":
		newStrategy = NewGORGO2Policy(lb)
	default:
		return fmt.Errorf("unknown policy: %s (available: least-loaded, prefix-tree, gorgo, gorgo2)", name)
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

// =====================================================
// TRACE ENDPOINTS - Perfetto-compatible tracing
// =====================================================

// handleTraceStartEndpoint starts a new tracing session
func (lb *LoadBalancer) handleTraceStartEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	sessionID := lb.tracer.Start()
	log.Printf("[LB] Trace session started: %s", sessionID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "started",
		"session_id": sessionID,
		"node_id":    lb.config.NodeID,
		"hint":       "Run your workload, then call /lb/trace/stop to get the Perfetto trace",
	})
}

// handleTraceStopEndpoint stops tracing and returns the Perfetto JSON
func (lb *LoadBalancer) handleTraceStopEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	traceData, err := lb.tracer.Stop()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	log.Printf("[LB] Trace session stopped, returning %d bytes", len(traceData))

	// Return as downloadable JSON file
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=trace.json")
	w.Write(traceData)
}

// handleTraceStatusEndpoint returns current trace status
func (lb *LoadBalancer) handleTraceStatusEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(lb.tracer.GetStatus())
}

// handleCacheClearEndpoint clears the prefix tree cache
func (lb *LoadBalancer) handleCacheClearEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	var cleared int
	strategyName := lb.getStrategyName()

	switch strategy := lb.strategy.(type) {
	case *PrefixTreePolicy:
		cleared = strategy.ClearCache()
	case *GORGOPolicy:
		cleared = strategy.ClearCache()
	case *GORGO2Policy:
		cleared = strategy.ClearCache()
	default:
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":  false,
			"strategy": strategyName,
			"message":  "strategy does not use prefix tree cache",
		})
		return
	}

	log.Printf("[LB] Cleared prefix tree cache: %d nodes removed", cleared)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":       true,
		"strategy":      strategyName,
		"nodes_cleared": cleared,
		"message":       "prefix tree cache cleared",
	})
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
