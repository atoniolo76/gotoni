/*
Copyright © 2025 ALESSIO TONIOLO

trace.go provides event-driven tracing for the load balancer.

Design principles (inspired by PyTorch profiler):
- NOT constantly running - only active when explicitly started
- Event-driven with hooks inserted at key points
- Produces Perfetto-compatible traces (Chrome Trace Format)
- Supports distributed tracing across nodes via header propagation
- Minimal overhead when disabled

Usage:
 1. Start trace via endpoint: POST /lb/trace/start
 2. Run workload (benchmark)
 3. Stop trace via endpoint: POST /lb/trace/stop
 4. Returns Perfetto JSON that can be loaded in chrome://tracing or ui.perfetto.dev
*/
package serve

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// TraceEvent represents a single event in Chrome Trace Format (Perfetto-compatible)
// See: https://docs.google.com/document/d/1CvAClvFfyA5R-PhYUmn5OOQtYMH4h6I0nSsKchNAySU
type TraceEvent struct {
	Name      string                 `json:"name"`           // Event name
	Cat       string                 `json:"cat"`            // Category (e.g., "lb", "forward", "queue")
	Ph        string                 `json:"ph"`             // Phase: B=begin, E=end, X=complete, i=instant
	Ts        int64                  `json:"ts"`             // Timestamp in microseconds
	Dur       int64                  `json:"dur,omitempty"`  // Duration in microseconds (for ph=X)
	Pid       int                    `json:"pid"`            // Process ID (node ID hash)
	Tid       int                    `json:"tid"`            // Thread ID (request ID hash)
	Args      map[string]interface{} `json:"args,omitempty"` // Additional metadata
	ID        string                 `json:"id,omitempty"`   // For async events (flow)
	Scope     string                 `json:"s,omitempty"`    // Scope for flow events
	BindingPt string                 `json:"bp,omitempty"`   // Binding point for flow events
}

// TraceSession manages an active tracing session
type TraceSession struct {
	ID        string
	StartTime time.Time
	NodeID    string

	events   []TraceEvent
	eventsMu sync.Mutex

	// Atomic flag for fast "is tracing active" check
	active atomic.Bool

	// For distributed tracing - events received from other nodes
	remoteEvents   []TraceEvent
	remoteEventsMu sync.Mutex
}

// Tracer is the global tracer instance
type Tracer struct {
	session   *TraceSession
	sessionMu sync.RWMutex
	nodeID    string
	nodePID   int // Hash of node ID for Perfetto PID
}

// NewTracer creates a new tracer instance
func NewTracer(nodeID string) *Tracer {
	return &Tracer{
		nodeID:  nodeID,
		nodePID: hashString(nodeID) % 10000, // Keep PID small for readability
	}
}

// IsActive returns true if tracing is currently active (fast path)
func (t *Tracer) IsActive() bool {
	t.sessionMu.RLock()
	defer t.sessionMu.RUnlock()
	return t.session != nil && t.session.active.Load()
}

// Start begins a new tracing session
func (t *Tracer) Start() string {
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()

	sessionID := fmt.Sprintf("trace-%d", time.Now().UnixNano())
	t.session = &TraceSession{
		ID:        sessionID,
		StartTime: time.Now(),
		NodeID:    t.nodeID,
		events:    make([]TraceEvent, 0, 1000),
	}
	t.session.active.Store(true)

	// Record trace start event
	t.session.events = append(t.session.events, TraceEvent{
		Name: "trace_session",
		Cat:  "trace",
		Ph:   "B",
		Ts:   0, // Relative to trace start
		Pid:  t.nodePID,
		Tid:  0,
		Args: map[string]interface{}{
			"session_id": sessionID,
			"node_id":    t.nodeID,
		},
	})

	return sessionID
}

// Stop ends the tracing session and returns the Perfetto JSON
func (t *Tracer) Stop() ([]byte, error) {
	t.sessionMu.Lock()
	defer t.sessionMu.Unlock()

	if t.session == nil {
		return nil, fmt.Errorf("no active trace session")
	}

	t.session.active.Store(false)

	// Record trace end event
	endTs := time.Since(t.session.StartTime).Microseconds()
	t.session.events = append(t.session.events, TraceEvent{
		Name: "trace_session",
		Cat:  "trace",
		Ph:   "E",
		Ts:   endTs,
		Pid:  t.nodePID,
		Tid:  0,
	})

	// Merge remote events
	t.session.eventsMu.Lock()
	t.session.remoteEventsMu.Lock()
	allEvents := make([]TraceEvent, 0, len(t.session.events)+len(t.session.remoteEvents))
	allEvents = append(allEvents, t.session.events...)
	allEvents = append(allEvents, t.session.remoteEvents...)
	t.session.remoteEventsMu.Unlock()
	t.session.eventsMu.Unlock()

	// Build Perfetto trace - just the events array for direct compatibility
	// Perfetto/Chrome Trace Viewer expects a plain array or {"traceEvents": [...]}
	// We output just the array for maximum compatibility

	// Add metadata as a special "M" (metadata) event at the start
	metadataEvent := TraceEvent{
		Name: "process_name",
		Cat:  "__metadata",
		Ph:   "M",
		Ts:   0,
		Pid:  t.nodePID,
		Tid:  0,
		Args: map[string]interface{}{
			"name": t.nodeID,
		},
	}

	// Prepend metadata event
	finalEvents := make([]TraceEvent, 0, len(allEvents)+1)
	finalEvents = append(finalEvents, metadataEvent)
	finalEvents = append(finalEvents, allEvents...)

	// Clear session
	t.session = nil

	return json.MarshalIndent(finalEvents, "", "  ")
}

