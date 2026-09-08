package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// TestRoomEvidenceAudioFormat_SatisfiesReplayPCMFormatSchema is the
// field-coverage regression guard for the writer/reader pcm_format contract:
// it marshals the exact roomEvidenceAudioFormat struct that
// session_room_evidence.go's writeManifest populates, embeds it under the
// same "audio_format" key the writer uses, and feeds those bytes into
// parseRoomReplayPCMFormat/validateRoomReplayPCMFormat -- the exact
// functions session_room_replay_manifest.go's LoadRoomReplayPlan calls to
// admit a bundle. If a future change adds a reader-required field without
// adding it to the writer struct (or the writer drops a field the reader
// still needs), this fails immediately, instead of only surfacing the next
// time someone happens to run a real record-then-replay round trip.
func TestRoomEvidenceAudioFormat_SatisfiesReplayPCMFormatSchema(t *testing.T) {
	written := roomEvidenceAudioFormat{
		SampleRate:      24000,
		Channels:        1,
		Encoding:        roomEvidenceAudioEncoding,
		SampleWidthBits: roomEvidenceAudioSampleWidthBits,
		ByteOrder:       roomEvidenceAudioByteOrder,
	}
	fragment, err := json.Marshal(map[string]any{"audio_format": written})
	if err != nil {
		t.Fatalf("marshal manifest fragment: %v", err)
	}
	var object roomReplayJSONObject
	if err := json.Unmarshal(fragment, &object); err != nil {
		t.Fatalf("decode manifest fragment: %v", err)
	}

	parsed, err := parseRoomReplayPCMFormat(object)
	if err != nil {
		t.Fatalf("replay reader rejected the recorder's own audio_format shape: %v", err)
	}
	if err := validateRoomReplayPCMFormat(parsed); err != nil {
		t.Fatalf("replay reader's own validation rejected the recorder's audio_format: %v", err)
	}
	if parsed.SampleRate != written.SampleRate || parsed.Channels != written.Channels {
		t.Fatalf("parsed pcm format = %+v, want rate/channels to match written %+v", parsed, written)
	}
	if parsed.SampleWidthBits != roomEvidenceAudioSampleWidthBits {
		t.Fatalf("parsed sample_width_bits = %d, want %d", parsed.SampleWidthBits, roomEvidenceAudioSampleWidthBits)
	}
	if !strings.EqualFold(parsed.ByteOrder, roomEvidenceAudioByteOrder) {
		t.Fatalf("parsed byte_order = %q, want %q", parsed.ByteOrder, roomEvidenceAudioByteOrder)
	}
	if !strings.EqualFold(parsed.Encoding, roomEvidenceAudioEncoding) {
		t.Fatalf("parsed encoding = %q, want %q", parsed.Encoding, roomEvidenceAudioEncoding)
	}
}

