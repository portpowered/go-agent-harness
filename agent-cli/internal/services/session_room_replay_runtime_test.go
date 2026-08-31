package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestBuildRoomReplayParticipantPlansBypassesLiveSeams(t *testing.T) {
	bundle, _ := writeRoomReplayBundle(t)
	replay, err := LoadRoomReplayPlan(bundle)
	if err != nil {
		t.Fatalf("LoadRoomReplayPlan: %v", err)
	}
	replay.Participants[0].OpeningPrompt = "alpha opening"
	replay.Participants[0].Voice = "cedar"

	var credentialLookups atomic.Int32
	var liveFactories atomic.Int32
	var dialerFactories atomic.Int32
	var capabilityFactories atomic.Int32
	forbiddenCredentialLookup := func(string) (string, bool) {
		credentialLookups.Add(1)
		return "live-secret", true
	}
	forbiddenSessionFactory := func(room.Participant, SessionRunOptions) (messages.SessionInferencer, error) {
		liveFactories.Add(1)
		return nil, errors.New("live session factory called")
	}

	plans, secrets, err := buildRoomParticipantPlansWithContext(context.Background(), RoomRunOptions{
		// The live manifest is intentionally unusable. Replay planning must use
		// only the admitted plan projection below.
		Manifest:         room.Manifest{SchemaVersion: 999},
		ReplayPlan:       &replay,
		CredentialLookup: forbiddenCredentialLookup,
		SessionFactory:   forbiddenSessionFactory,
		WebSocketDialerFactory: func(room.Participant) transport.Dialer {
			dialerFactories.Add(1)
			return nil
		},
		ToolCapabilitiesFactory: func(room.Participant) (RoomParticipantToolCapabilities, error) {
			capabilityFactories.Add(1)
			return RoomParticipantToolCapabilities{}, nil
		},
		BrowserCapabilitiesFactory: func(room.Participant) (RoomParticipantBrowserCapabilities, error) {
			capabilityFactories.Add(1)
			return RoomParticipantBrowserCapabilities{}, nil
		},
	}, room.ValidationOptions{LookupCredential: forbiddenCredentialLookup})
	if err != nil {
		t.Fatalf("buildRoomParticipantPlansWithContext: %v", err)
	}
	if len(secrets) != 0 {
		t.Fatalf("replay secrets = %q, want none", secrets)
	}
	if len(plans) != len(replay.Participants) {
		t.Fatalf("replay plans = %d, want %d", len(plans), len(replay.Participants))
	}
	if credentialLookups.Load() != 0 || liveFactories.Load() != 0 || dialerFactories.Load() != 0 || capabilityFactories.Load() != 0 {
		t.Fatalf("live seams called: credentials=%d session-factory=%d dialer-factory=%d capabilities=%d", credentialLookups.Load(), liveFactories.Load(), dialerFactories.Load(), capabilityFactories.Load())
	}
	if plans[0].options.ReplayPath != replay.Participants[0].CapturePath || plans[0].options.APIKey != "" {
		t.Fatalf("alpha replay options = %+v, want bundle capture and no API key", plans[0].options)
	}
	if plans[0].options.Prompt != "alpha opening" || !plans[0].options.PromptProvided || plans[0].options.Voice != "cedar" {
		t.Fatalf("alpha prompt/voice options = %+v", plans[0].options)
	}
	if !plans[0].replay || !plans[1].replay || plans[0].tracker == nil || plans[1].tracker == nil {
		t.Fatalf("replay plans missing isolated tracker state: %+v / %+v", plans[0], plans[1])
	}
	if plans[0].replayLoop.Done == nil || plans[0].replayLoop.DoneErr == nil || plans[1].replayLoop.Done == nil || plans[1].replayLoop.DoneErr == nil {
		t.Fatalf("replay plans missing strict terminal seams: %+v / %+v", plans[0].replayLoop, plans[1].replayLoop)
	}
	if plans[0].replayLoop.Done == plans[1].replayLoop.Done {
		t.Fatal("participants share a replay completion channel")
	}
	if plans[0].replayLoop.MaxDuration != 0 || plans[1].replayLoop.MaxDuration != 0 {
		t.Fatalf("replay max durations = %s/%s, want zero capture-bounded runtime", plans[0].replayLoop.MaxDuration, plans[1].replayLoop.MaxDuration)
	}
}

