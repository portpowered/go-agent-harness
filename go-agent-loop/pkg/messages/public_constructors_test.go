package messages

import "testing"

func TestNewErrorValueWithDetailsKeepsProviderMetadata(t *testing.T) {
	value := NewErrorValueWithDetails("rate limited", "rate_limit_error", "429", "model", "evt-1")
	if value.Type != "error" || value.Message != "rate limited" || value.ErrorType != "rate_limit_error" ||
		value.Code != "429" || value.Param != "model" || value.EventID != "evt-1" {
		t.Fatalf("NewErrorValueWithDetails() = %#v", value)
	}
}

func TestNewEmbeddingEndValueUsesTheWireType(t *testing.T) {
	if value := NewEmbeddingEndValue(); value.Type != "embedding_end" {
		t.Fatalf("NewEmbeddingEndValue().Type = %q, want embedding_end", value.Type)
	}
}
