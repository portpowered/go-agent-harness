package anthropic

import "testing"

func TestWithModelOverridesTheDefaultModel(t *testing.T) {
	if got := New(WithModel("claude-test")).model; got != "claude-test" {
		t.Fatalf("model = %q, want claude-test", got)
	}
}