// GetStatus returns current trace status
func (t *Tracer) GetStatus() map[string]interface{} {
	t.sessionMu.RLock()
	defer t.sessionMu.RUnlock()

	if t.session == nil {
		return map[string]interface{}{
			"active":  false,
			"node_id": t.nodeID,
		}
	}

	t.session.eventsMu.Lock()
	eventCount := len(t.session.events)
	t.session.eventsMu.Unlock()

	return map[string]interface{}{
		"active":      t.session.active.Load(),
		"session_id":  t.session.ID,
		"node_id":     t.nodeID,
		"duration_ms": float64(time.Since(t.session.StartTime).Microseconds()) / 1000.0,
		"event_count": eventCount,
		"start_time":  t.session.StartTime.Format(time.RFC3339),
	}
}

// =====================================================
// TRACE HOOKS - Insert these at key points in code
// =====================================================

// TraceRequestReceived records when a request arrives at the LB
func (t *Tracer) TraceRequestReceived(requestID string, method, path, remoteAddr string) {
	if !t.IsActive() {
		return
	}

	t.recordEvent(TraceEvent{
		Name: "request",
		Cat:  "lb",
		Ph:   "B", // Begin
		Tid:  hashString(requestID) % 1000,
		Args: map[string]interface{}{
			"request_id":  requestID,
			"method":      method,
			"path":        path,
			"remote_addr": remoteAddr,
		},
	})
}

// TraceRequestComplete records when a request finishes
func (t *Tracer) TraceRequestComplete(requestID string, statusCode int, servedBy string) {
	if !t.IsActive() {
		return
	}

	t.recordEvent(TraceEvent{
		Name: "request",
		Cat:  "lb",
		Ph:   "E", // End
		Tid:  hashString(requestID) % 1000,
		Args: map[string]interface{}{
			"request_id":  requestID,
			"status_code": statusCode,
			"served_by":   servedBy,
		},
	})
}

// TraceQueueEnter records when a request enters the queue
func (t *Tracer) TraceQueueEnter(requestID string, queueDepth int) {
	if !t.IsActive() {
		return
	}

	t.recordEvent(TraceEvent{
		Name: "queue",
		Cat:  "queue",
		Ph:   "B",
		Tid:  hashString(requestID) % 1000,
		Args: map[string]interface{}{
			"request_id":  requestID,
			"queue_depth": queueDepth,
		},
	})
}

// TraceQueueExit records when a request leaves the queue
func (t *Tracer) TraceQueueExit(requestID string, waitTimeMs float64) {
	if !t.IsActive() {
		return
	}

	t.recordEvent(TraceEvent{
		Name: "queue",
		Cat:  "queue",
		Ph:   "E",
		Tid:  hashString(requestID) % 1000,
		Args: map[string]interface{}{
			"request_id":   requestID,
			"wait_time_ms": waitTimeMs,
		},
	})
}

// TraceForwardStart records when we start forwarding to a peer
// Returns a flow ID for stitching with remote trace
func (t *Tracer) TraceForwardStart(requestID, targetNode, targetIP string) string {
	if !t.IsActive() {
		return ""
	}

	flowID := fmt.Sprintf("%s-%s", requestID, targetNode)

	// Record the forward span start
	t.recordEvent(TraceEvent{
		Name: "forward_to_peer",
		Cat:  "forward",
		Ph:   "B",
		Tid:  hashString(requestID) % 1000,
		Args: map[string]interface{}{
			"request_id":  requestID,
			"target_node": targetNode,
			"target_ip":   targetIP,
			"flow_id":     flowID,
		},
	})

	// Record flow event (arrow in Perfetto)
	t.recordEvent(TraceEvent{
		Name:  "forward_flow",
		Cat:   "forward",
		Ph:    "s", // Flow start
		ID:    flowID,
		Tid:   hashString(requestID) % 1000,
		Scope: "forward",
	})

	return flowID
}

// TraceForwardEnd records when forwarding completes
func (t *Tracer) TraceForwardEnd(requestID string, statusCode int, durationMs float64) {
	if !t.IsActive() {
		return
	}

	t.recordEvent(TraceEvent{
		Name: "forward_to_peer",
		Cat:  "forward",
		Ph:   "E",
		Tid:  hashString(requestID) % 1000,
		Args: map[string]interface{}{
			"request_id":  requestID,
			"status_code": statusCode,
			"duration_ms": durationMs,
		},
	})
}