// TestRoomEvidenceArtifactPaths_DeclaresEveryReplayRequiredParticipantRole is
// the field-coverage regression guard for the writer/reader artifact-role
// contract this PR closes: every participant artifact role LoadRoomReplayPlan
// requires (roomReplayRequiredParticipantArtifactRoles, plus the
// non-human-only "capture" role) must have a corresponding non-empty,
// correctly-keyed field on roomEvidenceArtifactPaths -- the struct
// session_room_evidence.go's writeManifest serializes as each participant's
// nested "artifacts" object. This mirrors
// TestRoomEvidenceAudioFormat_SatisfiesReplayPCMFormatSchema's pattern for
// pcm_format: it marshals the writer's own struct and feeds the encoded JSON
// through the reader's own role-normalization function
// (normalizeRoomReplayArtifactRole), so a future required role added to the
// reader without a matching field added to the writer (or a JSON tag typo'd
// away from what the reader's normalizer maps back to that role) fails this
// test immediately, instead of only surfacing the next time someone happens
// to run a real record-then-replay round trip.
func TestRoomEvidenceArtifactPaths_DeclaresEveryReplayRequiredParticipantRole(t *testing.T) {
	written := roomEvidenceArtifactPaths{
		WAV:         "agent-p.wav",
		Diagnostics: "agent-p.diagnostics.jsonl",
		Deltas:      "agent-p.deltas.jsonl",
		SentPCM:     "participants/p/sent.pcm",
		ReceivedPCM: "participants/p/received.pcm",
		Events:      "participants/p/events.jsonl",
		Capture:     "participants/p/capture.json",
	}
	data, err := json.Marshal(written)
	if err != nil {
		t.Fatalf("marshal roomEvidenceArtifactPaths: %v", err)
	}
	var byKey map[string]string
	if err := json.Unmarshal(data, &byKey); err != nil {
		t.Fatalf("decode roomEvidenceArtifactPaths JSON: %v", err)
	}

	requiredRoles := append(append([]string(nil), roomReplayRequiredParticipantArtifactRoles...), roomReplayArtifactRoleCapture)
	if len(requiredRoles) < 7 {
		t.Fatalf("required participant artifact roles = %v, want at least 7 (this test itself would silently stop covering a role the reader dropped)", requiredRoles)
	}
	seenRoles := make(map[string]struct{}, len(requiredRoles))
	for jsonKey, path := range byKey {
		role := normalizeRoomReplayArtifactRole(jsonKey)
		if path == "" {
			t.Fatalf("roomEvidenceArtifactPaths JSON key %q (role %q) is empty; every field this test sets must reach the encoded JSON as a non-empty path", jsonKey, role)
		}
		seenRoles[role] = struct{}{}
	}
	for _, role := range requiredRoles {
		if _, ok := seenRoles[role]; !ok {
			t.Fatalf("roomEvidenceArtifactPaths has no field whose JSON key normalizes to required replay role %q; the writer struct and the reader's roomReplayRequiredParticipantArtifactRoles/roomReplayArtifactRoleCapture have drifted apart", role)
		}
	}
}

