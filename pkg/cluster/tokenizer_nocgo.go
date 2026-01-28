//go:build !cgo

// pkg/cluster/tokenizer_nocgo.go
// This file is built when CGO is disabled (provides fallback estimation)
package serve

// GetTokenCount returns an estimated number of tokens in the text.
// When CGO is disabled, we use a simple heuristic: ~4 characters per token.
// This is less accurate but allows the code to compile and run without CGO.
func GetTokenCount(text string) int {
	// Fallback: estimate ~4 chars per token (common approximation for English text)
	if len(text) == 0 {
		return 0
	}
	return (len(text) + 3) / 4 // Round up
}
