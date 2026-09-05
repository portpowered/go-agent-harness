package agentruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestLoadRoomReplayPlanValidatesCompleteBundleBeforeRuntime(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)

	plan, err := LoadRoomReplayPlan(bundle)
	if err != nil {
		t.Fatalf("LoadRoomReplayPlan: %v", err)
	}
	if !plan.Finalized || plan.SchemaVersion != RoomReplayBundleSchemaVersion {
		t.Fatalf("plan metadata = finalized:%t schema:%d, want finalized schema %d", plan.Finalized, plan.SchemaVersion, RoomReplayBundleSchemaVersion)
	}
	if len(plan.Participants) != 2 || plan.Participants[0].ID != "alpha" || plan.Participants[1].ID != "beta" {
		t.Fatalf("plan participants = %+v, want manifest-order-independent alpha/beta projections", plan.Participants)
	}
	if plan.Participants[0].CapturePath == "" || !filepath.IsAbs(plan.Participants[0].CapturePath) {
		t.Fatalf("capture path = %q, want resolved absolute path", plan.Participants[0].CapturePath)
	}
	if len(plan.Timeline) != 2 || plan.Timeline[0].Type != "speech_start" || plan.Timeline[1].ParticipantID != "beta" {
		t.Fatalf("timeline = %+v, want validated ordered events", plan.Timeline)
	}
	if plan.TimelinePath != filepath.Join(plan.BundlePath, "room-timeline.jsonl") {
		t.Fatalf("timeline path = %q, want bundle-relative resolution", plan.TimelinePath)
	}
	if plan.RoomMixPath != filepath.Join(plan.BundlePath, "room-mix.wav") {
		t.Fatalf("mix path = %q, want bundle-relative resolution", plan.RoomMixPath)
	}
	if manifest == nil {
		t.Fatal("test fixture did not return manifest")
	}

	// Admission reads and hashes the source only. It must not create a replay
	// destination or invoke a provider/device constructor as a side effect.
	if entries, err := os.ReadDir(filepath.Join(bundle, "replay-output")); !errors.Is(err, os.ErrNotExist) || len(entries) != 0 {
		t.Fatalf("admission created output side effect: entries=%v err=%v", entries, err)
	}
}

func TestLoadRoomReplayPlanAcceptsInventoryBackedParticipantArtifacts(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)
	participants := manifest["participants"].(map[string]any)
	legacyArtifacts := manifest["artifacts"].(map[string]any)
	inventory := make([]any, 0, len(participants)*len(roomReplayRequiredParticipantArtifactRoles)+len(participants)+2)
	for _, participantID := range []string{"alpha", "beta"} {
		participant := participants[participantID].(map[string]any)
		participantArtifacts := participant["artifacts"].(map[string]any)
		for _, role := range append(append([]string(nil), roomReplayRequiredParticipantArtifactRoles...), roomReplayArtifactRoleCapture) {
			original := participantArtifacts[role].(map[string]any)
			copy := make(map[string]any, len(original)+1)
			for key, value := range original {
				copy[key] = value
			}
			copy["name"] = copy["path"]
			inventory = append(inventory, copy)
		}
		delete(participant, "artifacts")
	}
	for _, role := range []string{"room_timeline", "room_mix"} {
		original := legacyArtifacts[role].(map[string]any)
		copy := make(map[string]any, len(original)+1)
		for key, value := range original {
			copy[key] = value
		}
		copy["name"] = copy["path"]
		inventory = append(inventory, copy)
	}
	manifest["artifacts"] = inventory
	writeManifestValue(t, bundle, manifest)

	plan, err := LoadRoomReplayPlan(bundle)
	if err != nil {
		t.Fatalf("LoadRoomReplayPlan with inventory-backed artifacts: %v", err)
	}
	for _, participant := range plan.Participants {
		if len(participant.Artifacts) != len(roomReplayRequiredParticipantArtifactRoles)+1 {
			t.Fatalf("participant %q has %d artifacts, want %d", participant.ID, len(participant.Artifacts), len(roomReplayRequiredParticipantArtifactRoles)+1)
		}
	}
}

