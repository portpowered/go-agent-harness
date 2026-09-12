package service

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
	participants := roomReplayTestMap(t, manifest["participants"], "participants")
	legacyArtifacts := roomReplayTestMap(t, manifest["artifacts"], "artifacts")
	requiredRoles := roomReplayRequiredParticipantArtifactRoles()
	inventory := make([]any, 0, len(participants)*len(requiredRoles)+len(participants)+2)
	for _, participantID := range []string{"alpha", "beta"} {
		participant := roomReplayTestMap(t, participants[participantID], "participant "+participantID)
		participantArtifacts := roomReplayTestMap(t, participant["artifacts"], "participant artifacts "+participantID)
		for _, role := range append(append([]string(nil), requiredRoles...), roomReplayArtifactRoleCapture) {
			original := roomReplayTestMap(t, participantArtifacts[role], "participant artifact "+participantID+"/"+role)
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
		original := roomReplayTestMap(t, legacyArtifacts[role], "room artifact "+role)
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
		if len(participant.Artifacts) != len(requiredRoles)+1 {
			t.Fatalf("participant %q has %d artifacts, want %d", participant.ID, len(participant.Artifacts), len(requiredRoles)+1)
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
		participants := roomReplayTestMap(t, manifest["participants"], "participants")
		participant := roomReplayTestMap(t, participants["alpha"], "participant alpha")
		artifacts := roomReplayTestMap(t, participant["artifacts"], "participant alpha artifacts")
		roomReplayTestMap(t, artifacts[roomReplayArtifactRoleSentPCM], "participant alpha sent_pcm")["path"] = "../outside.pcm"
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
		participants := roomReplayTestMap(t, manifest["participants"], "participants")
		alpha := roomReplayTestMap(t, participants["alpha"], "participant alpha")
		beta := roomReplayTestMap(t, participants["beta"], "participant beta")
		alphaArtifacts := roomReplayTestMap(t, alpha["artifacts"], "participant alpha artifacts")
		betaArtifacts := roomReplayTestMap(t, beta["artifacts"], "participant beta artifacts")
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

func TestLoadRoomReplayPlanRejectsOversizedManifestWithTypedMismatch(t *testing.T) {
	bundle, _ := writeRoomReplayBundle(t)
	data := make([]byte, roomReplayMaxManifestBytes+1)
	for index := range data {
		data[index] = 'x'
	}
	if err := os.WriteFile(filepath.Join(bundle, RoomReplayBundleManifestPath), data, 0o600); err != nil {
		t.Fatalf("write oversized manifest: %v", err)
	}

	_, err := LoadRoomReplayPlan(bundle)
	if err == nil || !errors.Is(err, ErrInvalidRoomReplayBundle) || !errors.Is(err, gateway.ErrReplayMismatch) {
		t.Fatalf("oversized manifest error = %v, want typed mismatch", err)
	}
	var bundleErr *RoomReplayBundleError
	if !errors.As(err, &bundleErr) || bundleErr.Field != "run-manifest.json" || !strings.Contains(bundleErr.Actual, "maximum") {
		t.Fatalf("oversized manifest error = %+v, want bounded manifest context", bundleErr)
	}
}

func TestLoadRoomReplayPlanRejectsOversizedTimelineLineWithTypedMismatch(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)
	line := strings.Repeat("x", int(roomReplayMaxTimelineLineBytes)) + "\n"
	if err := os.WriteFile(filepath.Join(bundle, "room-timeline.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatalf("write oversized timeline: %v", err)
	}
	updateArtifactDigest(t, manifest, "room_timeline", []byte(line))
	writeManifestValue(t, bundle, manifest)

	_, err := LoadRoomReplayPlan(bundle)
	if err == nil || !errors.Is(err, ErrInvalidRoomReplayBundle) || !errors.Is(err, gateway.ErrReplayMismatch) {
		t.Fatalf("oversized timeline error = %v, want typed mismatch", err)
	}
	var bundleErr *RoomReplayBundleError
	if !errors.As(err, &bundleErr) || bundleErr.Field != "room_timeline" || !strings.Contains(bundleErr.Expected, "JSONL lines") {
		t.Fatalf("oversized timeline error = %+v, want bounded line context", bundleErr)
	}
}

func TestLoadRoomReplayPlanReturnsDeterministicProjectionCopies(t *testing.T) {
	bundle, _ := writeRoomReplayBundle(t)
	first, err := LoadRoomReplayPlan(bundle)
	if err != nil {
		t.Fatalf("first LoadRoomReplayPlan: %v", err)
	}
	second, err := LoadRoomReplayPlan(bundle)
	if err != nil {
		t.Fatalf("second LoadRoomReplayPlan: %v", err)
	}
	if got, want := fmt.Sprintf("%v", first.Participants), fmt.Sprintf("%v", second.Participants); got != want {
		t.Fatalf("participant projection changed between loads: first=%s second=%s", got, want)
	}
	if len(first.Artifacts) != len(second.Artifacts) {
		t.Fatalf("artifact projection lengths = %d and %d", len(first.Artifacts), len(second.Artifacts))
	}
	for index := range first.Artifacts {
		if first.Artifacts[index].Path != second.Artifacts[index].Path || first.Artifacts[index].Owner != second.Artifacts[index].Owner {
			t.Fatalf("artifact %d changed between loads: first=%+v second=%+v", index, first.Artifacts[index], second.Artifacts[index])
		}
	}
}

func TestLoadRoomReplayPlanAcceptsSchemaV1AndRejectsMalformedManifest(t *testing.T) {
	t.Run("schema v1", func(t *testing.T) {
		bundle, manifest := writeRoomReplayBundle(t)
		manifest["schema_version"] = 1
		writeManifestValue(t, bundle, manifest)
		plan, err := LoadRoomReplayPlan(bundle)
		if err != nil || plan.SchemaVersion != 1 {
			t.Fatalf("schema v1 LoadRoomReplayPlan = plan:%+v err:%v", plan, err)
		}
	})
	t.Run("malformed json", func(t *testing.T) {
		bundle, _ := writeRoomReplayBundle(t)
		if err := os.WriteFile(filepath.Join(bundle, RoomReplayBundleManifestPath), []byte("{"), 0o600); err != nil {
			t.Fatalf("write malformed manifest: %v", err)
		}
		_, err := LoadRoomReplayPlan(bundle)
		if err == nil || !errors.Is(err, ErrInvalidRoomReplayBundle) || !errors.Is(err, gateway.ErrReplayMismatch) {
			t.Fatalf("malformed manifest error = %v, want typed mismatch", err)
		}
	})
	t.Run("malformed timestamp", func(t *testing.T) {
		bundle, manifest := writeRoomReplayBundle(t)
		manifest["clock_base"] = "not-a-timestamp"
		writeManifestValue(t, bundle, manifest)
		_, err := LoadRoomReplayPlan(bundle)
		if err == nil || !errors.Is(err, ErrRoomReplayBundleIncomplete) || !strings.Contains(err.Error(), "clock_base") {
			t.Fatalf("malformed timestamp error = %v, want incomplete clock_base", err)
		}
	})
}

func TestLoadRoomReplayPlanRejectsCaptureProviderMismatch(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)
	participants := roomReplayTestMap(t, manifest["participants"], "participants")
	participant := roomReplayTestMap(t, participants["alpha"], "participant alpha")
	participantArtifacts := roomReplayTestMap(t, participant["artifacts"], "participant alpha artifacts")
	captureRef := roomReplayTestMap(t, participantArtifacts[roomReplayArtifactRoleCapture], "participant alpha capture")
	capturePath := filepath.Join(bundle, filepath.FromSlash(roomReplayTestString(t, captureRef["path"], "participant alpha capture path")))
	capture, err := gwtesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load fixture capture: %v", err)
	}
	capture.Provider.Model = "different-model"
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal mismatched capture: %v", err)
	}
	if err := os.WriteFile(capturePath, data, 0o600); err != nil {
		t.Fatalf("write mismatched capture: %v", err)
	}
	captureRef["size"] = len(data)
	digest := sha256.Sum256(data)
	captureRef["sha256"] = hex.EncodeToString(digest[:])
	writeManifestValue(t, bundle, manifest)

	_, err = LoadRoomReplayPlan(bundle)
	if err == nil || !errors.Is(err, ErrInvalidRoomReplayBundle) || !strings.Contains(err.Error(), "participants[alpha].model") {
		t.Fatalf("capture provider mismatch error = %v, want typed participant model mismatch", err)
	}
}

