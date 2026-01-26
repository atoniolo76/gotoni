package serve

import (
	"testing"
)

func TestGetTokenCount(t *testing.T) {
	tokenCount := GetTokenCount("Hello, world!")
	t.Logf("Token count for 'Hello, world!': %d", tokenCount)

	// Mistral tokenizer should produce 4-6 tokens for this phrase
	// (varies by tokenizer, but should be in a reasonable range)
	if tokenCount < 1 || tokenCount > 10 {
		t.Errorf("Expected 1-10 tokens, got %d", tokenCount)
	}

	// Test longer text
	longText := "The quick brown fox jumps over the lazy dog."
	longCount := GetTokenCount(longText)
	t.Logf("Token count for '%s': %d", longText, longCount)

	// Should be roughly 10-15 tokens
	if longCount < 5 || longCount > 20 {
		t.Errorf("Expected 5-20 tokens for long text, got %d", longCount)
	}
}
