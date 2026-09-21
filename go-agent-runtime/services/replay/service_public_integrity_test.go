package replay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	replaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestProbeDocumentRejectsCaptureWithCorruptIntegrity(t *testing.T) {
	capture, err := gatewaytesting.SealSessionCapture(gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime"},
		Session: gatewaytesting.SessionMetadata{
			ID:                "integrity-regression",
			FixtureProvenance: gatewaytesting.SessionFixtureProvenanceSynthetic,
		},
		Records: []gatewaytesting.CapturedSessionEvent{},
	})
	if err != nil {
		t.Fatalf("seal capture: %v", err)
	}
	document, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	badDigest := strings.Repeat("0", 64)
	if badDigest == capture.Integrity.Digest {
		badDigest = strings.Repeat("1", 64)
	}
	document = bytes.Replace(document, []byte(capture.Integrity.Digest), []byte(badDigest), 1)
	if bytes.Contains(document, []byte(capture.Integrity.Digest)) {
		t.Fatal("capture digest was not corrupted")
	}

	service := replaywire.NewService()
	for _, method := range []struct {
		name string
		run  func() error
	}{
		{
			name: "inspect",
			run: func() error {
				_, err := service.InspectProbeDocument(context.Background(), "corrupt.session.json", document)
				return err
			},
		},
		{
			name: "analyze",
			run: func() error {
				_, err := service.AnalyzeProbeDocument(context.Background(), "corrupt.session.json", document)
				return err
			},
		},
	} {
		t.Run(method.name, func(t *testing.T) {
			err := method.run()
			if !errors.Is(err, gatewaytesting.ErrSessionCaptureIntegrity) {
				t.Fatalf("error = %v, want session capture integrity failure", err)
			}
		})
	}
}
