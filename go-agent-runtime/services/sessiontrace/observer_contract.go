package sessiontrace

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// RuntimeRecorder owns ordered runtime observations for one invocation. The
// recorder is intentionally host-neutral; callers provide provider/device
// boundary values and never construct its mutable state themselves.
type RuntimeRecorder interface {
	EnableProviderBoundaryObservations()
	ObservesProviderBoundaries() bool
	Observe(SessionRuntimeObservationKind, []byte, int, bool, error)
	ObserveWithInputCommit(SessionRuntimeObservationKind, []byte, int, int, bool, error)
	ObserveFinal(SessionRuntimeObservationKind, []byte, int, int, bool, error, *SessionFinalAccounting)
	AudioOutputMessage([]byte, messages.StreamMessage)
	AudioPlaybackReceipt(audio.PlaybackReceipt)
	AudioInput([]byte)
	ProviderAudioSent([]byte)
	InputCommit()
	ProviderInputCommit()
	ResponseCreate(messages.StreamMessage)
	TurnCompleted(int)
	TerminalWithAccounting(int, error, *SessionFinalAccounting)
}