func TestLoadRoomReplayPlanAcceptsArrayAndAliasSchemas(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)
	participants := roomReplayTestMap(t, manifest["participants"], "participants")
	participantValues := make([]any, 0, len(participants))
	for _, participantID := range []string{"alpha", "beta"} {
		original := roomReplayTestMap(t, participants[participantID], "participant "+participantID)
		participant := make(map[string]any, len(original))
		for key, value := range original {
			participant[key] = value
		}
		artifactMap := roomReplayTestMap(t, participant["artifacts"], "participant artifacts "+participantID)
		artifactValues := make([]any, 0, len(artifactMap))
		for _, role := range []string{
			roomReplayArtifactRoleWAV,
			roomReplayArtifactRoleDiagnostics,
			roomReplayArtifactRoleDeltas,
			roomReplayArtifactRoleSentPCM,
			roomReplayArtifactRoleReceivedPCM,
			roomReplayArtifactRoleEvents,
			roomReplayArtifactRoleCapture,
		} {
			artifact := roomReplayTestMap(t, artifactMap[role], "artifact "+participantID+"/"+role)
			artifactValues = append(artifactValues, map[string]any{
				"role":    role,
				"path":    artifact["path"],
				"bytes":   artifact["size"],
				"sha_256": artifact["sha256"],
			})
		}
		participant["artifacts"] = artifactValues
		participantValues = append(participantValues, participant)
	}
	manifest["participants"] = participantValues

	artifacts := roomReplayTestMap(t, manifest["artifacts"], "artifacts")
	timeline := roomReplayTestMap(t, artifacts["room_timeline"], "room timeline")
	mix := roomReplayTestMap(t, artifacts["room_mix"], "room mix")
	manifest["artifacts"] = []any{
		map[string]any{
			"name":          "room_timeline",
			"path":          timeline["path"],
			"bytes":         timeline["size"],
			"sha256_digest": timeline["sha256"],
		},
		map[string]any{
			"name":          "room_mix",
			"path":          mix["path"],
			"bytes":         mix["size"],
			"sha256_digest": mix["sha256"],
		},
	}
	writeManifestValue(t, bundle, manifest)

	plan, err := LoadRoomReplayPlan(bundle)
	if err != nil {
		t.Fatalf("LoadRoomReplayPlan with array/alias schema: %v", err)
	}
	if len(plan.Participants) != 2 || plan.Participants[0].ID != "alpha" || plan.Participants[1].ID != "beta" {
		t.Fatalf("participants = %+v, want stable array order", plan.Participants)
	}
	if len(plan.Artifacts) != 16 || plan.TimelinePath == "" || plan.RoomMixPath == "" {
		t.Fatalf("plan artifacts = %d timeline=%q mix=%q, want participant and room projections", len(plan.Artifacts), plan.TimelinePath, plan.RoomMixPath)
	}
}

