package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	roomReplayAdditionalParticipantID = "beta"
	roomReplayHumanParticipantKind    = "human"
)

func TestLoadRoomReplayPlanAcceptsArrayAndAliasSchemas(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)
	participants := roomReplayTestMap(t, manifest["participants"], "participants")
	participantValues := make([]any, 0, len(participants))
	for _, participantID := range []string{"alpha", roomReplayAdditionalParticipantID} {
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

	plan, err := roomReplayServiceForTest().Load(bundle)
	if err != nil {
		t.Fatalf("LoadRoomReplayPlan with array/alias schema: %v", err)
	}
	if len(plan.Participants) != 2 || plan.Participants[0].ID != "alpha" || plan.Participants[1].ID != roomReplayAdditionalParticipantID {
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
	beta := roomReplayTestMap(t, participants[roomReplayAdditionalParticipantID], "participant beta")
	beta["kind"] = roomReplayHumanParticipantKind
	delete(beta, "provider")
	delete(beta, "model")
	betaArtifacts := roomReplayTestMap(t, beta["artifacts"], "participant beta artifacts")
	delete(betaArtifacts, roomReplayArtifactRoleCapture)
	writeManifestValue(t, bundle, manifest)

	plan, err := roomReplayServiceForTest().Load(bundle)
	if err != nil {
		t.Fatalf("LoadRoomReplayPlan with legacy aliases: %v", err)
	}
	if plan.PCMFormat.SampleRate != 24000 || plan.PCMFormat.Encoding != "pcm_s16le" {
		t.Fatalf("PCM format = %+v, want nested legacy aliases", plan.PCMFormat)
	}
	if len(plan.Participants) != 2 || plan.Participants[1].Kind != roomReplayHumanParticipantKind {
		t.Fatalf("participants = %+v, want human participant", plan.Participants)
	}
	if plan.Participants[1].CapturePath != "" {
		t.Fatalf("human capture path = %q, want no provider capture requirement", plan.Participants[1].CapturePath)
	}
}

func TestRoomReplayServiceValidatesBundleAndOutputBoundaries(t *testing.T) {
	bundle, _ := writeRoomReplayBundle(t)
	service := roomReplayServiceForTest()
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

func TestRoomReplayServiceValidatesOutputBoundaryThroughSymlinks(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "bundle")
	if err := os.Mkdir(bundle, 0o755); err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	plan := RoomReplayPlan{BundlePath: bundle}
	service := roomReplayServiceForTest()

	external := t.TempDir()
	linkToBundle := filepath.Join(external, "bundle-link")
	if err := os.Symlink(bundle, linkToBundle); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	if err := service.ValidateOutput(plan, filepath.Join(linkToBundle, "output")); err == nil {
		t.Fatal("output through an external symlink into the source bundle was accepted")
	}

	linkFromBundle := filepath.Join(bundle, "external-link")
	if err := os.Symlink(external, linkFromBundle); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	if err := service.ValidateOutput(plan, filepath.Join(linkFromBundle, "output")); err != nil {
		t.Fatalf("output through a source symlink to an external directory was rejected: %v", err)
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

	_, err := roomReplayServiceForTest().Load(bundle)
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

		plan, err := roomReplayServiceForTest().Load(bundle)
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

		_, err := roomReplayServiceForTest().Load(bundle)
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
			_, err := roomReplayServiceForTest().Load(bundle)
			if err == nil || !errors.Is(err, test.want) {
				t.Fatalf("LoadRoomReplayPlan error = %v, want errors.Is(..., %v)", err, test.want)
			}
		})
	}
}

func TestRoomReplayPathNormalizationRejectsUnsafeInputs(t *testing.T) {
	for _, value := range []string{"", "has\x00nul", `a\b`, "/absolute", "../outside", ".", "artifact:stream"} {
		t.Run(value, func(t *testing.T) {
			bundle, manifest := writeRoomReplayBundle(t)
			participants := roomReplayTestMap(t, manifest["participants"], "participants")
			participant := roomReplayTestMap(t, participants["alpha"], "participant alpha")
			artifacts := roomReplayTestMap(t, participant["artifacts"], "participant alpha artifacts")
			roomReplayTestMap(t, artifacts[roomReplayArtifactRoleSentPCM], "participant alpha sent PCM")["path"] = value
			writeManifestValue(t, bundle, manifest)
			_, err := roomReplayServiceForTest().Load(bundle)
			want := ErrInvalidRoomReplayBundle
			if value == "" {
				want = ErrRoomReplayBundleIncomplete
			}
			if !errors.Is(err, want) {
				t.Fatalf("Service.Load with unsafe artifact path %q = %v, want errors.Is(%v)", value, err, want)
			}
		})
	}
}

