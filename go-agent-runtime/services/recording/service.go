// Package recording owns capture admission and durable finalization.
package recording

import (
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

var (
	// ErrLiveEvidenceClosed identifies an observation submitted after evidence
	// finalization began.
	ErrLiveEvidenceClosed = errors.New("live recording evidence is closed")
	// ErrLiveEvidenceClaimed identifies a destination owned by another live
	// invocation.
	ErrLiveEvidenceClaimed = errors.New("live recording destination is already claimed")
)

// Writer finalizes one capture outside the agent tick. Implementations retain
// the original persistence error for callers to report incomplete evidence.
type Writer interface{ FlushToFile(string) error }

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
