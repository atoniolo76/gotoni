/*
Copyright © 2025 ALESSIO TONIOLO

lb_observability.go provides observability for the distributed load balancer.
It pushes structured logs and metrics to Loki for visualization in Grafana.

Key features:
- Trace context for request waterfall diagrams
- Component timing spans (tokenizer, policy, forward)
- SGLang metrics aggregation across nodes
- Load balancer action logging (forwards, policy decisions)
*/
package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// EventType categorizes observability events for filtering in Grafana
type EventType string

const (
	// Request lifecycle events
	EventRequestReceived  EventType = "request_received"
	EventRequestCompleted EventType = "request_completed"
	EventRequestForwarded EventType = "request_forwarded"
	EventRequestQueued    EventType = "request_queued"
	EventRequestDequeued  EventType = "request_dequeued"

	// Policy events
	EventPolicySelect     EventType = "policy_select"
	EventGORGOCostCalc    EventType = "gorgo_cost_calc"
	EventPrefixTreeLookup EventType = "prefix_tree_lookup"

	// Component timing events
	EventTokenizerStart   EventType = "tokenizer_start"
	EventTokenizerEnd     EventType = "tokenizer_end"
	EventHTTPForwardStart EventType = "http_forward_start"
	EventHTTPForwardEnd   EventType = "http_forward_end"
	EventLocalProxyStart  EventType = "local_proxy_start"
	EventLocalProxyEnd    EventType = "local_proxy_end"

	// Metrics events
	EventMetricsPoll  EventType = "metrics_poll"
	EventPeerMetrics  EventType = "peer_metrics"
	EventLocalMetrics EventType = "local_metrics"

	// Health events
	EventPeerUnhealthy  EventType = "peer_unhealthy"
	EventPeerRecovered  EventType = "peer_recovered"
	EventLocalUnhealthy EventType = "local_unhealthy"
)

// LBEvent represents a single observability event
type LBEvent struct {
	Timestamp  time.Time              `json:"timestamp"`
	TraceID    string                 `json:"trace_id"`    // Groups events for one request
	SpanID     string                 `json:"span_id"`     // Unique ID for this event
	ParentSpan string                 `json:"parent_span"` // For nested spans
	NodeID     string                 `json:"node_id"`     // Which LB node
	EventType  EventType              `json:"event_type"`
	DurationMs float64                `json:"duration_ms"` // For end events
	Metadata   map[string]interface{} `json:"metadata"`
}

// TraceContext holds trace information for a single request
type TraceContext struct {
	TraceID   string
	SpanStack []string // Stack of span IDs for nesting
	StartTime time.Time
	Events    []LBEvent
	mu        sync.Mutex
}

// NewTraceContext creates a new trace context for a request
func NewTraceContext() *TraceContext {
	return &TraceContext{
		TraceID:   uuid.New().String()[:8], // Short trace ID for readability
		SpanStack: []string{},
		StartTime: time.Now(),
		Events:    []LBEvent{},
	}
}

// StartSpan begins a new timing span, returns span ID
func (tc *TraceContext) StartSpan(eventType EventType, metadata map[string]interface{}) string {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	spanID := uuid.New().String()[:8]
	parentSpan := ""
	if len(tc.SpanStack) > 0 {
		parentSpan = tc.SpanStack[len(tc.SpanStack)-1]
	}
	tc.SpanStack = append(tc.SpanStack, spanID)

	event := LBEvent{
		Timestamp:  time.Now(),
		TraceID:    tc.TraceID,
		SpanID:     spanID,
		ParentSpan: parentSpan,
		EventType:  eventType,
		Metadata:   metadata,
	}
	tc.Events = append(tc.Events, event)

	return spanID
}

