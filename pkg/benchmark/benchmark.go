package benchmark

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// MeasureTTFT sends a chat completion request and returns the duration and prompt token count
func MeasureTTFT(url, content string, client *http.Client) (duration time.Duration, promptTokens int, err error) {
	payload := fmt.Sprintf(`{"model": "mistralai/Mistral-7B-Instruct-v0.3", "messages": [{"role": "user", "content": %q}], "max_tokens": 1}`, content)

	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	if err != nil {
		return 0, 0, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	duration = time.Since(start)

	if err != nil {
		return duration, 0, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse prompt_tokens from response
	var result struct {
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if jsonErr := json.Unmarshal(body, &result); jsonErr == nil {
		promptTokens = result.Usage.PromptTokens
	}

	return duration, promptTokens, nil
}

// WildchatPrompt represents a prompt from the WildChat dataset
type WildchatPrompt struct {
	Article       string `json:"article"`
	SummaryTokens int    `json:"summary_tokens"`
}

// TestMeasureGORGO_MS_PER_CHAR measures prefill rate using character-based measurement
func TestMeasureGORGO_MS_PER_CHAR(t *testing.T) {
	url := "http://151.145.83.16:8080/v1/chat/completions"
	client := &http.Client{Timeout: 30 * time.Second}

	// Load prompts from WildChat dataset
	wildchatPath := "../../benchmark/wildchat_flat.jsonl"
	t.Logf("Loading prompts from %s...", wildchatPath)

	file, err := os.Open(wildchatPath)
	if err != nil {
		t.Fatalf("Failed to open wildchat file: %v", err)
	}
	defer file.Close()

	var allPrompts []WildchatPrompt
	scanner := bufio.NewScanner(file)
	// Increase scanner buffer for long lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var prompt WildchatPrompt
		if err := json.Unmarshal(scanner.Bytes(), &prompt); err != nil {
			continue // skip malformed lines
		}
		// Filter out very short or very long prompts
		if len(prompt.Article) >= 10 && len(prompt.Article) <= 10000 {
			allPrompts = append(allPrompts, prompt)
		}
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("Error reading wildchat file: %v", err)
	}

	t.Logf("Loaded %d prompts from WildChat", len(allPrompts))

	// First, measure token counts for all prompts (one-time cost)
	t.Log("Measuring token counts for prompts...")
	type promptWithTokens struct {
		content string
		tokens  int
	}
	var promptsWithTokens []promptWithTokens

	// Sample every Nth prompt to get token counts faster
	sampleStep := max(1, len(allPrompts)/50)
	for i := 0; i < len(allPrompts); i += sampleStep {
		// Quick request just to get token count
		_, tokens, err := MeasureTTFT(url, allPrompts[i].Article, client)
		if err != nil {
			continue
		}
		promptsWithTokens = append(promptsWithTokens, promptWithTokens{
			content: allPrompts[i].Article,
			tokens:  tokens,
		})
	}

	// Sort by actual token count
	sort.Slice(promptsWithTokens, func(i, j int) bool {
		return promptsWithTokens[i].tokens < promptsWithTokens[j].tokens
	})

	t.Logf("Got token counts for %d prompts", len(promptsWithTokens))

	// Helper to check if two strings share a prefix (min 20 chars to be meaningful)
	sharesPrefix := func(a, b string, minLen int) bool {
		maxCheck := min(len(a), len(b), 100) // check up to first 100 chars
		if maxCheck < minLen {
			return false
		}
		for i := minLen; i <= maxCheck; i++ {
			if a[:i] == b[:i] {
				return true
			}
		}
		return false
	}

	// Select 10 evenly-spaced prompts by token count, avoiding prefix overlaps
	numSamples := 10
	if len(promptsWithTokens) < numSamples {
		numSamples = len(promptsWithTokens)
	}

	var selectedPrompts []promptWithTokens
	targetIndices := make([]int, numSamples)
	for i := 0; i < numSamples; i++ {
		targetIndices[i] = i * (len(promptsWithTokens) - 1) / (numSamples - 1)
	}

	for _, targetIdx := range targetIndices {
		// Try to find a prompt near targetIdx that doesn't share prefix with selected ones
		found := false
		for offset := 0; offset < len(promptsWithTokens) && !found; offset++ {
			// Alternate checking above and below target
			for _, delta := range []int{offset, -offset} {
				idx := targetIdx + delta
				if idx < 0 || idx >= len(promptsWithTokens) {
					continue
				}
				candidate := promptsWithTokens[idx]

				// Check for prefix overlap with all selected prompts
				hasOverlap := false
				for _, selected := range selectedPrompts {
					if sharesPrefix(candidate.content, selected.content, 20) {
						hasOverlap = true
						break
					}
				}

				if !hasOverlap {
					selectedPrompts = append(selectedPrompts, candidate)
					found = true
					break
				}
			}
		}
	}

	t.Logf("Selected %d prompts with no overlapping prefixes", len(selectedPrompts))

	// Build test cases from selected prompts
	testCases := make([]struct {
		name    string
		content string
	}, len(selectedPrompts))

	for i, p := range selectedPrompts {
		testCases[i] = struct {
			name    string
			content string
		}{
			name:    fmt.Sprintf("wc_%d", i+1),
			content: p.content,
		}
		// Show first 40 chars of each prompt to verify no prefix overlap
		preview := p.content
		if len(preview) > 40 {
			preview = preview[:40] + "..."
		}
		t.Logf("  %s: %d tokens, prefix: %q", testCases[i].name, p.tokens, preview)
	}

	t.Logf("Selected %d prompts with lengths: %v chars",
		len(selectedPrompts),
		func() []int {
			lengths := make([]int, len(selectedPrompts))
			for i, tc := range testCases {
				lengths[i] = len(tc.content)
			}
			return lengths
		}())

	// Warmup request (not timed)
	t.Log("Sending warmup requests...")
	for i := 0; i < 3; i++ {
		_, _, err := MeasureTTFT(url, "warmup", client)
		if err != nil {
			t.Fatalf("Warmup failed: %v", err)
		}
	}
	t.Log("Warmup complete")

	t.Log("\n=== TTFT Measurements (WildChat prompts) ===")
	t.Logf("%-8s | %-6s | %-8s | %-12s", "Sample", "Tokens", "Chars", "TTFT")
	t.Log(strings.Repeat("-", 50))

	var results []struct {
		tokens   int
		duration time.Duration
		chars    int
	}

	for _, tc := range testCases {
		// Run 3 times and take the median to reduce noise
		var durations []time.Duration
		var tokens int

		for i := 0; i < 3; i++ {
			d, tok, err := MeasureTTFT(url, tc.content, client)
			if err != nil {
				t.Fatalf("Request failed for %s: %v", tc.name, err)
			}
			durations = append(durations, d)
			tokens = tok
			time.Sleep(50 * time.Millisecond) // small gap between requests
		}

		// Sort and take median
		sort.Slice(durations, func(i, j int) bool {
			return durations[i] < durations[j]
		})
		median := durations[len(durations)/2]

		t.Logf("%-8s | %-6d | %-8d | %v", tc.name, tokens, len(tc.content), median)
		results = append(results, struct {
			tokens   int
			duration time.Duration
			chars    int
		}{tokens, median, len(tc.content)})
	}

	// Calculate prefill rate using linear regression
	// TTFT = base_latency + (prefill_rate * tokens)
	// slope = prefill_rate (ms/token)
	// intercept = base_latency (network + overhead)
	if len(results) >= 2 {
		t.Log(strings.Repeat("-", 50))

		// Convert to float64 for regression
		n := float64(len(results))
		var sumX, sumY, sumXY, sumX2, sumY2 float64

		for _, r := range results {
			x := float64(r.tokens)
			y := float64(r.duration.Microseconds()) / 1000.0 // convert to ms
			sumX += x
			sumY += y
			sumXY += x * y
			sumX2 += x * x
			sumY2 += y * y
		}

		// Linear regression: y = intercept + slope * x
		// slope = (n*Σxy - Σx*Σy) / (n*Σx² - (Σx)²)
		// intercept = (Σy - slope*Σx) / n
		denominator := n*sumX2 - sumX*sumX
		if denominator != 0 {
			slope := (n*sumXY - sumX*sumY) / denominator
			intercept := (sumY - slope*sumX) / n

			// Calculate R² (coefficient of determination)
			// R² = 1 - (SS_res / SS_tot)
			meanY := sumY / n
			var ssRes, ssTot float64
			for _, r := range results {
				x := float64(r.tokens)
				y := float64(r.duration.Microseconds()) / 1000.0
				predicted := intercept + slope*x
				ssRes += (y - predicted) * (y - predicted)
				ssTot += (y - meanY) * (y - meanY)
			}
			r2 := 1.0 - (ssRes / ssTot)

			t.Logf("Linear regression: TTFT = %.2f ms + %.4f ms/token × tokens", intercept, slope)
			t.Logf("  Base latency (intercept): %.2f ms", intercept)
			t.Logf("  Prefill rate (slope):     %.4f ms/token", slope)
			t.Logf("  R² (goodness of fit):     %.4f", r2)

			// Also show neighbor deltas for comparison
			t.Log("")
			t.Log("Neighbor deltas:")
			var deltaSum float64
			for i := 1; i < len(results); i++ {
				tokenDelta := results[i].tokens - results[i-1].tokens
				timeDelta := results[i].duration - results[i-1].duration
				msPerToken := float64(timeDelta.Microseconds()) / float64(tokenDelta) / 1000.0
				deltaSum += msPerToken
				t.Logf("  %d→%d tokens: %+.3f ms/token", results[i-1].tokens, results[i].tokens, msPerToken)
			}
			avgDelta := deltaSum / float64(len(results)-1)
			t.Logf("  Average delta: %.4f ms/token", avgDelta)
		}
	}
}