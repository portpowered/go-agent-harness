package agentruntime

import (
	"strings"

	captureclaim "github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim"
	captureclaimwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim/wire"
)

const sessionRecordingClaimSuffix = captureclaim.Suffix

// Deprecated: use captureclaim.ErrDestinationOccupied from the runtime
// service contract.
var ErrSessionRecordingDestinationOccupied = captureclaim.ErrDestinationOccupied

// Deprecated: use captureclaim.ErrDestinationClaimed from the runtime service
// contract.
var ErrSessionRecordingDestinationClaimed = captureclaim.ErrDestinationClaimed

// Deprecated: use captureclaim.ErrClaimLost from the runtime service contract.
var ErrSessionRecordingClaimLost = captureclaim.ErrClaimLost

// Deprecated: use captureclaim.ClaimHolder or captureclaim.ClaimError.
type SessionRecordingClaimHolder = captureclaim.ClaimHolder

type SessionRecordingClaimError = captureclaim.ClaimError

// sessionRecordingClaim is a decision-free adapter for the legacy private option field and method names.
type sessionRecordingClaim struct {
	path  string
	claim captureclaim.Claim
}

func (c *sessionRecordingClaim) publish(flush func(string) error) error {
	if c == nil || c.claim == nil {
		return captureclaim.ErrClaimLost
	}
	return c.claim.Publish(flush)
}

func (c *sessionRecordingClaim) release() error {
	if c == nil || c.claim == nil {
		return nil
	}
	return c.claim.Release()
}

// ensureSessionRecordingClaim retains one service-owned claim across nested
// CLI wrappers without moving any capture policy back into the CLI package.
func ensureSessionRecordingClaim(opts *SessionRunOptions) (*sessionRecordingClaim, error) {
	if opts == nil || strings.TrimSpace(opts.RecordPath) == "" {
		return nil, nil
	}
	if opts.recordingClaim != nil {
		opts.RecordPath = opts.recordingClaim.path
		return opts.recordingClaim, nil
	}
	claim, err := acquireSessionRecordingClaim(opts.RecordPath)
	if err != nil {
		return nil, err
	}
	opts.RecordPath = claim.path
	opts.recordingClaim = claim
	return claim, nil
}

func acquireSessionRecordingClaim(path string) (*sessionRecordingClaim, error) {
	claim, err := captureclaimwire.NewService(captureclaimwire.Dependencies{}).Acquire(path)
	if err != nil {
		return nil, err
	}
	return &sessionRecordingClaim{path: claim.Path(), claim: claim}, nil
}

func readSessionRecordingClaimHolder(path string) *SessionRecordingClaimHolder {
	return captureclaimwire.NewService(captureclaimwire.Dependencies{}).ObserveHolder(path)
}
