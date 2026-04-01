package proxy

import (
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// TEST HELPERS
// =============================================================================

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

func newTestProxy(policy string) *HttpProxy {
	cfg := DefaultProxyConfig()
	cfg.RoutingPolicyName = policy
	cfg.CapacityGatingEnabled = true
	cfg.MaxRunningPerServer = 50
	return NewHttpProxy(cfg)
}

func addTestServers(p *HttpProxy, n int) []*SGLangServer {
	servers := make([]*SGLangServer, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("server-%d", i)
		ip := fmt.Sprintf("10.0.0.%d", i+1)
		p.AddServer(id, ip, 8080)
	}
	p.serversMu.RLock()
	copy(servers, p.servers)
	p.serversMu.RUnlock()
	return servers
}

// simulateLoad adds fake running requests to a server.
func simulateLoad(server *SGLangServer, count int, tokensPerReq int) {
	server.runningRequestsMu.Lock()
	defer server.runningRequestsMu.Unlock()
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("req-%s-%d", server.ID, i)
		server.runningRequests[id] = &trackedRequest{
			RequestID:    id,
			TokenCount:   tokensPerReq,
			DispatchedAt: time.Now(),
			Server:       server,
		}
	}
}

func clearLoad(server *SGLangServer) {
	server.runningRequestsMu.Lock()
	defer server.runningRequestsMu.Unlock()
	server.runningRequests = make(map[string]*trackedRequest)
}

var sharedPrompts = []string{
	"You are a helpful assistant. Translate the following English text to French: 'The weather is beautiful today and I would like to go for a walk in the park.'",
	"You are a helpful assistant. Summarize the following article about climate change and its impact on global food production systems in developing countries.",
	"You are a code review assistant. Review the following Go function for potential race conditions and suggest improvements.",
	"You are a creative writing assistant. Write a short story about a robot discovering emotions for the first time in a post-apocalyptic world.",
	"You are a math tutor. Explain the concept of eigenvalues and eigenvectors with practical examples from physics and engineering applications.",
}

func randomPrompt(rng *rand.Rand) string {
	return sharedPrompts[rng.Intn(len(sharedPrompts))]
}

// =============================================================================
// UNIT TESTS — policy correctness
// =============================================================================

func TestLeastLoadSelectsMinimal(t *testing.T) {
	p := newTestProxy("least-load")
	servers := addTestServers(p, 3)

	simulateLoad(servers[0], 5, 100)
	simulateLoad(servers[1], 2, 100)
	simulateLoad(servers[2], 8, 100)

	selected, _ := p.selectServer("hello world", 10)
	if selected == nil {
		t.Fatal("expected a server, got nil")
	}
	if selected.ID != servers[1].ID {
		t.Errorf("expected server-1 (lowest load), got %s", selected.ID)
	}
}

func TestPrefixTreeFallsBackToLeastLoad(t *testing.T) {
	p := newTestProxy("prefix-tree")
	servers := addTestServers(p, 3)

	simulateLoad(servers[0], 5, 100)
	simulateLoad(servers[1], 1, 100)
	simulateLoad(servers[2], 3, 100)

	// No prefixes inserted — should fall back to least-load
	selected, _ := p.selectServer("brand new prompt with no cached prefix", 50)
	if selected == nil {
		t.Fatal("expected a server, got nil")
	}
	if selected.ID != servers[1].ID {
		t.Errorf("expected server-1 (least-load fallback), got %s", selected.ID)
	}
}

func TestPrefixTreeSelectsCachedServer(t *testing.T) {
	p := newTestProxy("prefix-tree")
	servers := addTestServers(p, 3)

	prompt := "You are a helpful assistant. Translate the following English text to French."
	p.insertPrefix(prompt, servers[2])

	// server-2 has the prefix cached, should be selected even with higher load
	simulateLoad(servers[0], 1, 100)
	simulateLoad(servers[1], 1, 100)
	simulateLoad(servers[2], 3, 100)

	selected, _ := p.selectServer(prompt+" Some extra text here.", 30)
	if selected == nil {
		t.Fatal("expected a server, got nil")
	}
	if selected.ID != servers[2].ID {
		t.Errorf("expected server-2 (prefix hit), got %s", selected.ID)
	}
}

