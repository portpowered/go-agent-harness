package sessionduration

import (
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TranscriptObserver receives the terminal evidence for each rendered stream
// message. The boolean reports whether a visible terminal line would need a
// leading newline. A session terminal reporter satisfies this port.
type TranscriptObserver interface {
	ObserveStreamMessage(messages.StreamMessage, bool)
}

// Transcript is the stateful operator-facing renderer for one session stream.
// Raw writes close an open transcript line first; Finish closes the final one.
type Transcript interface {
	io.Writer
	WriteMessage(messages.StreamMessage) error
	Finish() error
}

// StopFacts are host observations consulted by the session stop policy. They
// report facts only; the policy decision remains service-owned.
type StopFacts interface {
	HasTerminalToolContinuationFailure() bool
	HasTerminalScheduledResponseFailure() bool
	LastMessageEndAdmitted() bool
	HasToolLifecycleObligation() bool
	ScheduledAudioComplete() bool
}

// StopPolicy is the immutable completion configuration of one session loop.
// Facts may be nil when the host has no session observer.
type StopPolicy struct {
	CloseAfterOpen           bool
	WaitForClose             bool
	CloseAfterScheduledAudio bool
	Facts                    StopFacts
}

// StragglerDrain bounds the quiet-period observation of provider output that
// arrives after a session has selected its terminal boundary. Publish is the
// host rendering port; Clock is the canonical quiet-period scheduler. A
// positive WallSafety adds an independent wall-time bound for deterministic
// clocks that may stop advancing during teardown.
type StragglerDrain struct {
	Deltas      *messages.TypedBuffer[messages.StreamMessage]
	Clock       TimerScheduler
	QuietPeriod time.Duration
	WallSafety  time.Duration
	Publish     func(messages.StreamMessage) error
}

// SessionUpdatedWait bounds a host-observed configuration acknowledgement
// after an admitted SESSION.OPEN. Pending and Ready report facts only; the
// timer and timeout delivery are owned by the duration service. A zero
// Timeout disables the wait.
type SessionUpdatedWait struct {
	Timeout      time.Duration
	Pending      func() bool
	Ready        func() bool
	TimeoutError error
}