// TestRoomRunRecordThenReplay_ManifestAudioFormatRoundTrips is the
// non-fixture regression guard the #315/#316 replay suite never had: it
// drives a real two-participant room through RunRoomWithResult -- the exact
// code path `agent room run --config` uses -- reads the run-manifest.json
// recording actually wrote to disk, and feeds those literal bytes into
// parseRoomReplayManifest -- the exact schema parser `agent room run
// --replay` uses to admit a bundle. No hand-authored manifest fixture is
// involved anywhere in this test.
//
// Before the writer/reader pcm_format fix, this failed on every single run,
// 100% reproducibly, with: `pcm_format.sample_width_bits ... invalid or
// missing`.
func TestRoomRunRecordThenReplay_ManifestAudioFormatRoundTrips(t *testing.T) {
	ids := []string{"alpha", "beta"}
	inferencers := make(map[string]*roomTestInferencer, len(ids))
	for _, id := range ids {
		inferencers[id] = &roomTestInferencer{events: []messages.StreamMessage{
			roomTestSessionOpen(id),
			roomTestMessageEnd(),
		}}
	}

	outputDir := filepath.Join(t.TempDir(), "room-run")
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.OutputDir = outputDir
	opts.Manifest.Room.MaxTurns = 1
	// room-mix.wav is decoded with wavio.Read elsewhere in this package,
	// which only accepts conventional production sample rates; use the
	// room's real default format rather than the test helper's tiny
	// synthetic one, so this exercises the same audio_format values a real
	// `room run --config` invocation would record.
	opts.MixerConfig = room.PCM16MixerConfig{}

	if _, err := RunRoomWithResult(context.Background(), io.Discard, opts); err != nil {
		t.Fatalf("RunRoomWithResult: %v", err)
	}

	manifestData := readRoomEvidenceFile(t, filepath.Join(outputDir, RoomEvidenceManifestPath))

	// This is the real reader's schema-level manifest parser: the same one
	// LoadRoomReplayPlan calls before it ever validates individual artifacts
	// against the filesystem.
	document, parseErr := parseRoomReplayManifest(manifestData)
	if parseErr != nil {
		t.Fatalf("replay reader rejected the recorder's own run-manifest.json: %v", parseErr)
	}
	if document.PCMFormat.SampleRate <= 0 || document.PCMFormat.Channels <= 0 {
		t.Fatalf("parsed replay pcm format = %+v, want positive rate/channels", document.PCMFormat)
	}
	if err := validateRoomReplayPCMFormat(document.PCMFormat); err != nil {
		t.Fatalf("recorder's audio_format failed replay validation: %v", err)
	}

	// The recorder now declares and writes every participant artifact role
	// LoadRoomReplayPlan requires, including "events" (see
	// roomParticipantEvidence.events in session_room_evidence.go) and
	// "capture" (see roomEvidenceArtifactPaths.Capture). This test's
	// participants are built with a custom SessionFactory returning a
	// scripted roomTestInferencer -- deliberately never touching a live or
	// hermetic websocket dialer -- so the room evidence writer still cannot
	// produce a genuine provider session capture here: recording only
	// happens on the real live-construction path (NewLiveSessionInferencer),
	// exactly like solo `agent session run --record` never captures traffic
	// for an injected SessionInferencer either. "capture" is still declared
	// (its path is in the manifest) but was never written, so admission
	// correctly still rejects this specific bundle as incomplete -- for a
	// different, precise reason than before: a declared artifact with no
	// size/sha256 on disk, not a missing role declaration. Pin that exact
	// shape so this test breaks loudly the moment either fact stops holding.
	//
	// TestRoomRunRecordThenReplay_FullEndToEndReplaySucceeds below drives
	// participants through the real live-construction path (a hermetic
	// websocket dialer, not an injected SessionInferencer) and asserts a
	// complete, successful record-then-replay round trip -- the "replace
	// this pinned-failure assertion with a real end-to-end success
	// assertion" this comment used to defer.
	_, loadErr := LoadRoomReplayPlan(outputDir)
	if loadErr == nil {
		t.Fatal("LoadRoomReplayPlan unexpectedly succeeded for a bundle built with an injected SessionInferencer that never produced a real provider capture")
	}
	var bundleErr *RoomReplayBundleError
	if !errors.As(loadErr, &bundleErr) {
		t.Fatalf("LoadRoomReplayPlan error = %v, want a *RoomReplayBundleError", loadErr)
	}
	if strings.Contains(bundleErr.Field, "pcm_format") || strings.Contains(bundleErr.Field, "audio_format") {
		t.Fatalf("LoadRoomReplayPlan is still failing on the pcm/audio format field this PR fixed: %+v", bundleErr)
	}
	if strings.Contains(bundleErr.Field, ".artifacts.events") {
		t.Fatalf("LoadRoomReplayPlan error = %+v, still failing on the \"events\" artifact role this PR's recorder now writes", bundleErr)
	}
	if bundleErr.Kind != RoomReplayBundleIncomplete || !strings.Contains(bundleErr.Field, ".artifacts.capture") {
		t.Fatalf("LoadRoomReplayPlan error = %+v, want the known missing \"capture\" content (this test's SessionFactory never produces one), not a new or different failure", bundleErr)
	}
}