func TestGORGOSelectsLowestCost(t *testing.T) {
	p := newTestProxy("gorgo")
	servers := addTestServers(p, 3)

	simulateLoad(servers[0], 10, 500)
	simulateLoad(servers[1], 2, 100)
	simulateLoad(servers[2], 5, 300)

	selected, metrics := p.selectServer("a test prompt", 50)
	if selected == nil {
		t.Fatal("expected a server, got nil")
	}
	if metrics == nil {
		t.Fatal("expected routing metrics, got nil")
	}
	// server-1 has lowest cost (fewest running tokens)
	if selected.ID != servers[1].ID {
		t.Errorf("expected server-1 (lowest GORGO cost), got %s", selected.ID)
	}
}

func TestSetRoutingPolicy(t *testing.T) {
	p := newTestProxy("gorgo")
	if p.GetRoutingPolicyName() != "gorgo" {
		t.Errorf("expected gorgo, got %s", p.GetRoutingPolicyName())
	}

	p.SetRoutingPolicy("least-load")
	if p.GetRoutingPolicyName() != "least-load" {
		t.Errorf("expected least-load, got %s", p.GetRoutingPolicyName())
	}

	p.SetRoutingPolicy("prefix-tree")
	if p.GetRoutingPolicyName() != "prefix-tree" {
		t.Errorf("expected prefix-tree, got %s", p.GetRoutingPolicyName())
	}
}

func TestNoServersReturnsNil(t *testing.T) {
	for _, policy := range []string{"gorgo", "least-load", "prefix-tree"} {
		p := newTestProxy(policy)
		selected, _ := p.selectServer("hello", 10)
		if selected != nil {
			t.Errorf("[%s] expected nil with no servers, got %s", policy, selected.ID)
		}
	}
}

func TestAllServersAtCapacityReturnsNil(t *testing.T) {
	for _, policy := range []string{"gorgo", "least-load", "prefix-tree"} {
		p := newTestProxy(policy)
		p.config.MaxRunningPerServer = 5
		servers := addTestServers(p, 3)

		for _, s := range servers {
			simulateLoad(s, 5, 100)
		}

		selected, _ := p.selectServer("hello", 10)
		if selected != nil {
			t.Errorf("[%s] expected nil when all servers at capacity, got %s", policy, selected.ID)
		}
	}
}

// =============================================================================
// BENCHMARKS — routing policy selection latency
// =============================================================================

// BenchmarkSelectServer/{policy}-{servers}-{loadPerServer}

func benchmarkSelectServer(b *testing.B, policyName string, serverCount, loadPerServer int, usePrefixCache bool) {
	p := newTestProxy(policyName)
	servers := addTestServers(p, serverCount)

	for _, s := range servers {
		simulateLoad(s, loadPerServer, 200)
	}

	if usePrefixCache {
		for i, prompt := range sharedPrompts {
			serverIdx := i % len(servers)
			p.insertPrefix(prompt, servers[serverIdx])
		}
	}

	rng := rand.New(rand.NewSource(42))
	prompts := make([]string, 1024)
	for i := range prompts {
		prompts[i] = randomPrompt(rng)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		prompt := prompts[i%len(prompts)]
		tokens := estimateTokenCount(prompt)
		p.selectServer(prompt, tokens)
	}
}

// --- GORGO ---

func BenchmarkSelectServer_GORGO_4servers_0load(b *testing.B) {
	benchmarkSelectServer(b, "gorgo", 4, 0, false)
}

func BenchmarkSelectServer_GORGO_4servers_10load(b *testing.B) {
	benchmarkSelectServer(b, "gorgo", 4, 10, false)
}

