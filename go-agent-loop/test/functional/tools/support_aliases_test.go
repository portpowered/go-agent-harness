package tools

import "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/internal/support"

type MockInferencer = support.MockInferencer
type MockToolExecutor = support.MockToolExecutor
type Scenario = support.Scenario
type ExpectedMessage = support.ExpectedMessage
type ExpectedDelta = support.ExpectedDelta

var NewMockToolExecutor = support.NewMockToolExecutor
var NewScenario = support.NewScenario
var AssertMessages = support.AssertMessages
var AssertDeltaContains = support.AssertDeltaContains
