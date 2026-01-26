// pkg/cluster/tokenizer.go
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
