package session

import (
	"time"
)

// LiveOpeningMessageResponse controls whether a rich opening message starts
// its provider response immediately or is queued for a following audio
// boundary. Plain opening text retains the existing response behavior.
type LiveOpeningMessageResponse string

const (
	LiveOpeningMessageRespond LiveOpeningMessageResponse = "respond"
	LiveOpeningMessageQueued  LiveOpeningMessageResponse = "queued"
)

// LiveLivenessPolicy controls provider progress checks for one invocation.
// The watchdog is reset by each accepted provider event and is suspended
// while a local tool executes. The policy is intentionally opt-in so hosts
// that use a provider-specific liveness owner can retain that behavior.
type LiveLivenessPolicy struct {
	Enabled bool
	Timeout time.Duration
}

// LiveReplayPlan is the narrow self-driving portion of a replay capture. A
// host may leave it nil for ordinary replay, or provide a plan resolved from
// an explicit capture before opening the session. The live owner sends each
// turn's chunks through its bounded media ingress and then admits one ordered
// audio commit.
type LiveReplayPlan struct {
	OpeningPrompt        string
	OpeningPromptPresent bool
	AudioTurns           []LiveReplayAudioTurn
	// InputAudioSampleRate and OutputAudioSampleRate are the negotiated
	// provider rates recovered from the capture's initial session.update.
	// Zero means that the capture did not declare a rate; the host then chooses
	// its explicit compatibility rate before opening the provider service.
	InputAudioSampleRate  int
	OutputAudioSampleRate int
	// WaitForSessionUpdated is true when the capture contains the provider's
	// session.updated handshake boundary before its first client action. A
	// finite replay must wait for that boundary before admitting PCM; otherwise
	// a fast host can write the first append while the replay cursor still
	// expects the server handshake.
	WaitForSessionUpdated bool
	// StopAfterResponse is true when the capture has no provider session-close
	// boundary and the invocation must finish at the first completed response
	// after replay admission. Provider-close captures leave this false so the
	// close event remains observable to hosts.
	StopAfterResponse bool
	// ProviderCloseExpected records whether the capture contains an explicit
	// provider session-close boundary. It is advisory metadata for host
	// rendering and lifecycle selection; replay validation remains strict.
	ProviderCloseExpected bool
}

// LiveReplayAudioTurn preserves the captured append boundaries. Chunks are
// copied at admission and must remain bounded by the host's replay resolver.
type LiveReplayAudioTurn struct {
	Chunks [][]int16
}

// LiveRateLimitRetryPolicy describes the one-session retry budget for
// provider-authored rate-limit terminals. The provider's delay hint is used
// when present and clamped to MaxDelay. Zero MaxRetries means one retry when
// Enabled is true; zero delay bounds select the service defaults.
type LiveRateLimitRetryPolicy struct {
	Enabled      bool
	MaxRetries   int
	DefaultDelay time.Duration
	MaxDelay     time.Duration
}

// LiveReplayPolicy describes an explicitly selected capture or replay source.
// The fields are intentionally paths rather than open files: the host owns
// path validation and opens them before admission when a stronger boundary is
// required. Empty paths select live provider operation with no capture.
type LiveReplayPolicy struct {
	InputCapturePath  string
	OutputCapturePath string
	Timing            LiveReplayTiming
}

// LiveReplayTiming controls how a host-owned replay source advances.
type LiveReplayTiming string

const (
	LiveReplayTimingRealtime LiveReplayTiming = "realtime"
	LiveReplayTimingFast     LiveReplayTiming = "fast"
	LiveReplayTimingStep     LiveReplayTiming = "step"
)