// EndSpan completes a timing span with duration
func (tc *TraceContext) EndSpan(spanID string, endEventType EventType, metadata map[string]interface{}) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	// Find the start event to calculate duration
	var startTime time.Time
	for _, e := range tc.Events {
		if e.SpanID == spanID {
			startTime = e.Timestamp
			break
		}
	}

	duration := time.Since(startTime)

	// Pop from span stack
	if len(tc.SpanStack) > 0 {
		tc.SpanStack = tc.SpanStack[:len(tc.SpanStack)-1]
	}

	parentSpan := ""
	if len(tc.SpanStack) > 0 {
		parentSpan = tc.SpanStack[len(tc.SpanStack)-1]
	}

	event := LBEvent{
		Timestamp:  time.Now(),
		TraceID:    tc.TraceID,
		SpanID:     spanID,
		ParentSpan: parentSpan,
		EventType:  endEventType,
		DurationMs: float64(duration.Microseconds()) / 1000.0,
		Metadata:   metadata,
	}
	tc.Events = append(tc.Events, event)
}

// AddEvent adds a standalone event (not a span)
func (tc *TraceContext) AddEvent(eventType EventType, metadata map[string]interface{}) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	parentSpan := ""
	if len(tc.SpanStack) > 0 {
		parentSpan = tc.SpanStack[len(tc.SpanStack)-1]
	}

	event := LBEvent{
		Timestamp:  time.Now(),
		TraceID:    tc.TraceID,
		SpanID:     uuid.New().String()[:8],
		ParentSpan: parentSpan,
		EventType:  eventType,
		Metadata:   metadata,
	}
	tc.Events = append(tc.Events, event)
}