func TestLoadRoomReplayPlanAcceptsLegacyAliasesAndHumanParticipant(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)
	delete(manifest, "finalized")
	manifest["status"] = "finalized"
	delete(manifest, "clock_base")
	timing := roomReplayTestMap(t, manifest["timing"], "timing")
	timing["clock_base"] = "2026-08-30T12:00:00Z"
	delete(manifest, "pcm_format")
	manifest["pcm"] = map[string]any{
		"sample_rate":     24000,
		"channel_count":   1,
		"sample_width":    16,
		"endianness":      "little",
		"sample_encoding": "pcm_s16le",
	}
	participants := roomReplayTestMap(t, manifest["participants"], "participants")
	beta := roomReplayTestMap(t, participants["beta"], "participant beta")
	beta["kind"] = "human"
	delete(beta, "provider")
	delete(beta, "model")
	betaArtifacts := roomReplayTestMap(t, beta["artifacts"], "participant beta artifacts")
	delete(betaArtifacts, roomReplayArtifactRoleCapture)
	writeManifestValue(t, bundle, manifest)

	plan, err := LoadRoomReplayPlan(bundle)
	if err != nil {
		t.Fatalf("LoadRoomReplayPlan with legacy aliases: %v", err)
	}
	if plan.PCMFormat.SampleRate != 24000 || plan.PCMFormat.Encoding != "pcm_s16le" {
		t.Fatalf("PCM format = %+v, want nested legacy aliases", plan.PCMFormat)
	}
	if len(plan.Participants) != 2 || plan.Participants[1].Kind != "human" {
		t.Fatalf("participants = %+v, want human participant", plan.Participants)
	}
	if plan.Participants[1].CapturePath != "" {
		t.Fatalf("human capture path = %q, want no provider capture requirement", plan.Participants[1].CapturePath)
	}
}

