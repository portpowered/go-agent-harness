package sessions

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/internal/support"
)

type MockToolExecutor = support.MockToolExecutor
type ExpectedDelta = support.ExpectedDelta

// NewMockToolExecutor forwards to support.NewMockToolExecutor.
func NewMockToolExecutor() *MockToolExecutor { return support.NewMockToolExecutor() }

// AssertDeltaContains forwards to support.AssertDeltaContains.
func AssertDeltaContains(t *testing.T, deltas []messages.StreamMessage, required []ExpectedDelta) {
	t.Helper()
	support.AssertDeltaContains(t, deltas, required)
}