func TestRoomReplayInventoryObjectAcceptsSinglePathEntry(t *testing.T) {
	bundle, manifest := writeRoomReplayBundle(t)
	artifacts := roomReplayTestMap(t, manifest["artifacts"], "artifacts")
	roomMix := roomReplayTestMap(t, artifacts["room_mix"], "room mix")
	manifest["integrity"] = map[string]any{
		"path": roomMix["path"], "size": roomMix["size"], "sha256": roomMix["sha256"],
	}
	writeManifestValue(t, bundle, manifest)
	if _, err := roomReplayServiceForTest().Load(bundle); err != nil {
		t.Fatalf("Service.Load with single-entry integrity metadata: %v", err)
	}
}

func TestRoomReplayInventoryRejectsInvalidPathAndDuplicateRole(t *testing.T) {
	t.Run("invalid integrity path", func(t *testing.T) {
		bundle, manifest := writeRoomReplayBundle(t)
		manifest["integrity"] = map[string]any{"path": 12}
		writeManifestValue(t, bundle, manifest)
		if _, err := roomReplayServiceForTest().Load(bundle); !errors.Is(err, ErrInvalidRoomReplayBundle) {
			t.Fatalf("Service.Load with non-string integrity path = %v, want invalid-bundle error", err)
		}
	})
	t.Run("duplicate participant role", func(t *testing.T) {
		bundle, manifest := writeRoomReplayBundle(t)
		participants := roomReplayTestMap(t, manifest["participants"], "participants")
		participant := roomReplayTestMap(t, participants["alpha"], "participant alpha")
		artifacts := roomReplayTestMap(t, participant["artifacts"], "participant alpha artifacts")
		sent := roomReplayTestMap(t, artifacts[roomReplayArtifactRoleSentPCM], "participant alpha sent PCM")
		duplicate := make(map[string]any, len(sent))
		for key, value := range sent {
			duplicate[key] = value
		}
		sent["role"] = roomReplayArtifactRoleSentPCM
		duplicate["role"] = roomReplayArtifactRoleSentPCM
		duplicate["path"] = "participants/alpha/duplicate.pcm"
		participant["artifacts"] = []any{sent, duplicate}
		writeManifestValue(t, bundle, manifest)
		if _, err := roomReplayServiceForTest().Load(bundle); !errors.Is(err, ErrInvalidRoomReplayBundle) {
			t.Fatalf("Service.Load with duplicate participant role = %v, want invalid-bundle error", err)
		}
	})
}

func TestRoomReplayTimelineOffsetRejectsMalformedNumbers(t *testing.T) {
	for _, raw := range []string{"not-a-number", "-1", "1e999"} {
		t.Run(raw, func(t *testing.T) {
			clockBase := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
			line := fmt.Sprintf(`{"type":"speech_start","offset":%s,"unix_ms":%d,"participant_id":"alpha"}`, raw, clockBase.UnixMilli())
			if err := loadRoomReplayWithTimeline(t, line); err == nil {
				t.Fatalf("Service.Load with malformed timeline offset %s succeeded", raw)
			}
		})
	}
}

func TestRoomReplayRejectsEmptyManifest(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatalf("create empty bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundle, RoomReplayBundleManifestPath), []byte(" \n"), 0o600); err != nil {
		t.Fatalf("write empty manifest: %v", err)
	}
	_, err := roomReplayServiceForTest().Load(bundle)
	if err == nil || !errors.Is(err, ErrInvalidRoomReplayBundle) {
		t.Fatalf("empty manifest error = %v, want typed mismatch", err)
	}
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
			if err := loadRoomReplayWithTimeline(t, test.line); err == nil {
				t.Fatalf("Service.Load with malformed timeline event %s succeeded", test.line)
			}
		})
	}
}

func loadRoomReplayWithTimeline(t *testing.T, line string) error {
	t.Helper()
	bundle, manifest := writeRoomReplayBundle(t)
	timeline := []byte(line + "\n")
	if err := os.WriteFile(filepath.Join(bundle, "room-timeline.jsonl"), timeline, 0o600); err != nil {
		t.Fatalf("write malformed timeline: %v", err)
	}
	updateArtifactDigest(t, manifest, "room_timeline", timeline)
	writeManifestValue(t, bundle, manifest)
	_, err := roomReplayServiceForTest().Load(bundle)
	return err
}