func TestRunRoomWithResultReplaysAdmittedBundleWithoutLiveConfiguration(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)
	model := "gpt-realtime"
	for _, participantID := range []string{"alpha", "beta"} {
		capture := roomRealtimeReplayCapture(model,
			roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, participantID+" system")),
			roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
				"type": "session.created", "session": map[string]any{"id": "sess-" + participantID, "model": model},
			})),
			roomRealtimeReplayEvent(3, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.created", "response": map[string]any{"id": "resp-" + participantID},
			})),
			roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.output_text.delta", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.output_text.delta", "delta": participantID + " response",
			})),
			roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_text.done", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.output_text.done",
			})),
			roomRealtimeReplayEvent(6, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.done",
			})),
		)
		writeRoomReplayRuntimeCapture(t, bundle, manifest, participantID, capture)
	}
	writeManifestValue(t, bundle, manifest)

	var credentialLookups atomic.Int32
	var liveFactories atomic.Int32
	var dialerFactories atomic.Int32
	roomCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := RunRoomWithResult(roomCtx, nil, RoomRunOptions{
		Manifest:   room.Manifest{SchemaVersion: 999},
		ReplayPath: bundle,
		CredentialLookup: func(string) (string, bool) {
			credentialLookups.Add(1)
			return "live-secret", true
		},
		SessionFactory: func(room.Participant, SessionRunOptions) (messages.SessionInferencer, error) {
			liveFactories.Add(1)
			return nil, errors.New("live session factory called")
		},
		WebSocketDialerFactory: func(room.Participant) transport.Dialer {
			dialerFactories.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunRoomWithResult replay: %v", err)
	}
	if result.Reason != RoomTerminationStopped {
		t.Fatalf("room replay reason = %q, want stopped after captured terminal boundaries", result.Reason)
	}
	if len(result.Participants) != 2 {
		t.Fatalf("room replay participants = %+v, want two results", result.Participants)
	}
	for _, participantID := range []string{"alpha", "beta"} {
		participant, ok := result.Participants[participantID]
		if !ok || !participant.Connected || participant.TerminationReason == ParticipantTerminationError {
			t.Fatalf("room replay participant %q = %+v, want connected non-error result", participantID, participant)
		}
	}
	if credentialLookups.Load() != 0 || liveFactories.Load() != 0 || dialerFactories.Load() != 0 {
		t.Fatalf("room replay called live seams: credentials=%d factories=%d dialers=%d", credentialLookups.Load(), liveFactories.Load(), dialerFactories.Load())
	}
}

func writeRoomReplayRuntimeCapture(t *testing.T, bundle string, manifest map[string]any, participantID string, capture gwtesting.SessionCapture) {
	t.Helper()
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal participant %q capture: %v", participantID, err)
	}
	relative := filepath.ToSlash(filepath.Join("participants", participantID, "session.session.json"))
	path := filepath.Join(bundle, filepath.FromSlash(relative))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write participant %q capture: %v", participantID, err)
	}
	participant := manifest["participants"].(map[string]any)[participantID].(map[string]any)
	artifacts := participant["artifacts"].(map[string]any)
	digest := sha256.Sum256(data)
	artifacts["capture"] = map[string]any{
		"path":   relative,
		"size":   len(data),
		"sha256": hex.EncodeToString(digest[:]),
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat participant %q capture: %v", participantID, err)
	}
	if relative == "" {
		t.Fatalf("participant %q capture path is empty", participantID)
	}
}