func TestLoadRoomReplayPlanRejectsTruncatedArtifactAsIncomplete(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)
	artifactPath := filepath.Join(bundle, "participants", "alpha", "sent.pcm")
	if err := os.Truncate(artifactPath, 1); err != nil {
		t.Fatalf("truncate artifact: %v", err)
	}

	_, err := LoadRoomReplayPlan(bundle)
	if err == nil || !errors.Is(err, gateway.ErrReplayIncomplete) || !errors.Is(err, ErrRoomReplayBundleIncomplete) {
		t.Fatalf("truncated artifact error = %v, want replay-incomplete classification", err)
	}
	if errors.Is(err, ErrInvalidRoomReplayBundle) {
		t.Fatalf("truncated artifact error = %v, want incomplete and not mismatch classification", err)
	}
	if !strings.Contains(err.Error(), "participants/alpha/sent.pcm") || !strings.Contains(err.Error(), "expected size") {
		t.Fatalf("truncated artifact error = %v, want artifact and expected/actual size context", err)
	}
	if manifest == nil {
		t.Fatal("test fixture did not return manifest")
	}
}

func TestLoadRoomReplayPlanRejectsSameLengthMutationAsMismatch(t *testing.T) {
	bundle, _ := writeRoomReplayBundle(t)
	artifactPath := filepath.Join(bundle, "participants", "beta", "received.pcm")
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	data[0] ^= 0xff
	if err := os.WriteFile(artifactPath, data, 0o600); err != nil {
		t.Fatalf("mutate artifact: %v", err)
	}

	_, err = LoadRoomReplayPlan(bundle)
	if err == nil || !errors.Is(err, gateway.ErrReplayMismatch) || errors.Is(err, gateway.ErrReplayIncomplete) {
		t.Fatalf("same-length mutation error = %v, want replay-mismatch only", err)
	}
	if !errors.Is(err, ErrInvalidRoomReplayBundle) {
		t.Fatalf("same-length mutation error = %v, want invalid-bundle classification", err)
	}
	if !strings.Contains(err.Error(), "participants/beta/received.pcm") || !strings.Contains(err.Error(), "expected") || !strings.Contains(err.Error(), "actual") {
		t.Fatalf("same-length mutation error = %v, want digest diff context", err)
	}
}

