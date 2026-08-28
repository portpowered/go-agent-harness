package wire

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// TestGeneratedRootCLI_WebRTCRejectsBeforeProductionSideEffects proves that
// the shipped graph rejects an otherwise valid WebRTC request before any
// customer-unreachable signaling, peer/media setup, provider connection, or
// device/runtime side effect can occur.
func TestGeneratedRootCLI_WebRTCRejectsBeforeProductionSideEffects(t *testing.T) {
	var signalingCalls, dataPlaneCalls, mediaSourceCalls int
	components := services.SessionRTCComponents{
		ResolveSignaling: func(context.Context, string) (rtc.Signaling, error) {
			signalingCalls++
			return nil, errors.New("signaling resolver should not be reached")
		},
		NewDataPlane: func(context.Context, rtc.Signaling) (services.SessionRTCDataPlane, error) {
			dataPlaneCalls++
			return nil, errors.New("RTC data-plane factory should not be reached")
		},
		OpenMediaSource: func(context.Context, string) (rtc.InboundMedia, error) {
			mediaSourceCalls++
			return nil, errors.New("media-source opener should not be reached")
		},
	}
	provider := &recordingSessionInferencer{}
	transportDialer := &recordingDialer{}
	registry := &recordingDeviceRegistry{}
	audioSource := &recordingAudioSource{}
	audioSink := &recordingAudioSink{}
	toolExecutor := &recordingToolExecutor{}
	app, err := ComposeAgentCLI(
		toolExecutor,
		transportDialer,
		registry,
		audioSource,
		audioSink,
		&recordingClock{},
		WithSessionInferencer(provider),
		WithSessionRTCComponents(components),
	)
	if err != nil {
		t.Fatalf("compose generated root CLI: %v", err)
	}

	var stdout, stderr bytes.Buffer
	root := app.Generate()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SilenceUsage = true
	root.SetArgs([]string{
		"--config-dir", filepath.Join(t.TempDir(), "missing-config"),
		"session",
		"--record", filepath.Join(t.TempDir(), "must-not-be-created.session.json"),
		"--provider", "grok",
		"--model", "grok-customer-boundary",
		"--api-key", "test-provider-key",
		"--transport", "webrtc",
		"--signaling", "loopback://customer-boundary",
		"--media-source", "fixture://customer-boundary",
		"--audio-in-device", "recording:input",
		"--audio-out-device", "recording:output",
		"complete the customer-boundary turn",
	})

	err = root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("generated root WebRTC session unexpectedly succeeded")
	}
	if !errors.Is(err, cli.ErrSessionWebRTCUnavailable) {
		t.Fatalf("generated root WebRTC error = %v, want customer capability error", err)
	}
	for _, want := range []string{
		"customer-reachable network signaling",
		"spoken-audio input",
		"--transport ws",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("generated root WebRTC error %q missing %q", err, want)
		}
	}
	if signalingCalls != 0 || dataPlaneCalls != 0 || mediaSourceCalls != 0 {
		t.Fatalf("RTC component calls = signaling:%d data-plane:%d media:%d, want zero", signalingCalls, dataPlaneCalls, mediaSourceCalls)
	}
	if provider.connects != 0 {
		t.Fatalf("provider session connects = %d, want zero", provider.connects)
	}
	if transportDials := transportDialer.dials.Load(); transportDials != 0 {
		t.Fatalf("transport dials = %d, want zero", transportDials)
	}
	if registry.lookups != 0 {
		t.Fatalf("device registry lookups = %d, want zero", registry.lookups)
	}
	if audioSource.reads != 0 || audioSink.writes != 0 {
		t.Fatalf("audio I/O = reads:%d writes:%d, want zero", audioSource.reads, audioSink.writes)
	}
	if toolExecutor.calls != 0 {
		t.Fatalf("tool executions = %d, want zero", toolExecutor.calls)
	}
	if strings.Contains(strings.ToLower(stdout.String()+stderr.String()), "usage:") {
		t.Fatalf("customer capability rejection emitted help: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
