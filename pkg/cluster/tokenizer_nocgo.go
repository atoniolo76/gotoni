//go:build !cgo

// pkg/cluster/tokenizer_nocgo.go
// Fallback when CGO is disabled (e.g., local macOS builds).
// Remote Linux instances use the real Rust tokenizer via tokenizer_cgo.go.
package serve

// GetTokenCount estimates the number of tokens in the text.
// Without CGO, uses a rough heuristic of ~4 characters per token.
func GetTokenCount(text string) int {
	return len(text) / 4
}
