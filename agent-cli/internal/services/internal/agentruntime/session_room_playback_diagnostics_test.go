package agentruntime

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// TestRunRoom_HumanParticipantPlaybackOverflowNamesParticipant is the
// mandatory regression test for the playback-overflow diagnostic being
// structurally incapable of firing in a room run: a room human participant
// owns a raw *audio.DeviceSink directly (see openRoomHumanDevices in
// session_room_orchestration.go) and never builds a SessionRunOptions or
// calls planSessionRuntime, so sessionPlaybackDiagnosticObserver -- and the
// SessionRunOptions.Diagnostics wiring it depends on -- never applied to a
// room's human speaker device at all, regardless of what the CLI, self-play,
// or any other caller did with SessionRunOptions.Diagnostics.
//
// This test FAILS against unmodified main: nothing on that tree ever reads
// the customer's runtime.output.PlaybackStats(), so opts.OnDiagnostic never
// observes a session_playback_overflow event no matter how badly the queue
// overflows. The fix adds emitRoomParticipantPlaybackOverflowDiagnostic,
// called from roomCoordinator.finishParticipant right after the human
// output device is closed, using the same combined per-participant sink
// (runtime.diagnosticSink, set once in runRoomParticipant) that provider
// participants already use for their own session diagnostics.
//
// The customer's speaker device is deliberately paired with a loopback
// partner that is never opened, so nothing ever drains it; the room mixer
// emits a mixed frame on every cadence tick regardless of whether any
// participant has spoken (see PCM16Mixer.mixFrameWithSources), so ordinary
// room output alone -- no audio from the agent required -- is enough to
// overflow the bounded queue deterministically within the sleep below.
func TestRunRoom_HumanParticipantPlaybackOverflowNamesParticipant(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.VirtualBackendConfig{
		Devices: []devicegw.VirtualDeviceConfig{
			// The customer's microphone. Its loopback partner is never opened,
			// so ordinary room capture reads simply block; nothing about the
			// input side interacts with the speaker side below.
			{ID: "customer-mic", Name: "customer mic", Direction: devicegw.DirectionInput, LoopbackID: "customer-mic-drain"},
			{ID: "customer-mic-drain", Name: "customer mic drain", Direction: devicegw.DirectionOutput},
			// The customer's speaker. Its loopback partner ("customer-speaker-drain")
			// is registered (so the pair is valid and writes never fail with
			// ErrVirtualNoLoopback) but is never opened, so the shared
			// PlaybackQueue behind "customer-speaker" is never drained.
			{ID: "customer-speaker", Name: "customer speaker", Direction: devicegw.DirectionOutput, LoopbackID: "customer-speaker-drain"},
			{ID: "customer-speaker-drain", Name: "customer speaker drain", Direction: devicegw.DirectionInput},
		},
	})
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}

	inferencer := &roomTestInferencer{events: []messages.StreamMessage{roomTestSessionOpen("agent")}}
	opts := RoomRunOptions{
		Manifest: room.Manifest{
			SchemaVersion: room.SchemaVersion,
			Room:          room.Room{Interactive: true},
			Participants: []room.Participant{
				{
					Kind:         room.ParticipantKindHuman,
					ID:           "customer",
					SystemPrompt: "human customer",
					Tools:        []string{},
					InputDevice:  "virtual:customer-mic",
					OutputDevice: "virtual:customer-speaker",
				},
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
		CredentialLookup: func(name string) (string, bool) {
			if name == "ROOM_AGENT_KEY" {
				return "secret-room-key", true
			}
			return "", false
		},
		DeviceRegistry: registry,
		SessionInferencers: map[string]messages.SessionInferencer{
			"agent": inferencer,
		},
	}

	type diagnosticEvent struct {
		participantID string
		record        SessionDiagnosticRecord
	}
	diagnostics := make(chan diagnosticEvent, 8)
	opts.OnDiagnostic = func(participantID string, record SessionDiagnosticRecord) {
		if record.Event != SessionDiagnosticEventPlaybackOverflow {
			return
		}
		diagnostics <- diagnosticEvent{participantID: participantID, record: record}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan roomTestRunOutcome, 1)
	go func() {
		result, runErr := RunRoomWithResult(ctx, io.Discard, opts)
		resultCh <- roomTestRunOutcome{result: result, err: runErr}
	}()

	// Let the mixer's real-time cadence (20ms ticks; see
	// room.DefaultPCM16FrameDuration) accumulate comfortably more audio than
	// the customer's speaker queue can hold (its capacity is 250ms of 16kHz
	// mono audio -- see audio.DefaultPlaybackLatencyTarget) before teardown
	// takes the final snapshot the diagnostic is built from.
	time.Sleep(1500 * time.Millisecond)
	cancel()

	var event diagnosticEvent
	select {
	case event = <-diagnostics:
	case <-time.After(5 * time.Second):
		t.Fatal("no session_playback_overflow diagnostic observed for the customer participant")
	}

	if event.participantID != "customer" {
		t.Fatalf("playback overflow diagnostic delivered for participant %q, want customer", event.participantID)
	}
	if got := event.record.Fields[SessionDiagnosticFieldPlaybackParticipantID]; got != "customer" {
		t.Fatalf("playback overflow diagnostic fields %+v missing participant_id=customer, got %q", event.record.Fields, got)
	}
	dropped := event.record.Fields[SessionDiagnosticFieldPlaybackDroppedSamples]
	if dropped == "" || dropped == "0" {
		t.Fatalf("playback overflow diagnostic dropped_samples = %q, want a positive count", dropped)
	}
	if got := event.record.Fields[SessionDiagnosticFieldPlaybackDeviceID]; got == "" {
		t.Fatalf("playback overflow diagnostic missing device_id: %+v", event.record.Fields)
	}

	select {
	case outcome := <-resultCh:
		if outcome.err != nil {
			t.Fatalf("room run: %v", outcome.err)
		}
		if outcome.result.Reason != RoomTerminationStopped {
			t.Fatalf("room result reason = %v, want stopped", outcome.result.Reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("room did not terminate after cancellation")
	}
}