// LBObservability handles metrics/log shipping to Loki
type LBObservability struct {
	lokiEndpoint string
	httpClient   *http.Client
	nodeID       string
	clusterName  string

	// Event buffer for batching
	buffer   []LBEvent
	bufferMu sync.Mutex

	// Trace history for /lb/logs endpoint
	traceHistory    []*TraceContext
	traceHistoryMu  sync.RWMutex
	maxTraceHistory int

	// Configuration
	pushInterval  time.Duration
	maxBufferSize int
	enabled       bool

	// Control
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// LBObservabilityConfig holds configuration for observability
type LBObservabilityConfig struct {
	LokiEndpoint  string        `json:"loki_endpoint"` // e.g., "http://localhost:3100/loki/api/v1/push"
	NodeID        string        `json:"node_id"`
	ClusterName   string        `json:"cluster_name"`
	PushInterval  time.Duration `json:"push_interval"`   // How often to batch push
	MaxBufferSize int           `json:"max_buffer_size"` // Flush when buffer reaches this size
	Enabled       bool          `json:"enabled"`
}

// DefaultObservabilityConfig returns sensible defaults
func DefaultObservabilityConfig() LBObservabilityConfig {
	return LBObservabilityConfig{
		LokiEndpoint:  "http://localhost:3100/loki/api/v1/push",
		NodeID:        "unknown",
		ClusterName:   "default",
		PushInterval:  5 * time.Second,
		MaxBufferSize: 100,
		Enabled:       false, // Disabled by default
	}
}

// NewLBObservability creates a new observability instance
func NewLBObservability(config LBObservabilityConfig) *LBObservability {
	ctx, cancel := context.WithCancel(context.Background())

	return &LBObservability{
		lokiEndpoint:    config.LokiEndpoint,
		nodeID:          config.NodeID,
		clusterName:     config.ClusterName,
		httpClient:      &http.Client{Timeout: 5 * time.Second},
		buffer:          make([]LBEvent, 0, config.MaxBufferSize),
		traceHistory:    make([]*TraceContext, 0, 100),
		maxTraceHistory: 100, // Keep last 100 request traces
		pushInterval:    config.PushInterval,
		maxBufferSize:   config.MaxBufferSize,
		enabled:         config.Enabled,
		ctx:             ctx,
		cancel:          cancel,
	}
}

// Start begins the background event pusher
func (o *LBObservability) Start() {
	if !o.enabled {
		log.Printf("[Observability] Disabled, not starting push loop")
		return
	}

	o.wg.Add(1)
	go o.pushLoop()

	log.Printf("[Observability] Started (endpoint: %s, interval: %v)", o.lokiEndpoint, o.pushInterval)
}

// Stop gracefully stops the observability system
func (o *LBObservability) Stop() {
	o.cancel()
	o.wg.Wait()

	// Final flush
	o.flush()

	log.Printf("[Observability] Stopped")
}

// RecordEvent adds an event to the buffer
func (o *LBObservability) RecordEvent(event LBEvent) {
	if !o.enabled {
		return
	}

	// Ensure node ID is set
	if event.NodeID == "" {
		event.NodeID = o.nodeID
	}

	o.bufferMu.Lock()
	o.buffer = append(o.buffer, event)
	shouldFlush := len(o.buffer) >= o.maxBufferSize
	o.bufferMu.Unlock()

	if shouldFlush {
		go o.flush()
	}
}

// RecordTrace records all events from a trace context
func (o *LBObservability) RecordTrace(tc *TraceContext) {
	if tc == nil {
		return
	}

	// Always save to trace history (even if Loki push is disabled)
	// Make a copy of the events to avoid race conditions
	tc.mu.Lock()
	eventsCopy := make([]LBEvent, len(tc.Events))
	copy(eventsCopy, tc.Events)
	tc.mu.Unlock()

	traceCopy := &TraceContext{
		TraceID: tc.TraceID,
		Events:  eventsCopy,
	}

	o.traceHistoryMu.Lock()
	o.traceHistory = append(o.traceHistory, traceCopy)
	// Keep only last N traces
	if len(o.traceHistory) > o.maxTraceHistory {
		o.traceHistory = o.traceHistory[len(o.traceHistory)-o.maxTraceHistory:]
	}
	o.traceHistoryMu.Unlock()

	// Push to Loki if enabled
	if !o.enabled {
		return
	}

	for _, event := range eventsCopy {
		event.NodeID = o.nodeID
		o.RecordEvent(event)
	}
}

// GetRecentTraces returns recent request traces for the /lb/logs endpoint
func (o *LBObservability) GetRecentTraces(limit int) []*TraceContext {
	o.traceHistoryMu.RLock()
	defer o.traceHistoryMu.RUnlock()

	if limit <= 0 || limit > len(o.traceHistory) {
		limit = len(o.traceHistory)
	}

	// Return most recent traces
	start := len(o.traceHistory) - limit
	if start < 0 {
		start = 0
	}

	result := make([]*TraceContext, limit)
	copy(result, o.traceHistory[start:])
	return result
}

// RecordMetrics records SGLang metrics as an event
func (o *LBObservability) RecordMetrics(metrics SGLangMetrics, peerID string, isLocal bool) {
	if !o.enabled {
		return
	}

	eventType := EventPeerMetrics
	if isLocal {
		eventType = EventLocalMetrics
	}

	event := LBEvent{
		Timestamp: time.Now(),
		TraceID:   "", // Metrics don't have trace context
		SpanID:    uuid.New().String()[:8],
		NodeID:    o.nodeID,
		EventType: eventType,
		Metadata: map[string]interface{}{
			"peer_id":          peerID,
			"is_local":         isLocal,
			"running_reqs":     metrics.NumRunningReqs,
			"waiting_reqs":     metrics.NumWaitingReqs,
			"total_reqs":       metrics.NumTotalReqs,
			"gpu_cache_usage":  metrics.GPUCacheUsage,
			"max_running_reqs": metrics.MaxRunningReqs,
			"healthy":          metrics.Healthy,
			"latency_ms":       metrics.Latency,
		},
	}

	o.RecordEvent(event)
}

// RecordForward records a forwarding decision with full details
func (o *LBObservability) RecordForward(tc *TraceContext, targetPeerID, targetIP string, port int, policy string, metadata map[string]interface{}) {
	if !o.enabled {
		return
	}

	// Merge provided metadata with forward-specific fields
	fullMetadata := map[string]interface{}{
		"target_peer_id": targetPeerID,
		"target_ip":      targetIP,
		"target_port":    port,
		"policy":         policy,
	}
	for k, v := range metadata {
		fullMetadata[k] = v
	}

	if tc != nil {
		tc.AddEvent(EventRequestForwarded, fullMetadata)
	}
}

// RecordGORGODecision records GORGO policy calculation details
func (o *LBObservability) RecordGORGODecision(tc *TraceContext, peerID string, networkLatencyMs int64, unmatchedTokens int, prefillCostMs float64, totalCostMs float64, matchRate float64) {
	if !o.enabled {
		return
	}

	metadata := map[string]interface{}{
		"peer_id":            peerID,
		"network_latency_ms": networkLatencyMs,
		"unmatched_tokens":   unmatchedTokens,
		"prefill_cost_ms":    prefillCostMs,
		"total_cost_ms":      totalCostMs,
		"match_rate":         matchRate,
		"ms_per_token":       GORGO_MS_PER_TOKEN,
	}

	if tc != nil {
		tc.AddEvent(EventGORGOCostCalc, metadata)
	}
}

// pushLoop runs the background push loop
func (o *LBObservability) pushLoop() {
	defer o.wg.Done()

	ticker := time.NewTicker(o.pushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-o.ctx.Done():
			return
		case <-ticker.C:
			o.flush()
		}
	}
}

