package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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

	// Full bundle admission (LoadRoomReplayPlan) additionally requires two
	// per-participant artifact roles, "events" and "capture", that
	// session_room_evidence.go has never written and that
	// docs/room-recording-bundle.md does not document as part of the
	// recording bundle. That is a second, separate defect from the
	// pcm_format schema mismatch this PR fixes -- see the PR description --
	// and is out of scope here. Pin its exact current shape so this test
	// breaks loudly, instead of silently no-op'ing, the moment that gap
	// closes or its failure mode changes, rather than letting a real
	// end-to-end LoadRoomReplayPlan success go unnoticed and untested.
	_, loadErr := LoadRoomReplayPlan(outputDir)
	if loadErr == nil {
		t.Fatal("LoadRoomReplayPlan unexpectedly succeeded; the missing events/capture artifact gap appears to be fixed -- replace this pinned-failure assertion with a real end-to-end success assertion")
	}
	var bundleErr *RoomReplayBundleError
	if !errors.As(loadErr, &bundleErr) {
		t.Fatalf("LoadRoomReplayPlan error = %v, want a *RoomReplayBundleError", loadErr)
	}
	if strings.Contains(bundleErr.Field, "pcm_format") || strings.Contains(bundleErr.Field, "audio_format") {
		t.Fatalf("LoadRoomReplayPlan is still failing on the pcm/audio format field this PR fixed: %+v", bundleErr)
	}
	if bundleErr.Kind != RoomReplayBundleIncomplete || !strings.Contains(bundleErr.Field, ".artifacts.events") {
		t.Fatalf("LoadRoomReplayPlan error = %+v, want the known missing participant \"events\" artifact gap (a separate, out-of-scope defect), not a new or different failure", bundleErr)
	}
}
