package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// SealReplayCapture finalizes the protected capture envelope used by replay.
func SealReplayCapture(capture gatewaytesting.SessionCapture) (gatewaytesting.SessionCapture, error) {
	if capture.Version == 0 || capture.Version == gatewaytesting.SessionCaptureLegacyVersion {
		capture.Version = gatewaytesting.SessionCaptureVersion
	}
	if capture.Version != gatewaytesting.SessionCaptureVersion {
		return gatewaytesting.SessionCapture{}, &gatewaytesting.SessionCaptureValidationError{
			Classification: gatewaytesting.SessionCaptureErrorClassUnsupportedVersion,
			FieldPath:      "/version",
			Expected:       strconv.Itoa(gatewaytesting.SessionCaptureVersion),
			Actual:         strconv.Itoa(capture.Version),
			Err:            gatewaytesting.ErrSessionCaptureUnsupportedVersion,
		}
	}
	if capture.Records == nil {
		capture.Records = make([]gatewaytesting.CapturedSessionEvent, 0)
	}
	digest, err := ComputeReplayCaptureDigest(capture)
	if err != nil {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("compute session capture digest: %w", err)
	}
	capture.Integrity = gatewaytesting.SessionCaptureIntegrity{
		Algorithm: gatewaytesting.SessionCaptureIntegrityAlgorithm,
		Coverage:  gatewaytesting.SessionCaptureIntegrityCoverage,
		Digest:    digest,
	}
	return capture, nil
}

// ComputeReplayCaptureDigest hashes the stable protected capture coverage.
func ComputeReplayCaptureDigest(capture gatewaytesting.SessionCapture) (string, error) {
	type coverageEnvelope struct {
		Version            int                                    `json:"version"`
		Provider           gatewaytesting.SessionProviderMetadata `json:"provider"`
		Session            gatewaytesting.SessionMetadata         `json:"session"`
		Records            []gatewaytesting.CapturedSessionEvent  `json:"records"`
		EndsWithDisconnect bool                                   `json:"ends_with_disconnect,omitempty"`
	}
	coverage, err := json.Marshal(coverageEnvelope{
		Version:            capture.Version,
		Provider:           capture.Provider,
		Session:            capture.Session,
		Records:            capture.Records,
		EndsWithDisconnect: capture.EndsWithDisconnect,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(coverage)
	return hex.EncodeToString(digest[:]), nil
}