func TestRoomReplayServiceValidatesBundleAndOutputBoundaries(t *testing.T) {
	bundle, _ := writeRoomReplayBundle(t)
	service := New()
	plan, err := service.Load(bundle)
	if err != nil {
		t.Fatalf("Service.Load: %v", err)
	}
	for _, test := range []struct {
		name        string
		destination string
		wantErr     bool
	}{
		{name: "missing", destination: "", wantErr: true},
		{name: "source", destination: plan.BundlePath, wantErr: true},
		{name: "source child", destination: filepath.Join(plan.BundlePath, "replay-output"), wantErr: true},
		{name: "outside", destination: filepath.Join(t.TempDir(), "replay-output"), wantErr: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := service.ValidateOutput(plan, test.destination)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateOutput(%q) = %v, want error: %t", test.destination, err, test.wantErr)
			}
		})
	}
	manifestPath := filepath.Join(bundle, RoomReplayBundleManifestPath)
	if _, err := service.Load(manifestPath); err != nil {
		t.Fatalf("Service.Load(manifest path): %v", err)
	}
	_, err = service.Load(filepath.Join(t.TempDir(), "missing-bundle"))
	if err == nil || !errors.Is(err, ErrRoomReplayBundleIncomplete) {
		t.Fatalf("missing bundle error = %v, want incomplete classification", err)
	}
}

func TestLoadRoomReplayPlanRejectsConflictingInventoryMetadata(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)
	manifest["integrity"] = map[string]any{
		"participants/alpha/sent.pcm": map[string]any{
			"bytes":  999,
			"sha256": "",
		},
	}
	writeManifestValue(t, bundle, manifest)

	_, err := LoadRoomReplayPlan(bundle)
	if err == nil || !errors.Is(err, ErrInvalidRoomReplayBundle) || !strings.Contains(err.Error(), "participants/alpha/sent.pcm") {
		t.Fatalf("conflicting inventory metadata error = %v, want typed path conflict", err)
	}
}

