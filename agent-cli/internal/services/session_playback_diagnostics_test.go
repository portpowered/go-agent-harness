package services

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// recordingDiagnosticSink is a minimal SessionDiagnosticSink test double that
// records every record it receives, so a test can assert exactly what a real
// caller-supplied sink would have observed.
type recordingDiagnosticSink struct {
	records []SessionDiagnosticRecord
}

func (s *recordingDiagnosticSink) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	s.records = append(s.records, record)
}

// TestResolvePlaybackDiagnosticSink covers the structural fix's core
// contract: a caller-supplied sink is always used untouched, and a nil sink
// -- the exact shape of the #350/#360 omission -- resolves to a real,
// non-nil fallback instead of staying nil.
func TestResolvePlaybackDiagnosticSink(t *testing.T) {
	if resolved := resolvePlaybackDiagnosticSink(nil); resolved == nil {
		t.Fatal("resolvePlaybackDiagnosticSink(nil) = nil, want the package fallback sink")
	} else if resolved != fallbackPlaybackDiagnosticSink {
		t.Fatal("resolvePlaybackDiagnosticSink(nil) did not return the shared fallback sink")
	}

	sink := &recordingDiagnosticSink{}
	if resolved := resolvePlaybackDiagnosticSink(sink); resolved != SessionDiagnosticSink(sink) {
		t.Fatal("resolvePlaybackDiagnosticSink did not pass a caller-supplied sink through unchanged")
	}
}

// TestSessionPlaybackDiagnosticObserverResolvedNeverNil proves the specific
// wiring bug is closed at the function level: an observer built from a
// resolved nil sink is callable (never nil) and still reaches a real sink --
// here, the fallback -- carrying the dropped-sample count.
func TestSessionPlaybackDiagnosticObserverResolvedNeverNil(t *testing.T) {
	observer := sessionPlaybackDiagnosticObserver(resolvePlaybackDiagnosticSink(nil))
	if observer == nil {
		t.Fatal("sessionPlaybackDiagnosticObserver(resolvePlaybackDiagnosticSink(nil)) = nil, want a callable observer")
	}
	// The fallback logs rather than exposing a way to assert on it directly;
	// this call only needs to prove it does not panic on the un-configured
	// path a forgetful caller now falls back to.
	observer("virtual:output", audio.PlaybackQueueStats{DroppedSamples: 7, OverflowEvents: 1})

	sink := &recordingDiagnosticSink{}
	observer = sessionPlaybackDiagnosticObserver(resolvePlaybackDiagnosticSink(sink))
	observer("virtual:output", audio.PlaybackQueueStats{DroppedSamples: 7, OverflowEvents: 1})
	if len(sink.records) != 1 {
		t.Fatalf("caller-supplied sink recorded %d records, want 1", len(sink.records))
	}
	if sink.records[0].Fields[SessionDiagnosticFieldPlaybackDroppedSamples] != "7" {
		t.Fatalf("recorded fields = %+v, want dropped_samples=7", sink.records[0].Fields)
	}
}

// TestEmitRoomParticipantPlaybackOverflowDiagnostic covers the room-specific
// choke point directly: it must attach the dropping participant's ID and
// must resolve a nil sink through the same fallback as the RTC path, and it
// must stay silent when nothing actually overflowed.
func TestEmitRoomParticipantPlaybackOverflowDiagnostic(t *testing.T) {
	format := audio.DefaultDeviceFormat()
	queueCapacity, err := audio.PlaybackQueueCapacity(format, audio.DefaultPlaybackLatencyTarget)
	if err != nil {
		t.Fatalf("playback queue capacity: %v", err)
	}

	registry, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{
		Devices: []audio.VirtualDeviceConfig{
			{ID: "speaker", Name: "speaker", Direction: audio.DirectionOutput, LoopbackID: "speaker-drain"},
			{ID: "speaker-drain", Name: "speaker drain", Direction: audio.DirectionInput},
		},
	})
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	sink, err := audio.NewDeviceSink(registry, audio.DeviceID("virtual:speaker"))
	if err != nil {
		t.Fatalf("open device sink: %v", err)
	}
	t.Cleanup(func() { _ = sink.Close() })

	recorder := &recordingDiagnosticSink{}
	emitRoomParticipantPlaybackOverflowDiagnostic("customer", sink, recorder)
	if len(recorder.records) != 0 {
		t.Fatalf("emit fired with no overflow: %+v", recorder.records)
	}

	overflow := make([]int16, queueCapacity+audio.FrameSize)
	if err := sink.WriteSamples(context.Background(), overflow); err != nil {
		t.Fatalf("write overflowing samples: %v", err)
	}

	emitRoomParticipantPlaybackOverflowDiagnostic("customer", sink, recorder)
	if len(recorder.records) != 1 {
		t.Fatalf("caller-supplied sink recorded %d records after overflow, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.Event != SessionDiagnosticEventPlaybackOverflow {
		t.Fatalf("record event = %q, want %q", record.Event, SessionDiagnosticEventPlaybackOverflow)
	}
	if record.Fields[SessionDiagnosticFieldPlaybackParticipantID] != "customer" {
		t.Fatalf("record fields = %+v, want participant_id=customer", record.Fields)
	}
	if record.Fields[SessionDiagnosticFieldPlaybackDroppedSamples] == "0" {
		t.Fatal("record reports zero dropped samples after a deliberate overflow")
	}

	// A nil sink must still resolve to the shared fallback rather than being
	// silently skipped, exactly like the RTC path above.
	emitRoomParticipantPlaybackOverflowDiagnostic("customer", sink, nil)
}