func TestParseRoomReplayArtifactInventoryAcceptsPathKeyedMetadata(t *testing.T) {
	entries, err := parseRoomReplayArtifactInventory(json.RawMessage(`{"participants/alpha/sent.pcm":{"size":4,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), "integrity")
	if err != nil {
		t.Fatalf("parseRoomReplayArtifactInventory: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "participants/alpha/sent.pcm" || entries[0].Size == nil || *entries[0].Size != 4 {
		t.Fatalf("inventory entries = %+v, want path-keyed size metadata", entries)
	}
}

func TestLoadRoomReplayPlanRejectsUnsafeAndAliasedArtifacts(t *testing.T) {
	t.Run("traversal", func(t *testing.T) {
		bundle, manifest := writeRoomReplayBundle(t)
		participant := manifest["participants"].(map[string]any)["alpha"].(map[string]any)
		artifacts := participant["artifacts"].(map[string]any)
		artifacts[roomReplayArtifactRoleSentPCM].(map[string]any)["path"] = "../outside.pcm"
		writeManifestValue(t, bundle, manifest)

		_, err := LoadRoomReplayPlan(bundle)
		if err == nil || !errors.Is(err, gateway.ErrReplayMismatch) || !strings.Contains(err.Error(), "traversal") {
			t.Fatalf("traversal error = %v, want path mismatch", err)
		}
	})

	t.Run("symlink escape", func(t *testing.T) {
		bundle, _ := writeRoomReplayBundle(t)
		external := filepath.Join(t.TempDir(), "outside.pcm")
		if err := os.WriteFile(external, []byte{1, 2, 3, 4}, 0o600); err != nil {
			t.Fatalf("write external artifact: %v", err)
		}
		link := filepath.Join(bundle, "participants", "alpha", "sent.pcm")
		if err := os.Remove(link); err != nil {
			t.Fatalf("remove artifact: %v", err)
		}
		if err := os.Symlink(external, link); err != nil {
			t.Fatalf("symlink artifact: %v", err)
		}

		_, err := LoadRoomReplayPlan(bundle)
		if err == nil || !errors.Is(err, gateway.ErrReplayMismatch) || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink error = %v, want path mismatch", err)
		}
	})

	t.Run("duplicate ownership", func(t *testing.T) {
		bundle, manifest := writeRoomReplayBundle(t)
		alpha := manifest["participants"].(map[string]any)["alpha"].(map[string]any)
		beta := manifest["participants"].(map[string]any)["beta"].(map[string]any)
		alphaArtifacts := alpha["artifacts"].(map[string]any)
		betaArtifacts := beta["artifacts"].(map[string]any)
		betaArtifacts[roomReplayArtifactRoleSentPCM] = alphaArtifacts[roomReplayArtifactRoleSentPCM]
		writeManifestValue(t, bundle, manifest)

		_, err := LoadRoomReplayPlan(bundle)
		if err == nil || !errors.Is(err, gateway.ErrReplayMismatch) || !strings.Contains(err.Error(), "artifact ownership") {
			t.Fatalf("duplicate ownership error = %v, want ownership mismatch", err)
		}
	})
}

func TestLoadRoomReplayPlanRejectsUndeclaredTimelineArtifact(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)
	timelinePath := filepath.Join(bundle, "room-timeline.jsonl")
	clockBase := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	line := fmt.Sprintf(`{"sequence":0,"monotonic_offset_ms":0,"unix_ms":%d,"type":"speech_start","participant_id":"alpha","artifact":"not-declared.pcm"}`+"\n", clockBase.UnixMilli())
	if err := os.WriteFile(timelinePath, []byte(line), 0o600); err != nil {
		t.Fatalf("write timeline: %v", err)
	}
	updateArtifactDigest(t, manifest, "room_timeline", []byte(line))
	writeManifestValue(t, bundle, manifest)

	_, err := LoadRoomReplayPlan(bundle)
	if err == nil || !errors.Is(err, gateway.ErrReplayMismatch) || !strings.Contains(err.Error(), "undeclared") {
		t.Fatalf("undeclared timeline reference error = %v, want diff-bearing mismatch", err)
	}
}

func writeRoomReplayBundle(t *testing.T) (string, map[string]any) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "bundle")
	for _, participantID := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(bundle, "participants", participantID), 0o700); err != nil {
			t.Fatalf("create participant directory: %v", err)
		}
	}
	files := map[string][]byte{
		"participants/alpha/agent.wav":         {1, 2, 3},
		"participants/alpha/diagnostics.jsonl": {[]byte(`{"event":"turn"}`)[0]},
		"participants/alpha/deltas.jsonl":      []byte("delta\n"),
		"participants/alpha/sent.pcm":          {10, 11, 12, 13},
		"participants/alpha/received.pcm":      {0, 0, 2, 0},
		"participants/alpha/events.jsonl":      []byte("event\n"),
		"participants/beta/agent.wav":          {4, 5, 6},
		"participants/beta/diagnostics.jsonl":  []byte("diag\n"),
		"participants/beta/deltas.jsonl":       []byte("delta\n"),
		"participants/beta/sent.pcm":           {20, 21, 22, 23},
		"participants/beta/received.pcm":       {0, 0, 3, 0},
		"participants/beta/events.jsonl":       []byte("event\n"),
		"room-mix.wav":                         {7, 8, 9, 10},
	}
	for name, data := range files {
		filename := filepath.Join(bundle, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatalf("create artifact directory: %v", err)
		}
		if err := os.WriteFile(filename, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	clockBase := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	timeline := []byte(fmt.Sprintf(`{"sequence":0,"monotonic_offset_ms":0,"unix_ms":%d,"type":"speech_start","participant_id":"alpha"}`+"\n"+`{"sequence":1,"monotonic_offset_ms":10,"unix_ms":%d,"type":"speech_start","participant_id":"beta"}`+"\n", clockBase.UnixMilli(), clockBase.UnixMilli()+10))
	if err := os.WriteFile(filepath.Join(bundle, "room-timeline.jsonl"), timeline, 0o600); err != nil {
		t.Fatalf("write timeline: %v", err)
	}
	for _, participantID := range []string{"alpha", "beta"} {
		capture := gwtesting.SessionCapture{
			Version:  gwtesting.SessionCaptureVersion,
			Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime"},
			Records: []gwtesting.CapturedSessionEvent{{
				Sequence:    1,
				Direction:   gwtesting.DirectionClientToServer,
				Type:        "session.update",
				PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
				Payload:     json.RawMessage(`{"type":"session.update","session":{"model":"gpt-realtime"}}`),
			}},
		}
		data, err := json.Marshal(capture)
		if err != nil {
			t.Fatalf("marshal capture: %v", err)
		}
		name := "participants/" + participantID + "/session.session.json"
		if err := os.WriteFile(filepath.Join(bundle, filepath.FromSlash(name)), data, 0o600); err != nil {
			t.Fatalf("write capture: %v", err)
		}
		files[name] = data
	}

	manifest := map[string]any{
		"schema_version": RoomReplayBundleSchemaVersion,
		"finalized":      true,
		"clock_base":     clockBase.Format(time.RFC3339Nano),
		"timing":         map[string]any{"started_at": clockBase.Format(time.RFC3339Nano), "ended_at": clockBase.Add(100 * time.Millisecond).Format(time.RFC3339Nano), "elapsed": "100ms"},
		"pcm_format":     map[string]any{"sample_rate_hz": 24000, "channels": 1, "sample_width_bits": 16, "byte_order": "little", "encoding": "signed_pcm16"},
		"participants":   map[string]any{},
		"artifacts":      map[string]any{},
	}
	participants := manifest["participants"].(map[string]any)
	for _, participantID := range []string{"alpha", "beta"} {
		artifactValues := map[string]any{}
		for role, filename := range map[string]string{
			roomReplayArtifactRoleWAV:         "participants/" + participantID + "/agent.wav",
			roomReplayArtifactRoleDiagnostics: "participants/" + participantID + "/diagnostics.jsonl",
			roomReplayArtifactRoleDeltas:      "participants/" + participantID + "/deltas.jsonl",
			roomReplayArtifactRoleSentPCM:     "participants/" + participantID + "/sent.pcm",
			roomReplayArtifactRoleReceivedPCM: "participants/" + participantID + "/received.pcm",
			roomReplayArtifactRoleEvents:      "participants/" + participantID + "/events.jsonl",
			roomReplayArtifactRoleCapture:     "participants/" + participantID + "/session.session.json",
		} {
			data := files[filename]
			artifactValues[role] = artifactObject(filename, data)
		}
		participants[participantID] = map[string]any{
			"id": participantID, "kind": "agent", "provider": "openai", "model": "gpt-realtime", "artifacts": artifactValues,
		}
	}
	artifacts := manifest["artifacts"].(map[string]any)
	artifacts["room_timeline"] = artifactObject("room-timeline.jsonl", timeline)
	artifacts["room_mix"] = artifactObject("room-mix.wav", files["room-mix.wav"])
	writeManifestValue(t, bundle, manifest)
	return bundle, manifest
}

func artifactObject(path string, data []byte) map[string]any {
	digest := sha256.Sum256(data)
	return map[string]any{"path": path, "size": len(data), "sha256": hex.EncodeToString(digest[:])}
}

func writeManifestValue(t *testing.T, bundle string, value map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal test manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundle, RoomReplayBundleManifestPath), append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write test manifest: %v", err)
	}
}

func updateArtifactDigest(t *testing.T, manifest map[string]any, role string, data []byte) {
	t.Helper()
	artifact := manifest["artifacts"].(map[string]any)[role].(map[string]any)
	artifact["size"] = len(data)
	digest := sha256.Sum256(data)
	artifact["sha256"] = hex.EncodeToString(digest[:])
}