// TraceLocalProcess records local SGLang processing
func (t *Tracer) TraceLocalProcessStart(requestID string) {
	if !t.IsActive() {
		return
	}

	t.recordEvent(TraceEvent{
		Name: "local_sglang",
		Cat:  "process",
		Ph:   "B",
		Tid:  hashString(requestID) % 1000,
		Args: map[string]interface{}{
			"request_id": requestID,
		},
	})
}

func (t *Tracer) TraceLocalProcessEnd(requestID string, statusCode int) {
	if !t.IsActive() {
		return
	}

	t.recordEvent(TraceEvent{
		Name: "local_sglang",
		Cat:  "process",
		Ph:   "E",
		Tid:  hashString(requestID) % 1000,
		Args: map[string]interface{}{
			"request_id":  requestID,
			"status_code": statusCode,
		},
	})
}

// TracePolicyDecision records a policy decision (instant event)
func (t *Tracer) TracePolicyDecision(requestID, policy, decision string, metadata map[string]interface{}) {
	if !t.IsActive() {
		return
	}

	args := map[string]interface{}{
		"request_id": requestID,
		"policy":     policy,
		"decision":   decision,
	}
	for k, v := range metadata {
		args[k] = v
	}

	t.recordEvent(TraceEvent{
		Name: "policy_decision",
		Cat:  "policy",
		Ph:   "i", // Instant event
		Tid:  hashString(requestID) % 1000,
		Args: args,
	})
}

// TraceCapacityCheck records capacity check result
func (t *Tracer) TraceCapacityCheck(requestID string, hasCapacity bool, runningReqs, waitingReqs int) {
	if !t.IsActive() {
		return
	}

	t.recordEvent(TraceEvent{
		Name: "capacity_check",
		Cat:  "lb",
		Ph:   "i",
		Tid:  hashString(requestID) % 1000,
		Args: map[string]interface{}{
			"request_id":   requestID,
			"has_capacity": hasCapacity,
			"running_reqs": runningReqs,
			"waiting_reqs": waitingReqs,
		},
	})
}

// TraceTokenization records how long tokenization took (critical for GORGO overhead)
func (t *Tracer) TraceTokenization(requestID string, textLen, tokenCount int, durationUs int64) {
	if !t.IsActive() {
		return
	}

	t.recordEvent(TraceEvent{
		Name: "tokenize",
		Cat:  "gorgo",
		Ph:   "X", // Complete event with duration
		Tid:  hashString(requestID) % 1000,
		Dur:  durationUs,
		Args: map[string]interface{}{
			"request_id":  requestID,
			"text_len":    textLen,
			"token_count": tokenCount,
			"duration_us": durationUs,
		},
	})
}

// TraceGORGODecision records GORGO's routing decision with cost calculation
func (t *Tracer) TraceGORGODecision(requestID string, decision string, metadata map[string]interface{}) {
	if !t.IsActive() {
		return
	}

	args := map[string]interface{}{
		"request_id": requestID,
		"decision":   decision,
	}
	for k, v := range metadata {
		args[k] = v
	}

	t.recordEvent(TraceEvent{
		Name: "gorgo_decision",
		Cat:  "gorgo",
		Ph:   "i",
		Tid:  hashString(requestID) % 1000,
		Args: args,
	})
}

// =====================================================
// DISTRIBUTED TRACING - Receive events from other nodes
// =====================================================

// ReceiveRemoteEvents merges trace events from a forwarded request
func (t *Tracer) ReceiveRemoteEvents(events []TraceEvent) {
	if !t.IsActive() {
		return
	}

	t.sessionMu.RLock()
	session := t.session
	t.sessionMu.RUnlock()

	if session == nil {
		return
	}

	session.remoteEventsMu.Lock()
	session.remoteEvents = append(session.remoteEvents, events...)
	session.remoteEventsMu.Unlock()
}

// GetTraceHeaders returns headers to propagate trace context
func (t *Tracer) GetTraceHeaders(requestID string) map[string]string {
	if !t.IsActive() {
		return nil
	}

	t.sessionMu.RLock()
	defer t.sessionMu.RUnlock()

	if t.session == nil {
		return nil
	}

	return map[string]string{
		"X-Trace-ID":      t.session.ID,
		"X-Trace-Request": requestID,
		"X-Trace-Origin":  t.nodeID,
	}
}

// =====================================================
// INTERNAL HELPERS
// =====================================================

func (t *Tracer) recordEvent(event TraceEvent) {
	t.sessionMu.RLock()
	session := t.session
	t.sessionMu.RUnlock()

	if session == nil || !session.active.Load() {
		return
	}

	// Set timestamp relative to trace start
	event.Ts = time.Since(session.StartTime).Microseconds()
	event.Pid = t.nodePID

	session.eventsMu.Lock()
	session.events = append(session.events, event)
	session.eventsMu.Unlock()
}

// hashString creates a simple hash for consistent thread IDs
func hashString(s string) int {
	h := 0
	for _, c := range s {
		h = 31*h + int(c)
	}
	if h < 0 {
		h = -h
	}
	return h
}
