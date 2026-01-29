/*
Copyright © 2025 ALESSIO TONIOLO

constants.go defines all configuration constants for the gotoni load balancer system.
Update these values to change default behavior across all components.
*/
package config

import "time"

// =============================================================================
// LOAD BALANCER CONFIGURATION
// =============================================================================

// Port Configuration
const (
	// DefaultLoadBalancerPort is the port the LB listens on for incoming requests
	DefaultLoadBalancerPort = 8000

	// DefaultApplicationPort is the port of the local backend (SGLang)
	DefaultApplicationPort = 8080
)

const MaxHops = 1

// Capacity & Request Handling
const (
	// DefaultMaxConcurrentRequests is the max requests before forwarding to peers
	// This is used when RunningReqsThreshold is 0
	DefaultMaxConcurrentRequests = 10

	// DefaultRunningReqsThreshold forces forwarding when running_reqs >= this value
	// This is the recommended way to control load distribution (overrides IE queue indicator)
	// Set to 0 to disable and use IE queue indicator instead
	DefaultRunningReqsThreshold = 5

	// DefaultMaxQueueSize is the max requests that can be queued before rejecting
	// This should rarely be hit if peers are configured correctly
	DefaultMaxQueueSize = 1000
)

// Timeouts
const (
	// DefaultRequestTimeout is the timeout for forwarded requests to peers
	DefaultRequestTimeout = 30 * time.Second

	// DefaultQueueTimeout is how long a request can wait in queue before timing out
	DefaultQueueTimeout = 30 * time.Second
)

// Queue Processing
const (
	// DefaultQueueEnabled controls whether requests are queued when all nodes are at capacity
	DefaultQueueEnabled = true

	// DefaultQueueProbeInterval is how often the queue processor checks for capacity
	DefaultQueueProbeInterval = 20 * time.Millisecond
)

// Load Balancing Strategy
const (
	// DefaultStrategy is the load balancing algorithm to use
	// Options: "gorgo", "gorgo2", "least-loaded", "prefix-tree"
	DefaultStrategy = "gorgo"

	// DefaultUseIEQueueIndicator controls whether to use SGLang's internal queue
	// as the capacity indicator. When true, forwards when NumWaitingReqs > 0.
	// Note: RunningReqsThreshold > 0 overrides this setting.
	DefaultUseIEQueueIndicator = true
)

// GORGO/GORGO2 Policy Tuning Parameters
const (
	// DefaultGORGOMsPerToken is the estimated prefill time per token in milliseconds
	// This represents cold-start prefill rate on 8xA100 with Mistral-7B
	// Adjust based on your hardware and model
	DefaultGORGOMsPerToken = 0.094

	// DefaultGORGO2RunningCostFactor is the weight applied to running requests
	// Running requests cost less because they're already partially processed
	// Range: 0.0 to 1.0 (lower = running requests weighted less)
	DefaultGORGO2RunningCostFactor = 0.5
)

// =============================================================================
// SGLANG METRICS POLLING
// =============================================================================

const (
	// DefaultMetricsEnabled controls whether to poll SGLang metrics
	DefaultMetricsEnabled = true

	// DefaultMetricsPollInterval is how often to poll metrics from local and peer backends
	DefaultMetricsPollInterval = 1 * time.Second

	// DefaultMetricsEndpoint is the SGLang metrics endpoint (requires --enable-metrics)
	DefaultMetricsEndpoint = "/metrics"

	// DefaultMetricsTimeout is the timeout for metrics requests
	DefaultMetricsTimeout = 500 * time.Millisecond

	// DefaultUnhealthyThreshold is consecutive poll failures before marking a node unhealthy
	DefaultUnhealthyThreshold = 3

	// DefaultGPUCacheThreshold marks a node as "at capacity" when GPU cache exceeds this
	DefaultGPUCacheThreshold = 0.95
)

// =============================================================================
// SGLANG SERVER CONFIGURATION
// =============================================================================

const (
	// DefaultSGLangPort is the port SGLang listens on
	DefaultSGLangPort = 8080

	// DefaultSGLangModel is the default model to serve
	DefaultSGLangModel = "mistralai/Mistral-7B-Instruct-v0.3"

	// DefaultSGLangMaxRunningRequests limits concurrent requests in SGLang's GPU batch
	// Set this low to force requests to queue, triggering load balancer forwarding
	DefaultSGLangMaxRunningRequests = 10

	// DefaultSGLangDockerImage is the Docker image for SGLang
	DefaultSGLangDockerImage = "lmsysorg/sglang:latest"

	// DefaultSGLangContainerName is the Docker container name
	DefaultSGLangContainerName = "sglang-server"
)

// =============================================================================
// DEPLOYMENT CONFIGURATION
// =============================================================================

const (
	// DefaultGotoniRemotePath is where the gotoni binary is stored on remote instances
	DefaultGotoniRemotePath = "/home/ubuntu/gotoni"

	// DefaultTmuxSessionName is the tmux session name for the load balancer process
	DefaultTmuxSessionName = "gotoni-lb"

	// DefaultUlimitFileDescriptors is the file descriptor limit for the LB process
	// High value needed for many concurrent connections
	DefaultUlimitFileDescriptors = 65535
)

// =============================================================================
// TRACING CONFIGURATION
// =============================================================================

const (
	// DefaultTraceCollectionTimeout is how long to wait when collecting traces
	DefaultTraceCollectionTimeout = 120 * time.Second
)

// =============================================================================
// HTTP CLIENT CONFIGURATION
// =============================================================================

const (
	// DefaultMaxIdleConnsPerHost for the HTTP client connection pool
	DefaultMaxIdleConnsPerHost = 100

	// DefaultIdleConnTimeout for the HTTP client
	DefaultIdleConnTimeout = 90 * time.Second
)