// flush sends buffered events to Loki
func (o *LBObservability) flush() {
	o.bufferMu.Lock()
	if len(o.buffer) == 0 {
		o.bufferMu.Unlock()
		return
	}

	// Take ownership of buffer
	events := o.buffer
	o.buffer = make([]LBEvent, 0, o.maxBufferSize)
	o.bufferMu.Unlock()

	// Build Loki push request
	if err := o.pushToLoki(events); err != nil {
		log.Printf("[Observability] Failed to push to Loki: %v", err)
		// Could re-buffer events here, but for now we log and drop
	}
}

// LokiPushRequest is the format expected by Loki's push API
type LokiPushRequest struct {
	Streams []LokiStream `json:"streams"`
}

// LokiStream represents a log stream in Loki
type LokiStream struct {
	Stream map[string]string `json:"stream"` // Labels
	Values [][]string        `json:"values"` // [timestamp_ns, log_line]
}

// pushToLoki sends events to Loki
func (o *LBObservability) pushToLoki(events []LBEvent) error {
	// Group events by type for better Loki label efficiency
	streams := make(map[EventType][]LBEvent)
	for _, event := range events {
		streams[event.EventType] = append(streams[event.EventType], event)
	}

	lokiReq := LokiPushRequest{
		Streams: make([]LokiStream, 0, len(streams)),
	}

	for eventType, typeEvents := range streams {
		stream := LokiStream{
			Stream: map[string]string{
				"job":        "gotoni_lb",
				"cluster":    o.clusterName,
				"node_id":    o.nodeID,
				"event_type": string(eventType),
			},
			Values: make([][]string, 0, len(typeEvents)),
		}

		for _, event := range typeEvents {
			// Loki wants timestamp as nanoseconds string
			tsNanos := fmt.Sprintf("%d", event.Timestamp.UnixNano())

			// JSON encode the full event as the log line
			logLine, err := json.Marshal(event)
			if err != nil {
				continue
			}

			stream.Values = append(stream.Values, []string{tsNanos, string(logLine)})
		}

		lokiReq.Streams = append(lokiReq.Streams, stream)
	}

	// Send to Loki
	body, err := json.Marshal(lokiReq)
	if err != nil {
		return fmt.Errorf("failed to marshal Loki request: %w", err)
	}

	req, err := http.NewRequestWithContext(o.ctx, "POST", o.lokiEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Loki returned status %d", resp.StatusCode)
	}

	log.Printf("[Observability] Pushed %d events to Loki", len(events))
	return nil
}

// GetBufferSize returns current buffer size (for monitoring)
func (o *LBObservability) GetBufferSize() int {
	o.bufferMu.Lock()
	defer o.bufferMu.Unlock()
	return len(o.buffer)
}

// SetEnabled enables/disables observability at runtime
func (o *LBObservability) SetEnabled(enabled bool) {
	o.enabled = enabled
	if enabled && o.ctx.Err() != nil {
		// Restart if was stopped
		o.ctx, o.cancel = context.WithCancel(context.Background())
		o.Start()
	}
}

// SetLokiEndpoint changes the Loki endpoint at runtime
func (o *LBObservability) SetLokiEndpoint(endpoint string) {
	o.lokiEndpoint = endpoint
}
