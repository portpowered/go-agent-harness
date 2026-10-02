package inference

import "testing"

func TestWithSessionReasoningEffortReachesTheSessionRequest(t *testing.T) {
	inferencer := NewSessionGatewayInferencer(nil, WithSessionReasoningEffort("high"))
	if got := inferencer.Request().Config.ReasoningEffort; got != "high" {
		t.Fatalf("ReasoningEffort = %q, want high", got)
	}
}
