//go:build cgo

// pkg/cluster/tokenizer_cgo.go
// This file is only built when CGO is enabled (provides accurate token counting)
package serve

import (
	"sync"

	"github.com/daulet/tokenizers"
)

var (
	tokenizer     *tokenizers.Tokenizer
	tokenizerOnce sync.Once
	tokenizerErr  error
)

// GetTokenCount returns the number of tokens in the text using the Mistral tokenizer.
// This is the CGO-enabled version that provides accurate token counts.
func GetTokenCount(text string) int {
	tokenizerOnce.Do(func() {
		tokenizer, tokenizerErr = tokenizers.FromPretrained("mistralai/Mistral-7B-Instruct-v0.3")
	})

	if tokenizerErr != nil {
		// Fallback: estimate ~4 chars per token
		return len(text) / 4
	}

	// Encode returns ([]uint32 IDs, []string tokens)
	ids, _ := tokenizer.Encode(text, false)
	return len(ids)
}
