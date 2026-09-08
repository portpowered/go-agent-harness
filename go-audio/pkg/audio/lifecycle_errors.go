package audio

import "github.com/portpowered/go-agent-harness/go-audio/pkg/contract"

// ErrClosed is the shared lifecycle error for audio sources, sinks, and
// control queues. The neutral contract package owns its identity so low-level
// device adapters do not depend on analysis packages.
var ErrClosed = contract.ErrClosed
