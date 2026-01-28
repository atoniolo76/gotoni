package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atoniolo76/gotoni/pkg/db"
)

// ---------------------------------------------------------------------------
// Data structures for test reports
// ---------------------------------------------------------------------------

// MetricsSnapshot captures a single poll of SGLang metrics for one instance.
type MetricsSnapshot struct {
	Timestamp  time.Time     `json:"timestamp"`
	InstanceIP string        `json:"instance_ip"`
	Metrics    SGLangMetrics `json:"metrics"`
	Source     string        `json:"source"` // "sglang" or "lb"
}

// LBMetricsSnapshot captures a single poll of load balancer metrics.
type LBMetricsSnapshot struct {
	Timestamp  time.Time              `json:"timestamp"`
	InstanceIP string                 `json:"instance_ip"`
	Data       map[string]interface{} `json:"data"`
}

// RequestResult records the outcome of a single inference request.
type RequestResult struct {
	RequestID  int           `json:"request_id"`
	StartTime  time.Time     `json:"start_time"`
	DurationMs float64       `json:"duration_ms"`
	StatusCode int           `json:"status_code"`
	ServedBy   string        `json:"served_by,omitempty"` // from X-Served-By header
	Error      string        `json:"error,omitempty"`
}

// TestReport is the top-level JSON artifact written at the end of each test.
type TestReport struct {
	TestName        string              `json:"test_name"`
	StartTime       time.Time           `json:"start_time"`
	EndTime         time.Time           `json:"end_time"`
	InstanceIPs     []string            `json:"instance_ips"`
	MetricsConfig   MetricsConfig       `json:"metrics_config"`
	SGLangSnapshots []MetricsSnapshot   `json:"sglang_snapshots,omitempty"`
	LBSnapshots     []LBMetricsSnapshot `json:"lb_snapshots,omitempty"`
	Requests        []RequestResult     `json:"requests,omitempty"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// getClusterInstanceIPs queries the local SQLite database for the IPs of all
// instances belonging to the "sglang-auto-cluster" cluster.
func getClusterInstanceIPs(t *testing.T) []string {
	t.Helper()

	database, err := db.InitDB()
	if err != nil {
		t.Fatalf("Failed to initialize database: %v", err)
	}

	clusterName := "sglang-auto-cluster"
	dbCluster, err := database.GetCluster(clusterName)
	if err != nil {
		t.Fatalf("Failed to find cluster %q in database: %v (run TestClusterSGLang first)", clusterName, err)
	}

	instanceIDs, err := database.GetClusterInstances(dbCluster.ID)
	if err != nil {
		t.Fatalf("Failed to get cluster instances: %v", err)
	}
	if len(instanceIDs) == 0 {
		t.Fatalf("Cluster %q has no instances in the database", clusterName)
	}

	var ips []string
	for _, id := range instanceIDs {
		inst, err := database.GetInstance(id)
		if err != nil {
			t.Logf("Warning: could not fetch instance %s: %v", id, err)
			continue
		}
		if inst.IPAddress != "" {
			ips = append(ips, inst.IPAddress)
		}
	}

	if len(ips) == 0 {
		t.Fatalf("No instances with IP addresses found for cluster %q", clusterName)
	}

	t.Logf("Discovered %d instance(s): %v", len(ips), ips)
	return ips
}

// fetchSGLangMetricsHTTP fetches metrics from a single SGLang server via HTTP.
// This is a standalone helper (not tied to the LoadBalancer receiver).
func fetchSGLangMetricsHTTP(ip string, port int) (*SGLangMetrics, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	metricsURL := fmt.Sprintf("http://%s:%d/get_server_info", ip, port)

	start := time.Now()
	resp, err := client.Get(metricsURL)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", metricsURL, err)
	}
	defer resp.Body.Close()
	latency := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, metricsURL)
	}

	var info SGLangServerInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode error from %s: %w", metricsURL, err)
	}

	return &SGLangMetrics{
		NumRunningReqs:   info.NumRunningReqs,
		NumWaitingReqs:   info.NumWaitingReqs,
		NumTotalReqs:     info.NumRunningReqs + info.NumWaitingReqs,
		GPUCacheUsage:    info.GPUCacheUsage,
		MaxRunningReqs:   info.MaxRunningReqs,
		MaxTotalTokens:   info.MaxTotalTokens,
		LastUpdated:      time.Now(),
		Healthy:          true,
		ConsecutiveFails: 0,
		Latency:          latency.Milliseconds(),
	}, nil
}

// fetchLBMetrics fetches the /lb/metrics JSON from a load balancer instance.
func fetchLBMetrics(ip string, port int) (map[string]interface{}, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://%s:%d/lb/metrics", ip, port)

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode error from %s: %w", url, err)
	}
	return data, nil
}

// sendChatCompletions sends numRequests concurrent chat completion requests to
// targetURL. Concurrency is bounded by a semaphore (maxConcurrency).
// Returns a slice of RequestResult recording each request's outcome.
func sendChatCompletions(t *testing.T, targetURL string, numRequests int, maxConcurrency int) []RequestResult {
	t.Helper()

	if maxConcurrency <= 0 {
		maxConcurrency = 10
	}

	sem := make(chan struct{}, maxConcurrency)
	results := make([]RequestResult, numRequests)

	client := &http.Client{Timeout: 120 * time.Second}
	payload := `{"model":"mistralai/Mistral-7B-Instruct-v0.3","messages":[{"role":"user","content":"What is 2+2? Reply in one word."}],"max_tokens":32}`

	var wg sync.WaitGroup
	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			req, err := http.NewRequest("POST", targetURL, strings.NewReader(payload))
			if err != nil {
				results[idx] = RequestResult{RequestID: idx, Error: err.Error()}
				return
			}
			req.Header.Set("Content-Type", "application/json")

			start := time.Now()
			resp, err := client.Do(req)
			dur := time.Since(start)

			res := RequestResult{
				RequestID:  idx,
				StartTime:  start,
				DurationMs: float64(dur.Milliseconds()),
			}

			if err != nil {
				res.Error = err.Error()
				results[idx] = res
				return
			}
			defer resp.Body.Close()
			// Drain body so the connection can be reused.
			io.ReadAll(resp.Body)

			res.StatusCode = resp.StatusCode
			res.ServedBy = resp.Header.Get("X-Served-By")
			results[idx] = res
		}(i)
	}

	wg.Wait()
	return results
}

// startMetricsPoller launches goroutines that poll SGLang /get_server_info at
// the given frequency. It returns a function to stop polling and the collected
// snapshots.
func startMetricsPoller(ctx context.Context, ips []string, port int, interval time.Duration) (stop func(), snapshots func() []MetricsSnapshot) {
	ctx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	var collected []MetricsSnapshot

	var wg sync.WaitGroup
	for _, ip := range ips {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					m, err := fetchSGLangMetricsHTTP(ip, port)
					if err != nil {
						continue
					}
					snap := MetricsSnapshot{
						Timestamp:  time.Now(),
						InstanceIP: ip,
						Metrics:    *m,
						Source:     "sglang",
					}
					mu.Lock()
					collected = append(collected, snap)
					mu.Unlock()
				}
			}
		}(ip)
	}

	stopFn := func() {
		cancel()
		wg.Wait()
	}

	snapshotsFn := func() []MetricsSnapshot {
		mu.Lock()
		defer mu.Unlock()
		out := make([]MetricsSnapshot, len(collected))
		copy(out, collected)
		return out
	}

	return stopFn, snapshotsFn
}

// startLBMetricsPoller is like startMetricsPoller but polls /lb/metrics.
func startLBMetricsPoller(ctx context.Context, ips []string, port int, interval time.Duration) (stop func(), snapshots func() []LBMetricsSnapshot) {
	ctx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	var collected []LBMetricsSnapshot

	var wg sync.WaitGroup
	for _, ip := range ips {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					data, err := fetchLBMetrics(ip, port)
					if err != nil {
						continue
					}
					snap := LBMetricsSnapshot{
						Timestamp:  time.Now(),
						InstanceIP: ip,
						Data:       data,
					}
					mu.Lock()
					collected = append(collected, snap)
					mu.Unlock()
				}
			}
		}(ip)
	}

	stopFn := func() {
		cancel()
		wg.Wait()
	}

	snapshotsFn := func() []LBMetricsSnapshot {
		mu.Lock()
		defer mu.Unlock()
		out := make([]LBMetricsSnapshot, len(collected))
		copy(out, collected)
		return out
	}

	return stopFn, snapshotsFn
}

// writeReport marshals a TestReport to a timestamped JSON file and logs the path.
func writeReport(t *testing.T, report *TestReport) {
	t.Helper()
	fname := fmt.Sprintf("sglang_metrics_%s_%d.json", report.TestName, time.Now().Unix())
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Errorf("Failed to marshal report: %v", err)
		return
	}
	if err := os.WriteFile(fname, data, 0644); err != nil {
		t.Errorf("Failed to write report to %s: %v", fname, err)
		return
	}
	t.Logf("Report written to %s (%d bytes)", fname, len(data))
}

// printLatencyStats computes and prints min/max/mean/p50/p95 from durations.
func printLatencyStats(t *testing.T, results []RequestResult) {
	t.Helper()
	var durations []float64
	var errors int
	for _, r := range results {
		if r.Error != "" {
			errors++
			continue
		}
		durations = append(durations, r.DurationMs)
	}
	if len(durations) == 0 {
		t.Logf("  No successful requests to compute latency stats")
		return
	}
	sort.Float64s(durations)
	sum := 0.0
	for _, d := range durations {
		sum += d
	}
	mean := sum / float64(len(durations))
	p50 := durations[len(durations)/2]
	p95idx := int(math.Ceil(float64(len(durations))*0.95)) - 1
	if p95idx < 0 {
		p95idx = 0
	}
	p95 := durations[p95idx]

	t.Logf("  Requests: %d success, %d errors", len(durations), errors)
	t.Logf("  Latency (ms): min=%.0f  max=%.0f  mean=%.0f  p50=%.0f  p95=%.0f",
		durations[0], durations[len(durations)-1], mean, p50, p95)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestSGLangMetricsPolling discovers SGLang instances from the database and
// continuously polls each server's metrics at 5Hz, printing them live and
// saving all snapshots to a timestamped JSON file.
func TestSGLangMetricsPolling(t *testing.T) {
	ips := getClusterInstanceIPs(t)

	// Print MetricsConfig once.
	cfg := DefaultMetricsConfig()
	cfgJSON, _ := json.MarshalIndent(cfg, "", "  ")
	t.Logf("=== MetricsConfig ===\n%s", string(cfgJSON))

	// Duration is configurable via METRICS_DURATION_SECS (default 30).
	durationSecs := 30
	if v := os.Getenv("METRICS_DURATION_SECS"); v != "" {
		if d, err := strconv.Atoi(v); err == nil && d > 0 {
			durationSecs = d
		}
	}
	duration := time.Duration(durationSecs) * time.Second
	t.Logf("Polling SGLang metrics at 5Hz for %s across %d instance(s)...", duration, len(ips))

	ctx := context.Background()
	stopPoller, getSnapshots := startMetricsPoller(ctx, ips, 8080, 200*time.Millisecond)

	// Also print live in a separate goroutine (1Hz summary so we don't flood output).
	printCtx, printCancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-printCtx.Done():
				return
			case <-ticker.C:
				for _, ip := range ips {
					m, err := fetchSGLangMetricsHTTP(ip, 8080)
					if err != nil {
						t.Logf("[%s] %s: ERROR %v", time.Now().Format("15:04:05.000"), ip, err)
						continue
					}
					t.Logf("[%s] %s: running=%d waiting=%d total=%d gpu_cache=%.3f max_running=%d latency=%dms",
						time.Now().Format("15:04:05.000"), ip,
						m.NumRunningReqs, m.NumWaitingReqs, m.NumTotalReqs,
						m.GPUCacheUsage, m.MaxRunningReqs, m.Latency)
				}
			}
		}
	}()

	time.Sleep(duration)

	printCancel()
	stopPoller()

	snapshots := getSnapshots()
	t.Logf("Collected %d total metric snapshots", len(snapshots))

	report := &TestReport{
		TestName:        "metrics_polling",
		StartTime:       time.Now().Add(-duration),
		EndTime:         time.Now(),
		InstanceIPs:     ips,
		MetricsConfig:   cfg,
		SGLangSnapshots: snapshots,
	}
	writeReport(t, report)
}

// TestSGLangMetricsUnderLoad sends inference requests to each SGLang server
// while simultaneously polling metrics at 5Hz, so you can observe how
// running/waiting request counts change under load.
func TestSGLangMetricsUnderLoad(t *testing.T) {
	ips := getClusterInstanceIPs(t)

	cfg := DefaultMetricsConfig()
	cfgJSON, _ := json.MarshalIndent(cfg, "", "  ")
	t.Logf("=== MetricsConfig ===\n%s", string(cfgJSON))

	numRequests := 20
	if v := os.Getenv("NUM_REQUESTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			numRequests = n
		}
	}

	t.Logf("Sending %d requests to each of %d SGLang server(s) while polling metrics at 5Hz", numRequests, len(ips))

	// Start background metrics poller.
	ctx := context.Background()
	stopPoller, getSnapshots := startMetricsPoller(ctx, ips, 8080, 200*time.Millisecond)

	// Also print a live summary at 1Hz.
	printCtx, printCancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-printCtx.Done():
				return
			case <-ticker.C:
				for _, ip := range ips {
					m, err := fetchSGLangMetricsHTTP(ip, 8080)
					if err != nil {
						continue
					}
					t.Logf("[%s] %s: running=%d waiting=%d total=%d gpu_cache=%.3f",
						time.Now().Format("15:04:05.000"), ip,
						m.NumRunningReqs, m.NumWaitingReqs, m.NumTotalReqs,
						m.GPUCacheUsage)
				}
			}
		}
	}()

	// Send requests to each instance in parallel.
	var allResults []RequestResult
	var resultsMu sync.Mutex
	var wg sync.WaitGroup

	for _, ip := range ips {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			url := fmt.Sprintf("http://%s:8080/v1/chat/completions", ip)
			t.Logf("Sending %d requests to %s", numRequests, url)
			results := sendChatCompletions(t, url, numRequests, 10)
			resultsMu.Lock()
			allResults = append(allResults, results...)
			resultsMu.Unlock()
		}(ip)
	}
	wg.Wait()

	printCancel()
	stopPoller()

	// Print per-instance summary.
	t.Logf("=== Request Results ===")
	byIP := make(map[string][]RequestResult)
	for _, r := range allResults {
		// Parse IP from the request (we tagged nothing here, but we know order).
		// Instead, just aggregate all.
		byIP["all"] = append(byIP["all"], r)
	}
	printLatencyStats(t, allResults)

	snapshots := getSnapshots()
	t.Logf("Collected %d metric snapshots during load test", len(snapshots))

	report := &TestReport{
		TestName:        "metrics_under_load",
		StartTime:       time.Now(),
		EndTime:         time.Now(),
		InstanceIPs:     ips,
		MetricsConfig:   cfg,
		SGLangSnapshots: snapshots,
		Requests:        allResults,
	}
	writeReport(t, report)
}

// TestLoadBalancerObservability sends 100 concurrent requests to the first
// load balancer and observes:
//   - Which peer served each request (X-Served-By header)
//   - How LB metrics change over time (/lb/metrics polled at 5Hz)
//   - When the LB starts forwarding to peers (local at capacity)
func TestLoadBalancerObservability(t *testing.T) {
	ips := getClusterInstanceIPs(t)
	lbPort := 8000
	sglangPort := 8080

	cfg := DefaultMetricsConfig()
	cfgJSON, _ := json.MarshalIndent(cfg, "", "  ")
	t.Logf("=== MetricsConfig ===\n%s", string(cfgJSON))

	numRequests := 100
	if v := os.Getenv("NUM_REQUESTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			numRequests = n
		}
	}

	targetLB := ips[0]
	targetURL := fmt.Sprintf("http://%s:%d/v1/chat/completions", targetLB, lbPort)
	t.Logf("Target LB: %s (port %d)", targetLB, lbPort)
	t.Logf("Sending %d concurrent requests to %s", numRequests, targetURL)
	t.Logf("Polling /lb/metrics on all %d instances at 5Hz", len(ips))
	t.Logf("Polling /get_server_info on all %d instances at 5Hz", len(ips))

	ctx := context.Background()

	// Start LB metrics poller.
	stopLBPoller, getLBSnapshots := startLBMetricsPoller(ctx, ips, lbPort, 200*time.Millisecond)

	// Start SGLang metrics poller (to see running/waiting counts on servers).
	stopSGPoller, getSGSnapshots := startMetricsPoller(ctx, ips, sglangPort, 200*time.Millisecond)

	// Print live LB summary at 1Hz.
	printCtx, printCancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-printCtx.Done():
				return
			case <-ticker.C:
				for _, ip := range ips {
					data, err := fetchLBMetrics(ip, lbPort)
					if err != nil {
						continue
					}
					local, _ := data["local"].(map[string]interface{})
					if local == nil {
						continue
					}
					t.Logf("[%s] LB %s: local_running=%.0f local_waiting=%.0f local_total=%.0f gpu_cache=%.3f",
						time.Now().Format("15:04:05.000"), ip,
						local["running_reqs"], local["waiting_reqs"], local["total_reqs"],
						local["gpu_cache"])

					// Print peer info.
					peers, _ := data["peers"].(map[string]interface{})
					for peerID, peerData := range peers {
						pd, _ := peerData.(map[string]interface{})
						if pd == nil {
							continue
						}
						t.Logf("           peer %s: running=%.0f waiting=%.0f total=%.0f available=%v healthy=%v latency=%.0fms",
							peerID[:16],
							pd["running_reqs"], pd["waiting_reqs"], pd["total_reqs"],
							pd["available"], pd["healthy"], pd["latency_ms"])
					}
				}
			}
		}
	}()

	// Allow an initial metrics snapshot before we start.
	time.Sleep(1 * time.Second)

	// Fire the requests.
	t.Logf("=== Sending %d requests NOW ===", numRequests)
	results := sendChatCompletions(t, targetURL, numRequests, 20)

	// Let metrics settle after requests complete.
	time.Sleep(3 * time.Second)

	printCancel()
	stopLBPoller()
	stopSGPoller()

	// ---------------------------------------------------------------------------
	// Analyze results
	// ---------------------------------------------------------------------------

	t.Logf("=== Request Distribution ===")

	// Count how many requests each peer served.
	servedByCount := make(map[string]int)
	localCount := 0
	var errors int
	for _, r := range results {
		if r.Error != "" {
			errors++
			continue
		}
		if r.ServedBy == "" {
			localCount++
			servedByCount["(local)"]++
		} else {
			servedByCount[r.ServedBy]++
		}
	}

	t.Logf("  Served locally (no X-Served-By): %d", localCount)
	for peer, count := range servedByCount {
		if peer == "(local)" {
			continue
		}
		t.Logf("  Forwarded to peer %s: %d", peer, count)
	}
	if errors > 0 {
		t.Logf("  Errors: %d", errors)
	}

	t.Logf("")
	t.Logf("=== Latency Stats ===")
	printLatencyStats(t, results)

	// Print a timeline of forwarding decisions.
	t.Logf("")
	t.Logf("=== Forwarding Timeline (first 20 requests by start time) ===")
	sorted := make([]RequestResult, len(results))
	copy(sorted, results)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].StartTime.Before(sorted[j].StartTime)
	})
	limit := 20
	if len(sorted) < limit {
		limit = len(sorted)
	}
	for _, r := range sorted[:limit] {
		served := "(local)"
		if r.ServedBy != "" {
			served = fmt.Sprintf("peer:%s", r.ServedBy)
		}
		status := fmt.Sprintf("%d", r.StatusCode)
		if r.Error != "" {
			status = "ERR"
		}
		t.Logf("  [%s] req#%03d  status=%s  dur=%.0fms  served_by=%s",
			r.StartTime.Format("15:04:05.000"), r.RequestID, status, r.DurationMs, served)
	}

	// Collect and write report.
	lbSnaps := getLBSnapshots()
	sgSnaps := getSGSnapshots()
	t.Logf("Collected %d LB snapshots and %d SGLang snapshots", len(lbSnaps), len(sgSnaps))

	report := &TestReport{
		TestName:        "lb_observability",
		StartTime:       time.Now(),
		EndTime:         time.Now(),
		InstanceIPs:     ips,
		MetricsConfig:   cfg,
		SGLangSnapshots: sgSnaps,
		LBSnapshots:     lbSnaps,
		Requests:        results,
	}
	writeReport(t, report)
}