func BenchmarkSelectServer_GORGO_4servers_10load_prefixCache(b *testing.B) {
	benchmarkSelectServer(b, "gorgo", 4, 10, true)
}

func BenchmarkSelectServer_GORGO_16servers_10load(b *testing.B) {
	benchmarkSelectServer(b, "gorgo", 16, 10, false)
}

func BenchmarkSelectServer_GORGO_16servers_10load_prefixCache(b *testing.B) {
	benchmarkSelectServer(b, "gorgo", 16, 10, true)
}

func BenchmarkSelectServer_GORGO_64servers_10load(b *testing.B) {
	benchmarkSelectServer(b, "gorgo", 64, 10, false)
}

// --- Least Load ---

func BenchmarkSelectServer_LeastLoad_4servers_0load(b *testing.B) {
	benchmarkSelectServer(b, "least-load", 4, 0, false)
}

func BenchmarkSelectServer_LeastLoad_4servers_10load(b *testing.B) {
	benchmarkSelectServer(b, "least-load", 4, 10, false)
}

func BenchmarkSelectServer_LeastLoad_16servers_10load(b *testing.B) {
	benchmarkSelectServer(b, "least-load", 16, 10, false)
}

func BenchmarkSelectServer_LeastLoad_64servers_10load(b *testing.B) {
	benchmarkSelectServer(b, "least-load", 64, 10, false)
}

// --- Prefix Tree ---

func BenchmarkSelectServer_PrefixTree_4servers_0load(b *testing.B) {
	benchmarkSelectServer(b, "prefix-tree", 4, 0, false)
}

func BenchmarkSelectServer_PrefixTree_4servers_10load(b *testing.B) {
	benchmarkSelectServer(b, "prefix-tree", 4, 10, false)
}

func BenchmarkSelectServer_PrefixTree_4servers_10load_prefixCache(b *testing.B) {
	benchmarkSelectServer(b, "prefix-tree", 4, 10, true)
}

func BenchmarkSelectServer_PrefixTree_16servers_10load(b *testing.B) {
	benchmarkSelectServer(b, "prefix-tree", 16, 10, false)
}

func BenchmarkSelectServer_PrefixTree_16servers_10load_prefixCache(b *testing.B) {
	benchmarkSelectServer(b, "prefix-tree", 16, 10, true)
}

func BenchmarkSelectServer_PrefixTree_64servers_10load(b *testing.B) {
	benchmarkSelectServer(b, "prefix-tree", 64, 10, false)
}

// =============================================================================
// BENCHMARKS — concurrent selection under contention
// =============================================================================

func benchmarkConcurrentSelectServer(b *testing.B, policyName string, serverCount, loadPerServer, goroutines int) {
	p := newTestProxy(policyName)
	servers := addTestServers(p, serverCount)

	for _, s := range servers {
		simulateLoad(s, loadPerServer, 200)
	}

	for i, prompt := range sharedPrompts {
		p.insertPrefix(prompt, servers[i%len(servers)])
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		for pb.Next() {
			prompt := randomPrompt(rng)
			tokens := estimateTokenCount(prompt)
			p.selectServer(prompt, tokens)
		}
	})
}

func BenchmarkConcurrentSelect_GORGO_8servers(b *testing.B) {
	benchmarkConcurrentSelectServer(b, "gorgo", 8, 10, 16)
}

func BenchmarkConcurrentSelect_LeastLoad_8servers(b *testing.B) {
	benchmarkConcurrentSelectServer(b, "least-load", 8, 10, 16)
}

func BenchmarkConcurrentSelect_PrefixTree_8servers(b *testing.B) {
	benchmarkConcurrentSelectServer(b, "prefix-tree", 8, 10, 16)
}

// =============================================================================
// BENCHMARKS — prefix tree operations
// =============================================================================