// TestRoomRunRecordThenReplay_FullEndToEndReplaySucceeds is the real
// end-to-end proof #316's deterministic-replay regression guard depends on:
// it drives two participants through RunRoomWithResult on the real
// live-construction path (defaultRoomSessionFactory ->
// NewLiveSessionInferencer -> the real OpenAI realtime provider gateway),
// with a hermetic websocket transport standing in for the network so the
// exchange is scripted and reproducible, exactly the seam
// TestRunRoomWithResult_UsesRealRealtimeStackAndStrictParticipantWires (in
// s2s_room_realtime_replay_test.go) already proves is a faithful stand-in for
// a real provider connection. That is what makes this "record" pass produce
// a genuine, non-fixture run-manifest.json plus real per-participant
// artifacts, including a raw provider websocket capture recorded through
// NewRecordingWebSocketDialer -- not a hand-authored one.
//
// It then feeds that recorder-produced bundle through LoadRoomReplayPlan and
// a second RunRoomWithResult with ReplayPath set (and every live seam
// -- CredentialLookup, SessionFactory, WebSocketDialerFactory -- wired to
// fail the test if called), and asserts the room replays to completion. No
// hand-authored manifest or capture fixture is used anywhere in this test:
// every byte admitted and replayed came from a real recording pass.
func TestRoomRunRecordThenReplay_FullEndToEndReplaySucceeds(t *testing.T) {
	const model = openAIRealtimeDefaultModel
	ids := []string{"alpha", "beta"}
	captures := make(map[string]gwtesting.SessionCapture, len(ids))
	for _, id := range ids {
		captures[id] = roomRealtimeReplayCapture(model,
			roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, id+" system prompt")),
			roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
				"type": "session.created", "session": map[string]any{"id": "sess-" + id, "model": model},
			})),
			roomRealtimeReplayEvent(3, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.created", "response": map[string]any{"id": "resp-" + id},
			})),
			roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.output_text.delta", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.output_text.delta", "delta": id + " response",
			})),
			roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_text.done", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.output_text.done",
			})),
			roomRealtimeReplayEvent(6, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
				"type": "response.done",
			})),
		)
	}
	harness := newRoomRealtimeReplayHarness(t, captures)

	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
	credentials := map[string]string{
		"ROOM_ALPHA_KEY": "room-record-alpha-test-key",
		"ROOM_BETA_KEY":  "room-record-beta-test-key",
	}
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1, MaxDuration: roomRealtimeReplayTestTimeout},
		Participants: []room.Participant{
			{ID: "alpha", SystemPrompt: "alpha system prompt", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_ALPHA_KEY", Tools: []string{}},
			{ID: "beta", SystemPrompt: "beta system prompt", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_BETA_KEY", Tools: []string{}},
		},
	}
	outputDir := filepath.Join(t.TempDir(), "room-run")

	recordCtx, recordCancel := context.WithTimeout(context.Background(), roomRealtimeReplayTestTimeout)
	defer recordCancel()
	recordResult, err := RunRoomWithResult(recordCtx, io.Discard, RoomRunOptions{
		Manifest:  manifest,
		ConfigDir: configDir, ModelCatalog: testModelCatalog(),
		BaseURL:   "wss://room-record.invalid/v1/realtime",
		OutputDir: outputDir,
		CredentialLookup: func(name string) (string, bool) {
			value, ok := credentials[name]
			return value, ok
		},
		WebSocketDialerFactory: harness.DialerFactory,
		// This test's scripted captures are text-only: neither participant
		// script has an input_audio_buffer.append record. The production
		// mixer otherwise streams real-time-paced audio (including silence)
		// to every participant on its own wall-clock cadence, which would
		// send unscripted audio the strict harness dialer has no matching
		// script event for -- a real defect this test isn't exercising, not
		// something to paper over. A cadence source that is never advanced
		// makes the mixer never emit a frame, keeping this record pass's
		// wire traffic exactly the scripted session.update/response
		// exchange, deterministically.
		MixerConfig: room.PCM16MixerConfig{CadenceFactory: func(time.Duration) room.PCM16Cadence {
			return newRoomRealtimeReplayCadence()
		}},
	})
	if err != nil {
		t.Fatalf("record pass RunRoomWithResult: %v", err)
	}
	if recordResult.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("record pass room reason = %q, want %q", recordResult.Reason, RoomTerminationMaxTurnsReached)
	}
	for _, id := range ids {
		participant, ok := recordResult.Participants[id]
		if !ok || !participant.Connected || participant.TurnsCompleted != 1 || participant.TerminationReason == ParticipantTerminationError {
			t.Fatalf("record pass participant %q = %+v, want connected with one completed turn", id, participant)
		}
	}

	// This is the exact reader LoadRoomReplayPlan uses; asserting through it
	// first pins that admission genuinely succeeds, before the second run
	// exercises the full replay execution path.
	manifestData := readRoomEvidenceFile(t, filepath.Join(outputDir, RoomEvidenceManifestPath))
	document, parseErr := parseRoomReplayManifest(manifestData)
	if parseErr != nil {
		t.Fatalf("replay reader rejected the recorder's own run-manifest.json: %v", parseErr)
	}
	if err := validateRoomReplayPCMFormat(document.PCMFormat); err != nil {
		t.Fatalf("recorder's audio_format failed replay validation: %v", err)
	}

	plan, err := LoadRoomReplayPlan(outputDir)
	if err != nil {
		t.Fatalf("LoadRoomReplayPlan on a recorder-produced bundle: %v -- the recorder must emit every artifact role the loader requires", err)
	}
	if len(plan.Participants) != len(ids) {
		t.Fatalf("replay plan participants = %d, want %d", len(plan.Participants), len(ids))
	}
	for _, id := range ids {
		participant, ok := plan.Participant(id)
		if !ok {
			t.Fatalf("replay plan is missing participant %q", id)
		}
		if participant.CapturePath == "" || !filepath.IsAbs(participant.CapturePath) {
			t.Fatalf("participant %q capture path = %q, want a resolved absolute path", id, participant.CapturePath)
		}
		capture, captureErr := gwtesting.LoadSessionCapture(participant.CapturePath)
		if captureErr != nil {
			t.Fatalf("load participant %q recorded capture: %v", id, captureErr)
		}
		if len(capture.Records) == 0 {
			t.Fatalf("participant %q recorded capture has no records", id)
		}
		for _, record := range capture.Records {
			if record.PayloadType != gwtesting.SessionPayloadTypeWebSocketMessage {
				t.Fatalf("participant %q recorded capture record has payload_type %q, want %q (a raw provider websocket capture, not a message-level one)", id, record.PayloadType, gwtesting.SessionPayloadTypeWebSocketMessage)
			}
		}
	}

	// Second pass: replay the recorded bundle end to end through the exact
	// code path `agent room run --replay` uses, with every live seam wired
	// to fail the test if RunRoomWithResult ever touches it.
	replayCtx, replayCancel := context.WithTimeout(context.Background(), roomRealtimeReplayTestTimeout)
	defer replayCancel()
	replayResult, err := RunRoomWithResult(replayCtx, io.Discard, RoomRunOptions{
		Manifest:   room.Manifest{SchemaVersion: 999},
		ReplayPlan: &plan,
		ReplayPath: outputDir, ModelCatalog: testModelCatalog(),
		CredentialLookup: func(string) (string, bool) {
			t.Fatal("room replay looked up a live credential")
			return "", false
		},
		SessionFactory: func(room.Participant, SessionRunOptions) (messages.SessionInferencer, error) {
			t.Fatal("room replay called the live session factory")
			return nil, nil
		},
		WebSocketDialerFactory: func(room.Participant) transport.Dialer {
			t.Fatal("room replay called the live websocket dialer factory")
			return nil
		},
		// Same reasoning as the record pass above: this bundle has no
		// recorded inbound audio, so newRoomReplaySchedule leaves the mixer
		// undriven by the replay scheduler, but the mixer itself still runs
		// on a real-time cadence by default and can otherwise emit an
		// unscripted silence frame against the now-exhausted strict replay
		// dialer. A cadence that is never advanced keeps this pass exactly
		// the recorded session.update/response exchange too.
		MixerConfig: room.PCM16MixerConfig{CadenceFactory: func(time.Duration) room.PCM16Cadence {
			return newRoomRealtimeReplayCadence()
		}},
	})
	if err != nil {
		t.Fatalf("replay pass RunRoomWithResult: %v", err)
	}
	if len(replayResult.Participants) != len(ids) {
		t.Fatalf("replay pass participants = %+v, want %d results", replayResult.Participants, len(ids))
	}
	for _, id := range ids {
		participant, ok := replayResult.Participants[id]
		if !ok || !participant.Connected || participant.TerminationReason == ParticipantTerminationError {
			t.Fatalf("replay pass participant %q = %+v, want connected non-error result", id, participant)
		}
	}
}
