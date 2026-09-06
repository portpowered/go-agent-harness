package session

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// LiveRequest contains normalized settings for a continuous bidirectional
// session. Audio and RTC selection are supplied through the service's ports;
// this value does not name a device, terminal, or provider implementation.
type LiveRequest struct {
	SessionID     string
	ParticipantID string
	Provider      string
	Model         string
	BaseURL       string
	RealtimeURL   string
	// CredentialReference is an opaque host-owned selector for the
	// participant's provider credential. It is resolved by the injected
	// inferencer/provider factory at admission time and is never interpreted as
	// an environment variable, API key, or replay payload by the runtime.
	CredentialReference string
	Instructions        string
	// OpeningPrompt is sent as the first user turn after the provider accepts
	// the session. It is data supplied by the host, so an empty prompt remains
	// a valid audio-only session.
	OpeningPrompt        string
	OpeningPromptPresent bool
	// OpeningContentParts are already-admitted content for the first user
	// turn, such as image bytes resolved by a host. The runtime never reads a
	// path or discovers a MIME type. When OpeningMessageResponse is
	// LiveOpeningMessageQueued, the parts are queued without requesting a
	// response so a finite audio source can commit the complete turn.
	OpeningContentParts     []messages.ContentPart
	OpeningMessageResponse  LiveOpeningMessageResponse
	Voice                   string
	ReasoningEffort         string
	InputTranscription      bool
	InputTranscriptionModel string
	InputAudioFormat        string
	OutputAudioFormat       string
	InputAudioSampleRate    int
	OutputAudioSampleRate   int
	TurnDetection           *LiveTurnDetection
	// ClientOwnsAudioTurnBoundaries disables provider-side VAD when true. It
	// is used by finite room/replay feeds that admit one explicit commit per
	// audio turn.
	ClientOwnsAudioTurnBoundaries bool
	// ToolNames is the participant's admitted tool-name allowlist. The live
	// capability factory uses it to select an invocation-local surface without
	// sharing mutable browser or tool state across participants.
	ToolNames []string
	// Capabilities is an optional already-admitted participant binding. Room
	// owners use it when a request-scoped browser/tool factory must run before
	// OpenLive; the handle takes ownership of Close.
	Capabilities *LiveCapabilities
	// Replay carries explicit capture/replay policy to the provider factory.
	// The session service never discovers files or creates host paths from it.
	Replay LiveReplayPolicy
	// ReplayPlan is an optional host-resolved plan for a self-driving replay.
	// It contains only bounded PCM chunks and an opening prompt; transport
	// handshake bytes remain owned by the provider replay service.
	ReplayPlan *LiveReplayPlan
	// MaxDuration bounds this invocation from Start through terminal cleanup.
	// A non-zero value requires the service's injected clock.Scheduler.
	MaxDuration time.Duration
	// RequireSessionUpdated makes the live owner wait for the provider's
	// SESSION.UPDATED acknowledgement after SESSION.OPEN. This is used by
	// scheduled audio admission and is independent of provider implementation.
	RequireSessionUpdated bool
	// SessionUpdatedTimeout bounds the acknowledgement wait. Zero selects the
	// service's bounded default when RequireSessionUpdated is true.
	SessionUpdatedTimeout time.Duration
	// RequireFirstTurn enables a bounded wait for the first provider response
	// after SESSION.OPEN. It is useful for finite opening prompts and scheduled
	// audio admission, where sending later media before the first turn is
	// accepted would reorder the conversation. The wait uses the injected
	// scheduler and is disabled by default for audio-only sessions.
	RequireFirstTurn bool
	// FirstTurnTimeout bounds the first-turn wait. A zero value selects the
	// service's bounded default when RequireFirstTurn is true. A positive value
	// also enables the policy, which keeps request construction concise for
	// hosts that only need a custom deadline.
	FirstTurnTimeout time.Duration
	// ToolExecutionTimeout bounds each participant tool invocation. A positive
	// value uses the injected scheduler's context domain so tool cancellation,
	// event timestamps, and room deadlines share one clock.
	ToolExecutionTimeout time.Duration
	// RateLimitRetry enables a bounded response.create retry when a provider
	// emits a rate-limit terminal. It is opt-in because retrying an arbitrary
	// realtime turn can duplicate side effects; scheduled/replay hosts select
	// it only when their admission policy makes the turn retryable.
	RateLimitRetry LiveRateLimitRetryPolicy
	// ProviderLiveness enables the participant-owned provider progress
	// watchdog and empty-response classifier. A positive Timeout also enables
	// the policy for concise room and host request construction; zero selects
	// the runtime's bounded default.
	ProviderLiveness LiveLivenessPolicy
	// FinishAfterResponse asks a finite invocation to stop after the first
	// completed assistant response following capture admission. Tool
	// continuations remain eligible; the live owner only finishes after the
	// assistant resumes and reaches a terminal message boundary.
	FinishAfterResponse bool
	// ExpectedResponses is the number of non-tool response terminals required
	// before a finite invocation may finish. It is used by ordered multi-turn
	// media feeds; zero retains the single-response behavior.
	ExpectedResponses int
}
