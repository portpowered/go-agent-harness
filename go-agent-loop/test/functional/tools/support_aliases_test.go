package tools

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/internal/support"
)

type MockInferencer = support.MockInferencer
type MockToolExecutor = support.MockToolExecutor
type Scenario = support.Scenario
type ExpectedMessage = support.ExpectedMessage
type ExpectedDelta = support.ExpectedDelta

// NewMockToolExecutor forwards to support.NewMockToolExecutor.
func NewMockToolExecutor() *MockToolExecutor { return support.NewMockToolExecutor() }

// NewScenario forwards to support.NewScenario.
func NewScenario(t *testing.T, inf *MockInferencer, tool *MockToolExecutor, opts ...agentloop.Option) *Scenario {
	return support.NewScenario(t, inf, tool, opts...)
}

// AssertMessages forwards to support.AssertMessages.
func AssertMessages(t *testing.T, msgs []messages.Message, expected []ExpectedMessage) {
	t.Helper()
	support.AssertMessages(t, msgs, expected)
}

// AssertDeltaContains forwards to support.AssertDeltaContains.
func AssertDeltaContains(t *testing.T, deltas []messages.StreamMessage, required []ExpectedDelta) {
	t.Helper()
	support.AssertDeltaContains(t, deltas, required)
}
