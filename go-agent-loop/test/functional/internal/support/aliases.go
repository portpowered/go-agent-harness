// Package support exposes the shared functional harness to concern-oriented
// test packages. The implementation remains with the media package because
// media streaming tests intentionally exercise its package-private fixture
// entries.
package support

import "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/media"

type MockInferencer = media.MockInferencer
type MockToolExecutor = media.MockToolExecutor
type Scenario = media.Scenario
type ExpectedMessage = media.ExpectedMessage
type ExpectedDelta = media.ExpectedDelta

var NewMockToolExecutor = media.NewMockToolExecutor
var NewScenario = media.NewScenario
var AssertMessages = media.AssertMessages
var AssertDeltaContains = media.AssertDeltaContains
