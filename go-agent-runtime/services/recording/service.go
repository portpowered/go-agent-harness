// Package recording owns capture admission and durable finalization.
package recording

import (
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

var (
	// ErrLiveEvidenceClosed identifies an observation submitted after evidence
	// finalization began.
	ErrLiveEvidenceClosed = errors.New("live recording evidence is closed")
	// ErrLiveEvidenceClaimed identifies a destination owned by another live
	// invocation.
	ErrLiveEvidenceClaimed = errors.New("live recording destination is already claimed")
)

// ResourceLimits bounds cumulative evidence retained by one recording
// invocation. Zero keeps the protected service default; positive values may
// only make a budget smaller. Queue limits remain separate admission
// controls, so draining a queue never refunds committed recording capacity.
// Byte limits include the encoded on-disk representation and its framing.
type ResourceLimits struct {
	TranscriptBytes int64
	TranscriptItems int64
	AudioBytes      int64
	AudioItems      int64
	SidecarBytes    int64
	SidecarItems    int64
	MetadataBytes   int64
	MetadataItems   int64
	TerminalBytes   int64
	TerminalItems   int64
	ProviderBytes   int64
	ProviderItems   int64
}

// ResourceUsage is an optional read-only snapshot of one invocation's bounded
// evidence accounting. Queue fields describe transient admission backlog;
// cumulative fields describe accepted on-disk evidence and are not refunded
// when the queue drains. Summary fields are the conservative retained-memory
// charge for the conversation projection.
//
// Implementations may expose this through ResourceUsageReporter without
// changing the LiveRecorder or ProviderCaptureSink contracts.
type ResourceUsage struct {
	QueueBytes             int64
	QueueItems             int64
	PeakQueueBytes         int64
	PeakQueueItems         int64
	AcceptedItems          int64
	ProcessedItems         int64
	AcceptedMessages       int64
	AcceptedAudio          int64
	AcceptedEvents         int64
	TranscriptBytes        int64
	TranscriptItems        int64
	AudioBytes             int64
	AudioItems             int64
	SidecarBytes           int64
	SidecarItems           int64
	MetadataBytes          int64
	MetadataItems          int64
	TerminalBytes          int64
	TerminalItems          int64
	SummaryBytes           int64
	SummaryItems           int64
	PeakSummaryBytes       int64
	PeakSummaryItems       int64
	ProviderQueueBytes     int64
	ProviderQueueItems     int64
	PeakProviderQueueBytes int64
	PeakProviderQueueItems int64
	ProviderAcceptedItems  int64
	ProviderBytes          int64
	ProviderItems          int64
	PeakProviderBytes      int64
	PeakProviderItems      int64
}

// ResourceUsageReporter optionally reports bounded evidence accounting. It is
// intentionally separate from the recording lifecycle interfaces so existing
// embedders do not need to implement diagnostics.
type ResourceUsageReporter interface {
	ResourceUsage() ResourceUsage
}

// The defaults are intentionally finite on the public constructors. The
// terminal budget is independent from data budgets and preserves bounded
// lifecycle evidence after a data budget is exhausted.
const (
	DefaultTranscriptBytes int64 = 64 << 20
	DefaultTranscriptItems int64 = 1 << 20
	DefaultAudioBytes      int64 = 64 << 20
	DefaultAudioItems      int64 = 1 << 20
	DefaultSidecarBytes    int64 = 256 << 10
	DefaultSidecarItems    int64 = 64
	DefaultMetadataBytes   int64 = 4 << 20
	DefaultMetadataItems   int64 = 4096
	DefaultProviderBytes   int64 = 64 << 20
	DefaultProviderItems   int64 = 1 << 20
	DefaultTerminalBytes   int64 = 128 << 10
	DefaultTerminalItems   int64 = 16
)

// Writer finalizes one capture outside the agent tick. Implementations retain
// the original persistence error for callers to report incomplete evidence.
type Writer interface{ FlushToFile(string) error }

// ProviderCaptureOptions selects one bounded raw provider capture. The
// destination is admitted before provider I/O starts; credentials and other
// secrets never cross this boundary or appear in capture errors.
type ProviderCaptureOptions struct {
	Destination string
	// Limits uses the same protected defaults as live semantic evidence.
	// Smaller values are useful for deterministic overflow tests.
	Limits ResourceLimits
}

// ProviderCaptureSink admits raw provider events without doing filesystem
// work on the provider goroutine. Abort releases an admitted but unstarted
// provider session when provider construction fails. FlushToFile publishes a
// verified capture after all admitted events have drained.
type ProviderCaptureSink interface {
	gatewaytesting.SessionCaptureSink
}

// ProviderCaptureService is a separate composition role for raw provider
// capture. Runtime provider builders receive it explicitly; they do not
// discover it through optional type assertions on the semantic recorder.
type ProviderCaptureService interface {
	OpenProviderCapture(ProviderCaptureOptions) (ProviderCaptureSink, error)
}

// SessionCapture joins finalization after the underlying session terminates.
// Construction is inert. ConnectSession admits at most one provider session.
type SessionCapture interface {
	messages.SessionInferencer
	FlushCapture() error
}

// LiveEvidenceOptions contains host-resolved recording metadata. The
// recording service owns destination validation, artifact naming, redaction,
// and final publication; the CLI only resolves the destination and supplies
// non-secret metadata.
type LiveEvidenceOptions struct {
	Destination    string
	SessionID      string
	ParticipantID  string
	Provider       string
	Model          string
	ClockBase      time.Time
	WallClockStart time.Time
	Credentials    []string
	// ProviderCapturePath is an optional explicit raw-capture destination, such
	// as a separately requested --record file. Empty uses the private spool.
	ProviderCapturePath string
	// DisableProviderCaptureSidecar prevents a replayed provider capture from
	// claiming the semantic sibling already owned by the source invocation.
	// The raw ProviderCapturePath remains available for immutable bundle
	// evidence, but no new terminal sidecar is written beside it.
	DisableProviderCaptureSidecar bool
	// Limits bounds cumulative service-owned evidence. Zero fields retain the
	// finite defaults declared above; larger values are capped at those
	// defaults so callers cannot disable protection accidentally.
	Limits ResourceLimits
}

// ProviderCapture is the optional composition port used to direct the provider
// capture writer into the same evidence archive. The file must be finalized
// before the recorder's Finalize call. Absence is recorded, never fabricated.
type ProviderCapture interface{ ProviderCapturePath() string }

// Service owns capture lifetime. Provider adapters supply a protocol writer;
// finalization is independent of the provider and of the CLI host.
type Service interface {
	TrackSession(messages.SessionInferencer, Writer, string) (SessionCapture, error)
	OpenLiveEvidence(LiveEvidenceOptions) (session.LiveRecorder, error)
	// OpenLiveSemanticEvidence creates the semantic lifecycle sidecar associated
	// with an explicitly requested provider capture. The recording service owns
	// the sibling artifact path and writes only normalized runtime observations;
	// it never changes or synthesizes the provider capture.
	OpenLiveSemanticEvidence(string) (session.LiveRecorder, error)
}
