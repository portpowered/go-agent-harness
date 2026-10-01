// Package support exposes the shared functional harness to concern-oriented
// test packages. The implementation remains with the media package because
// media streaming tests intentionally exercise its package-private fixture
// entries.
package support

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/media"
)

type MockInferencer = media.MockInferencer
type MockToolExecutor = media.MockToolExecutor
type Scenario = media.Scenario
type ExpectedMessage = media.ExpectedMessage
type ExpectedDelta = media.ExpectedDelta

// NewMockToolExecutor forwards to media.NewMockToolExecutor.
func NewMockToolExecutor() *MockToolExecutor { return media.NewMockToolExecutor() }

// NewScenario forwards to media.NewScenario.
func NewScenario(t *testing.T, inf *MockInferencer, tool *MockToolExecutor, opts ...agentloop.Option) *Scenario {
	return media.NewScenario(t, inf, tool, opts...)
}

// AssertMessages forwards to media.AssertMessages.
func AssertMessages(t *testing.T, msgs []messages.Message, expected []ExpectedMessage) {
	t.Helper()
	media.AssertMessages(t, msgs, expected)
}

// AssertDeltaContains forwards to media.AssertDeltaContains.
func AssertDeltaContains(t *testing.T, deltas []messages.StreamMessage, required []ExpectedDelta) {
	t.Helper()
	media.AssertDeltaContains(t, deltas, required)
}
