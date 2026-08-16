package sessions

import "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/internal/support"

type MockToolExecutor = support.MockToolExecutor
type ExpectedDelta = support.ExpectedDelta

var NewMockToolExecutor = support.NewMockToolExecutor
var AssertDeltaContains = support.AssertDeltaContains