// TestPlanSessionRuntimePlaybackObserverNonNilAcrossConstructionPaths is the
// mandatory regression test asserting that every non-test construction path
// for SessionRunOptions yields a non-nil playback observer once planned,
// closing the class of bug (not just the two call sites already found) --
// see resolvePlaybackDiagnosticSink's doc comment. Each case below builds its
// SessionRunOptions the same way the real, corresponding production call
// site does (self-play's own builder function, and the room package's actual
// per-participant plan for a live and a replay participant), deliberately
// leaving Diagnostics unset exactly as that call site does today, then
// re-plans a hermetic copy (ReplayPath + an injected SessionInferencer, the
// same seam session_interactive_policy_test.go already uses) and asserts the
// resulting plan's RTC playback observer is non-nil regardless.
func TestPlanSessionRuntimePlaybackObserverNonNilAcrossConstructionPaths(t *testing.T) {
	cases := []struct {
		name string
		opts SessionRunOptions
	}{
		{
			name: "generic minimal caller (a hypothetical future construction site)",
			opts: SessionRunOptions{},
		},
		{
			name: "self-play (services.selfPlaySessionRunOptions)",
			opts: selfPlaySessionRunOptions(SelfPlayRunOptions{}),
		},
		{
			name: "room live participant (services.buildRoomParticipantPlans)",
			opts: capturedRoomParticipantOptions(t),
		},
		{
			// Mirrors the SessionRunOptions literal built for a room replay
			// participant in buildRoomReplayParticipantPlans
			// (session_room_planning.go); Diagnostics is not among the fields
			// that function sets today.
			name: "room replay participant (services.buildRoomReplayParticipantPlans shape)",
			opts: SessionRunOptions{
				Provider:       "openai",
				Model:          "gpt-realtime",
				ModelProvided:  true,
				Prompt:         "hello",
				PromptProvided: true,
				WaitForClose:   false,
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.opts.Diagnostics != nil {
				t.Fatal("test case setup error: this case must leave Diagnostics unset to exercise the default")
			}
			opts := testCase.opts
			opts.ReplayPath = "unused.json"
			opts.SessionInferencer = stubPlanSessionInferencer{}

			plan, err := planSessionRuntime(opts)
			if err != nil {
				t.Fatalf("planSessionRuntime: %v", err)
			}
			if plan.rtcDeviceRequest.PlaybackObserver == nil {
				t.Fatal("plan.rtcDeviceRequest.PlaybackObserver = nil despite an unset SessionRunOptions.Diagnostics; the class-level default did not apply")
			}
		})
	}
}

// capturedRoomParticipantOptions returns the exact SessionRunOptions
// buildRoomParticipantPlans constructs today for a live (non-replay,
// non-human) room participant, by running the real production function with
// an injected SessionInferencer so no network or credential is needed. This
// stays honest to source drift: if session_room_planning.go ever starts
// setting Diagnostics, this helper's caller (Diagnostics != nil guard above)
// fails loudly instead of silently testing a stale shape.
func capturedRoomParticipantOptions(t *testing.T) SessionRunOptions {
	t.Helper()
	opts := RoomRunOptions{
		Manifest: room.Manifest{
			SchemaVersion: room.SchemaVersion,
			Room:          room.Room{Interactive: true},
			Participants: []room.Participant{
				{
					Kind:         room.ParticipantKindAgent,
					ID:           "agent",
					SystemPrompt: "provider agent",
					Provider:     "test-provider",
					Model:        "test-model",
					APIKeyEnv:    "ROOM_AGENT_KEY",
					Tools:        []string{},
				},
			},
		},
		CredentialLookup: func(string) (string, bool) { return "secret", true },
		SessionInferencers: map[string]messages.SessionInferencer{
			"agent": stubPlanSessionInferencer{},
		},
	}
	plans, _, err := buildRoomParticipantPlans(opts, room.ValidationOptions{LookupCredential: opts.CredentialLookup})
	if err != nil {
		t.Fatalf("buildRoomParticipantPlans: %v", err)
	}
	if len(plans) != 1 || plans[0].startupErr != nil {
		t.Fatalf("buildRoomParticipantPlans plans = %+v, want one clean agent plan", plans)
	}
	return plans[0].options
}