func TestLoadRoomReplayPlanPreservesFractionalTimelineAndRejectsUnsafeReference(t *testing.T) {
	t.Run("fractional offset", func(t *testing.T) {
		bundle, manifest := writeRoomReplayBundle(t)
		clockBase := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
		timeline := []byte(fmt.Sprintf(`{"sequence":0,"monotonic_offset_ms":0,"unix_ms":%d,"type":"speech_start","participant_id":"alpha"}`+"\n"+`{"sequence":1,"monotonic_offset_ms":10.5,"unix_ms":%d,"type":"speech_start","participant_id":"beta"}`+"\n", clockBase.UnixMilli(), clockBase.UnixMilli()+10))
		if err := os.WriteFile(filepath.Join(bundle, "room-timeline.jsonl"), timeline, 0o600); err != nil {
			t.Fatalf("write fractional timeline: %v", err)
		}
		updateArtifactDigest(t, manifest, "room_timeline", timeline)
		writeManifestValue(t, bundle, manifest)

		plan, err := LoadRoomReplayPlan(bundle)
		if err != nil {
			t.Fatalf("LoadRoomReplayPlan fractional timeline: %v", err)
		}
		if len(plan.Timeline) != 2 || plan.Timeline[1].OffsetMS != 10 || plan.Timeline[1].OffsetNanos != 10_500_000 {
			t.Fatalf("timeline = %+v, want fractional offset projection", plan.Timeline)
		}
	})

	t.Run("unsafe artifact reference", func(t *testing.T) {
		bundle, manifest := writeRoomReplayBundle(t)
		clockBase := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
		timeline := []byte(fmt.Sprintf(`{"sequence":0,"monotonic_offset_ms":0,"unix_ms":%d,"type":"speech_start","participant_id":"alpha","artifact":"../outside.pcm"}`+"\n", clockBase.UnixMilli()))
		if err := os.WriteFile(filepath.Join(bundle, "room-timeline.jsonl"), timeline, 0o600); err != nil {
			t.Fatalf("write unsafe timeline: %v", err)
		}
		updateArtifactDigest(t, manifest, "room_timeline", timeline)
		writeManifestValue(t, bundle, manifest)

		_, err := LoadRoomReplayPlan(bundle)
		if err == nil || !errors.Is(err, ErrInvalidRoomReplayBundle) || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("unsafe timeline reference error = %v, want typed path rejection", err)
		}
	})
}

func TestLoadRoomReplayPlanRejectsHeaderAndArtifactShapeFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   error
	}{
		{name: "unsupported schema", mutate: func(manifest map[string]any) { manifest["schema_version"] = 99 }, want: ErrInvalidRoomReplayBundle},
		{name: "invalid PCM byte order", mutate: func(manifest map[string]any) {
			roomReplayTestMap(t, manifest["pcm_format"], "pcm_format")["byte_order"] = "big"
		}, want: ErrInvalidRoomReplayBundle},
		{name: "negative turn count", mutate: func(manifest map[string]any) {
			roomReplayTestMap(t, roomReplayTestMap(t, manifest["participants"], "participants")["alpha"], "participant alpha")["completed_turns"] = -1
		}, want: ErrInvalidRoomReplayBundle},
		{name: "missing digest", mutate: func(manifest map[string]any) {
			participant := roomReplayTestMap(t, roomReplayTestMap(t, manifest["participants"], "participants")["alpha"], "participant alpha")
			artifact := roomReplayTestMap(t, roomReplayTestMap(t, participant["artifacts"], "participant artifacts")[roomReplayArtifactRoleSentPCM], "sent PCM")
			delete(artifact, "sha256")
		}, want: ErrRoomReplayBundleIncomplete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle, manifest := writeRoomReplayBundle(t)
			test.mutate(manifest)
			writeManifestValue(t, bundle, manifest)
			_, err := LoadRoomReplayPlan(bundle)
			if err == nil || !errors.Is(err, test.want) {
				t.Fatalf("LoadRoomReplayPlan error = %v, want errors.Is(..., %v)", err, test.want)
			}
		})
	}
}

