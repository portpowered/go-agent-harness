// Package replay defines artifact admission and replay planning for hosts.
package replay

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

var ErrCaptureUnavailable = errors.New("replay capture is unavailable")

// CaptureKind identifies the protocol represented by an admitted capture.
// Turn captures are consumed by the ordinary session replay service; realtime
// captures contain provider WebSocket traffic and can drive LiveRunner.
type CaptureKind string

const (
	CaptureKindTurn     CaptureKind = "turn"
	CaptureKindRealtime CaptureKind = "realtime"
)

// CaptureInspection is the replay service's complete admission result for a
// capture path. Hosts consume this typed result instead of opening the file,
// probing JSON payloads, or deriving provider metadata themselves.
type CaptureInspection struct {
	SourcePath       string
	CapturePath      string
	Kind             CaptureKind
	Provider         string
	Model            string
	IntegrityWarning string
	LivePlan         *session.LiveReplayPlan
}

// IsRealtime reports whether the admitted capture can drive a continuous
// provider session.
func (i CaptureInspection) IsRealtime() bool { return i.Kind == CaptureKindRealtime }

// Service constructs bounded replay actions from explicit capture artifacts.
// Execution and device attachment remain owned by the session service.
type Service interface {
	// InspectCapture validates and classifies a raw capture or finalized
	// recording directory, returning provider metadata and any self-driving
	// live plan. The returned paths are safe for the provider replay adapter.
	InspectCapture(context.Context, string) (CaptureInspection, error)
	LoadLivePlan(context.Context, string) (session.LiveReplayPlan, error)
	// ResolveCapturePath admits either a raw provider capture or a finalized
	// recording directory. Directory admission verifies the manifest, complete
	// status, provider artifact path, and artifact digest before returning the
	// raw capture path to the provider service.
	ResolveCapturePath(context.Context, string) (string, error)
}
