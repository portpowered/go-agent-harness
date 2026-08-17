package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestMediaProbeCommandRendersDeterministicCapabilityReport(t *testing.T) {
	var gotContext context.Context
	probe := func(ctx context.Context, raw string) (rtc.MediaCapabilities, error) {
		gotContext = ctx
		if raw != "rtsp://camera:secret@host:554/main" {
			t.Fatalf("probe URL = %q", raw)
		}
		return rtc.MediaCapabilities{Source: "rtsp://camera:<redacted>@host:554/main", AudioCodec: "PCMU", SampleRate: 8000, Channels: 1, Video: true}, nil
	}
	command := NewMediaProbeCommand(probe)
	command.Timeout = time.Second
	var out bytes.Buffer
	if err := command.Run(context.Background(), &out, "rtsp://camera:secret@host:554/main"); err != nil {
		t.Fatal(err)
	}
	if gotContext == nil {
		t.Fatal("probe did not receive a context")
	}
	want := "Source: rtsp://camera:<redacted>@host:554/main\nAudio codec: PCMU\nSample rate: 8000\nChannels: 1\nVideo presence: true\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
	if strings.Contains(out.String(), "secret") {
		t.Fatal("probe output leaked the credential")
	}
}

func TestMediaCommandRegistersProbeSubcommand(t *testing.T) {
	command := NewMediaCommand(func(context.Context, string) (rtc.MediaCapabilities, error) {
		return rtc.MediaCapabilities{}, nil
	}).Generate()
	probe, _, err := command.Find([]string{"probe"})
	if err != nil || probe == nil || probe.Use != "probe <url>" {
		t.Fatalf("probe command = %#v, error = %v", probe, err)
	}
}

func TestMediaProbeCommandPreservesTypedSourceError(t *testing.T) {
	want := &rtc.MediaSourceError{Kind: rtc.SourceErrorAuthentication, Source: "rtsp://camera:<redacted>@host:554/main"}
	command := NewMediaProbeCommand(func(context.Context, string) (rtc.MediaCapabilities, error) {
		return rtc.MediaCapabilities{}, want
	})
	err := command.Run(context.Background(), &bytes.Buffer{}, "rtsp://camera:secret@host:554/main")
	if !errors.Is(err, rtc.ErrSourceAuthentication) {
		t.Fatalf("error = %v, want authentication identity", err)
	}
	if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), rtc.RedactionMarker) {
		t.Fatalf("safe error = %v", err)
	}
}

func TestMediaProbeCommandRejectsIncompleteEvidence(t *testing.T) {
	command := NewMediaProbeCommand(func(context.Context, string) (rtc.MediaCapabilities, error) {
		return rtc.MediaCapabilities{Source: "stub"}, nil
	})
	if err := command.Run(context.Background(), &bytes.Buffer{}, "stub"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("error = %v, want incomplete evidence failure", err)
	}
}