func TestRoomReplayValidationHelpersRejectMalformedInputs(t *testing.T) {
	t.Run("path normalization", func(t *testing.T) {
		for _, value := range []string{"", "has\x00nul", `a\b`, "/absolute", "../outside", ".", "artifact:stream"} {
			if _, err := normalizeSafeRoomReplayPath(value); err == nil {
				t.Errorf("normalizeSafeRoomReplayPath(%q) = nil error, want rejection", value)
			}
		}
	})

	t.Run("inventory object and duplicate participant role", func(t *testing.T) {
		entries, err := parseRoomReplayArtifactInventory(json.RawMessage(`{"path":"room-mix.wav","size":4,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), "integrity")
		if err != nil || len(entries) != 1 || entries[0].Path != "room-mix.wav" {
			t.Fatalf("single inventory entry = %+v, err=%v", entries, err)
		}
		if _, err := parseRoomReplayArtifactInventory(json.RawMessage(`{"path":12}`), "integrity"); err == nil {
			t.Fatal("invalid inventory path type accepted")
		}

		duplicate := json.RawMessage(`[{"role":"sent_pcm","path":"a.pcm","size":1,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"role":"sent_pcm","path":"b.pcm","size":1,"sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]`)
		if _, err := parseRoomReplayParticipantArtifacts(duplicate, "artifacts"); err == nil {
			t.Fatal("duplicate participant artifact role accepted")
		}
	})

	t.Run("timeline budgets and offsets", func(t *testing.T) {
		if err := validateRoomReplayTimelineBudget(roomReplayMaxTimelineBytes+1, 0); err == nil {
			t.Fatal("oversized timeline accepted")
		}
		if err := validateRoomReplayTimelineBudget(0, roomReplayMaxTimelineRecords); err == nil {
			t.Fatal("overlong timeline record count accepted")
		}
		for _, raw := range []string{"not-a-number", "-1", "1e999"} {
			object := roomReplayJSONObject{"offset": json.RawMessage(raw)}
			if _, _, _, err := firstRoomReplayTimelineOffset(object, "offset"); err == nil {
				t.Errorf("firstRoomReplayTimelineOffset(%s) = nil error, want rejection", raw)
			}
		}
	})

	t.Run("empty manifest", func(t *testing.T) {
		bundle := filepath.Join(t.TempDir(), "bundle")
		if err := os.MkdirAll(bundle, 0o700); err != nil {
			t.Fatalf("create empty bundle: %v", err)
		}
		if err := os.WriteFile(filepath.Join(bundle, RoomReplayBundleManifestPath), []byte(" \n"), 0o600); err != nil {
			t.Fatalf("write empty manifest: %v", err)
		}
		_, err := LoadRoomReplayPlan(bundle)
		if err == nil || !errors.Is(err, ErrInvalidRoomReplayBundle) {
			t.Fatalf("empty manifest error = %v, want typed mismatch", err)
		}
	})
}

func TestRoomReplayTimelineParserRejectsMalformedEvents(t *testing.T) {
	for _, test := range []struct {
		name string
		line string
	}{
		{name: "not an object", line: `[]`},
		{name: "missing type", line: `{"offset":0,"unix_ms":0}`},
		{name: "invalid offset", line: `{"type":"speech_start","offset":"bad","unix_ms":0}`},
		{name: "missing unix timestamp", line: `{"type":"speech_start","offset":0}`},
		{name: "negative unix timestamp", line: `{"type":"speech_start","offset":0,"unix_ms":-1}`},
		{name: "negative sequence", line: `{"type":"speech_start","offset":0,"unix_ms":0,"sequence":-1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := parseRoomReplayTimelineEvent([]byte(test.line), 1, 0, "room-timeline.jsonl"); err == nil {
				t.Fatalf("parseRoomReplayTimelineEvent(%s) = nil error, want rejection", test.line)
			}
		})
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
	participants := roomReplayTestMap(t, manifest["participants"], "participants")
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
	artifacts := roomReplayTestMap(t, manifest["artifacts"], "artifacts")
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
	artifacts := roomReplayTestMap(t, manifest["artifacts"], "artifacts")
	artifact := roomReplayTestMap(t, artifacts[role], "artifact "+role)
	artifact["size"] = len(data)
	digest := sha256.Sum256(data)
	artifact["sha256"] = hex.EncodeToString(digest[:])
}

func roomReplayTestMap(t *testing.T, value any, field string) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("test manifest field %q has unexpected type %T", field, value)
	}
	return result
}

func roomReplayTestString(t *testing.T, value any, field string) string {
	t.Helper()
	result, ok := value.(string)
	if !ok {
		t.Fatalf("test manifest field %q has unexpected type %T", field, value)
	}
	return result
}
