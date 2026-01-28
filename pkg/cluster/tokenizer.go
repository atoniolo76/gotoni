// pkg/cluster/tokenizer.go
// Tokenizer client that communicates with the Rust sidecar via Unix Domain Socket
package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	// TokenizerSocketPath is the Unix socket path for the tokenizer sidecar
	TokenizerSocketPath = "/tmp/tokenizer.sock"

	// Fallback character-to-token ratio when sidecar is unavailable
	FallbackCharsPerToken = 4
)

var (
	tokenizerClient     *http.Client
	tokenizerClientOnce sync.Once
	tokenizerAvailable  bool
	tokenizerMu         sync.RWMutex
)

// tokenizeRequest is the JSON payload sent to the sidecar
type tokenizeRequest struct {
	Text string `json:"text"`
}

// tokenizeResponse is the JSON response from the sidecar
type tokenizeResponse struct {
	Count int `json:"count"`
}

// healthResponse is the JSON response from the health endpoint
type healthResponse struct {
	Status string `json:"status"`
	Model  string `json:"model"`
}

// initTokenizerClient initializes the HTTP client with Unix socket transport
func initTokenizerClient() {
	tokenizerClient = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", TokenizerSocketPath)
			},
		},
		Timeout: 5 * time.Second,
	}

	// Check if sidecar is available
	tokenizerAvailable = checkTokenizerHealth()
}

// checkTokenizerHealth checks if the tokenizer sidecar is running
func checkTokenizerHealth() bool {
	resp, err := tokenizerClient.Get("http://uds/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var health healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return false
	}

	return health.Status == "ok"
}

// GetTokenCount returns the number of tokens in the text using the tokenizer sidecar.
// Falls back to character-based estimation if the sidecar is unavailable.
func GetTokenCount(text string) int {
	if len(text) == 0 {
		return 0
	}

	// Initialize client once
	tokenizerClientOnce.Do(initTokenizerClient)

	// Check if sidecar is available
	tokenizerMu.RLock()
	available := tokenizerAvailable
	tokenizerMu.RUnlock()

	if !available {
		// Try to reconnect periodically
		go func() {
			tokenizerMu.Lock()
			defer tokenizerMu.Unlock()
			if !tokenizerAvailable {
				tokenizerAvailable = checkTokenizerHealth()
			}
		}()
		return fallbackTokenCount(text)
	}

	// Make request to sidecar
	count, err := getTokenCountFromSidecar(text)
	if err != nil {
		// Mark as unavailable and use fallback
		tokenizerMu.Lock()
		tokenizerAvailable = false
		tokenizerMu.Unlock()
		return fallbackTokenCount(text)
	}

	return count
}

// getTokenCountFromSidecar makes the actual request to the tokenizer sidecar
func getTokenCountFromSidecar(text string) (int, error) {
	reqBody, err := json.Marshal(tokenizeRequest{Text: text})
	if err != nil {
		return 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	// The "http://uds" hostname is a placeholder; the Unix socket transport ignores it
	resp, err := tokenizerClient.Post("http://uds/count", "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return 0, fmt.Errorf("failed to call tokenizer sidecar: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("tokenizer sidecar returned status %d", resp.StatusCode)
	}

	var result tokenizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("failed to decode response: %w", err)
	}

	return result.Count, nil
}

// fallbackTokenCount estimates token count when sidecar is unavailable
func fallbackTokenCount(text string) int {
	return (len(text) + FallbackCharsPerToken - 1) / FallbackCharsPerToken
}

// IsTokenizerAvailable returns whether the tokenizer sidecar is currently available
func IsTokenizerAvailable() bool {
	tokenizerClientOnce.Do(initTokenizerClient)
	tokenizerMu.RLock()
	defer tokenizerMu.RUnlock()
	return tokenizerAvailable
}

// RefreshTokenizerConnection attempts to reconnect to the tokenizer sidecar
func RefreshTokenizerConnection() bool {
	tokenizerClientOnce.Do(initTokenizerClient)
	tokenizerMu.Lock()
	defer tokenizerMu.Unlock()
	tokenizerAvailable = checkTokenizerHealth()
	return tokenizerAvailable
}
