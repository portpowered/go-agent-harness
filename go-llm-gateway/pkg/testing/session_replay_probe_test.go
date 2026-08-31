package testing

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunSessionReplayProbeFullPassOverRecordedFixture(t *testing.T) {
	fixture := SharedSessionFixturePath("session_healthy_multiturn_audio.session.json")

	report, err := RunSessionReplayProbe(context.Background(), fixture)
	if err != nil {
		t.Fatal(err)
	}

	if report.Provider != "grok" || report.Model != "grok-4-healthy-multiturn" {
		t.Fatalf("provider/model = %q/%q", report.Provider, report.Model)
	}
	if report.Provenance != SessionFixtureProvenanceSynthetic {
		t.Fatalf("provenance = %q, want %q", report.Provenance, SessionFixtureProvenanceSynthetic)
	}
	if report.InboundFrames != 10 || report.OutboundTicks != 1 {
		t.Fatalf("inbound frames/outbound ticks = %d/%d, want 10/1", report.InboundFrames, report.OutboundTicks)
	}
	wantObservations := []SessionProbeObservation{
		{Sequence: 1, Direction: DirectionClientToServer, Type: "session.update"},
		{Sequence: 2, Direction: DirectionServerToClient, Type: "session.created"},
		{Sequence: 3, Direction: DirectionServerToClient, Type: "response.created"},
		{Sequence: 4, Direction: DirectionServerToClient, Type: "response.audio.delta"},
		{Sequence: 5, Direction: DirectionServerToClient, Type: "response.audio_transcript.delta"},
		{Sequence: 6, Direction: DirectionServerToClient, Type: "response.audio.done"},
		{Sequence: 7, Direction: DirectionServerToClient, Type: "response.done"},
		{Sequence: 8, Direction: DirectionServerToClient, Type: "response.created"},
		{Sequence: 9, Direction: DirectionServerToClient, Type: "response.text.delta"},
		{Sequence: 10, Direction: DirectionServerToClient, Type: "response.done"},
		{Sequence: 11, Direction: DirectionServerToClient, Type: "session.closed"},
	}
	if !reflect.DeepEqual(report.Observations, wantObservations) {
		t.Fatalf("observations = %#v", report.Observations)
	}

	repeat, err := RunSessionReplayProbe(context.Background(), fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, repeat) {
		t.Fatal("repeated replay probe runs produced different reports")
	}
}

func TestRunSessionReplayProbeRejectsInvalidFixtureBeforeAnyObservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.session.json")
	invalidCapture := SessionCapture{
		Version:  SessionCaptureVersion,
		Provider: SessionProviderMetadata{Name: "grok", Model: "grok-realtime"},
		Records: []CapturedSessionEvent{
			{
				Sequence:    1,
				Direction:   DirectionServerToClient,
				TimestampMs: 0,
				Type:        "response.audio.delta",
				PayloadType: SessionPayloadTypeWebSocketMessage,
				Payload:     json.RawMessage(`{"type":"response.audio.delta","api_key":"sk-should-not-be-here"}`),
			},
		},
	}
	invalid, err := json.MarshalIndent(invalidCapture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, invalid, 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := RunSessionReplayProbe(context.Background(), path)
	if err == nil {
		t.Fatal("expected validation failure for invalid fixture")
	}
	if !containsAll(err.Error(), "session fixture validation failed before any probe observation", "fixture_provenance", "credential-like") {
		t.Fatalf("error = %v, want clear pre-observation validation error", err)
	}
	if report.InboundFrames != 0 || report.OutboundTicks != 0 || len(report.Observations) != 0 {
		t.Fatalf("report = %#v, want zero-value report when validation fails", report)
	}

	missing := filepath.Join(t.TempDir(), "absent.session.json")
	if _, err := RunSessionReplayProbe(context.Background(), missing); err == nil || !containsAll(err.Error(), "validation failed before any probe observation") {
		t.Fatalf("missing fixture error = %v, want validation failure before observations", err)
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}
