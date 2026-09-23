package agentruntime

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	runtimedeviceswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// TestRunRoom_HumanParticipantPlaybackUsesServiceBackpressure verifies that
// room output flows through the bounded device service pump. A virtual speaker
// with no reader must not generate a false overflow diagnostic merely because
// the room mixer continues producing cadence frames while the room runs.
func TestRunRoom_HumanParticipantPlaybackUsesServiceBackpressure(t *testing.T) {
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
		AudioService: audioiowire.NewService(),
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
		DeviceService: runtimedeviceswire.NewService(registry, audioiowire.NewService()),
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

	// Run past the device queue's nominal latency target. The service pump
	// applies bounded backpressure to the room producer instead of overrunning
	// the device queue.
	time.Sleep(1500 * time.Millisecond)
	cancel()
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
	select {
	case event := <-diagnostics:
		t.Fatalf("bounded playback emitted unexpected overflow diagnostic for %s: %+v", event.participantID, event.record)
	default:
	}
}
