// Package replay provides the CLI compatibility view of the public strict
// replay contract. Business implementation and construction live in the
// embeddable go-agent-runtime replay service.
package replay

import runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"

type Request = runtimeReplay.StrictRequest
type Prepared = runtimeReplay.StrictPrepared
type EvidenceScope = runtimeReplay.StrictEvidenceScope
type Runtime = runtimeReplay.StrictRuntime
type RuntimeFactory = runtimeReplay.StrictRuntimeFactory
type Result = runtimeReplay.StrictResult
type Service = runtimeReplay.StrictService

var (
	ErrBundleIncomplete           = runtimeReplay.ErrBundleIncomplete
	ErrBundleMismatch             = runtimeReplay.ErrBundleMismatch
	ErrToolMismatch               = runtimeReplay.ErrToolMismatch
	ErrToolFailure                = runtimeReplay.ErrToolFailure
	ErrDeterministicClockRequired = runtimeReplay.ErrDeterministicClockRequired
	ErrRuntimeFactoryRequired     = runtimeReplay.ErrRuntimeFactoryRequired
)