func BenchmarkPrefixTreeInsert(b *testing.B) {
	p := newTestProxy("gorgo")
	servers := addTestServers(p, 4)

	rng := rand.New(rand.NewSource(42))

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		prompt := randomPrompt(rng)
		p.insertPrefix(prompt, servers[i%len(servers)])
	}
}

func BenchmarkPrefixTreeLookup(b *testing.B) {
	p := newTestProxy("gorgo")
	servers := addTestServers(p, 4)

	for i := 0; i < 1000; i++ {
		prompt := fmt.Sprintf("You are assistant %d. Please help with task number %d in category %d.", i, i*7, i%10)
		p.insertPrefix(prompt, servers[i%len(servers)])
	}

	rng := rand.New(rand.NewSource(42))

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		prompt := randomPrompt(rng)
		p.findPrefixMatches(prompt)
	}
}

// =============================================================================
// BENCHMARKS — throughput under simulated realistic workload
// =============================================================================

func BenchmarkRealisticWorkload(b *testing.B) {
	policies := []string{"gorgo", "least-load", "prefix-tree"}

	for _, policy := range policies {
		b.Run(policy, func(b *testing.B) {
			p := newTestProxy(policy)
			servers := addTestServers(p, 8)

			// Warm up: uneven load distribution
			loads := []int{2, 5, 8, 1, 3, 7, 4, 6}
			for i, s := range servers {
				simulateLoad(s, loads[i], 250)
			}

			// Insert prefixes for half the prompts
			for i := 0; i < len(sharedPrompts)/2+1; i++ {
				p.insertPrefix(sharedPrompts[i], servers[i%len(servers)])
			}

			rng := rand.New(rand.NewSource(42))
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				prompt := randomPrompt(rng)
				tokens := estimateTokenCount(prompt)
				p.selectServer(prompt, tokens)
			}
		})
	}
}

// =============================================================================
// BENCHMARKS — policy switch overhead
// =============================================================================

func BenchmarkPolicySwitchAndSelect(b *testing.B) {
	p := newTestProxy("gorgo")
	addTestServers(p, 4)

	policies := []string{"gorgo", "least-load", "prefix-tree"}
	rng := rand.New(rand.NewSource(42))

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if i%100 == 0 {
			p.SetRoutingPolicy(policies[i/100%len(policies)])
		}
		prompt := randomPrompt(rng)
		tokens := estimateTokenCount(prompt)
		p.selectServer(prompt, tokens)
	}
}

// =============================================================================
// BENCHMARKS — large prefix tree
// =============================================================================

func BenchmarkSelectServer_LargePrefixTree(b *testing.B) {
	policies := []string{"gorgo", "prefix-tree"}

	for _, policy := range policies {
		b.Run(policy, func(b *testing.B) {
			p := newTestProxy(policy)
			servers := addTestServers(p, 8)

			// Build a large prefix tree with 10k entries
			for i := 0; i < 10000; i++ {
				prefix := fmt.Sprintf("You are assistant %d. System prompt variation %d for task category %d with parameters %d.",
					i%50, i, i%20, i*3)
				p.insertPrefix(prefix, servers[i%len(servers)])
			}

			for _, s := range servers {
				simulateLoad(s, 5, 200)
			}

			rng := rand.New(rand.NewSource(42))
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				prompt := randomPrompt(rng)
				tokens := estimateTokenCount(prompt)
				p.selectServer(prompt, tokens)
			}
		})
	}
}

// =============================================================================
// TEST — concurrent safety of policy switching
// =============================================================================

func TestConcurrentPolicySwitchSafety(t *testing.T) {
	p := newTestProxy("gorgo")
	addTestServers(p, 4)

	var wg sync.WaitGroup
	policies := []string{"gorgo", "least-load", "prefix-tree"}

	// Writers: switch policy
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				p.SetRoutingPolicy(policies[(id+j)%len(policies)])
			}
		}(i)
	}

	// Readers: select server
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				p.selectServer("test prompt for concurrency safety", 50)
			}
		}()
	}

	wg.Wait()
}
