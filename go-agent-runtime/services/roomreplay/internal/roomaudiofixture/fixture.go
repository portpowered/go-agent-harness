// Package roomaudiofixture generates the deterministic, privacy-safe room
// replay fixtures committed under services/roomreplay/wire/testdata/room-audio.
// Every sample is synthesized from fixed seeds, so regenerating a shape
// reproduces the committed bytes exactly.
package roomaudiofixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Fixture shapes accepted by Generate.
const (
	ShapeCleanTurnTaking             = "clean-turn-taking"
	ShapeDeliberateOverlap           = "deliberate-overlap"
	ShapeLongConversationTermination = "long-conversation-termination"
)

const (
	sampleRate                          = 1000
	duration                            = 4 * sampleRate
	overlapDuration                     = 8 * sampleRate
	longConversationDuration            = 24 * sampleRate
	longConversationRouteDelay          = 20
	longConversationBoundOffset         = 23200
	longConversationTurnsPerParticipant = 4

	participantA = "agent-a"
	participantB = "agent-b"

	directoryMode = 0o755
	fileMode      = 0o644
)

type turn struct {
	ID    string
	Start int
	End   int
}

type artifactRef struct {
	Path   string `json:"path"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

// Generate writes the named fixture shape into output, replacing any existing
// fixture files with the same relative paths.
func Generate(shape, output string) error {
	switch shape {
	case ShapeCleanTurnTaking:
		return generateCleanTurnTaking(output)
	case ShapeDeliberateOverlap:
		return generateDeliberateOverlap(output)
	case ShapeLongConversationTermination:
		return generateLongConversationTermination(output)
	default:
		return fmt.Errorf("unknown room audio fixture shape %q", shape)
	}
}

func prepareOutput(output string) error {
	if output == "" {
		return errors.New("fixture output directory is empty")
	}
	if err := os.MkdirAll(output, directoryMode); err != nil {
		return fmt.Errorf("create fixture directory: %w", err)
	}
	return nil
}

// writeFixture writes every artifact and the indented run manifest.
func writeFixture(output string, files map[string][]byte, manifest map[string]any) error {
	for relativePath, data := range files {
		path := filepath.Join(output, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(path), directoryMode); err != nil {
			return fmt.Errorf("create directory for %s: %w", relativePath, err)
		}
		if err := os.WriteFile(path, data, fileMode); err != nil {
			return fmt.Errorf("write %s: %w", relativePath, err)
		}
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	manifestPath := filepath.Join(output, "run-manifest.json")
	if err := os.WriteFile(manifestPath, append(manifestData, '\n'), fileMode); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

// roomArtifacts adds the room timeline and mix to files and returns their
// manifest references.
func roomArtifacts(files map[string][]byte, timeline, roomMix []byte) map[string]artifactRef {
	const timelinePath, roomMixPath = "room-timeline.jsonl", "room-mix.wav"
	files[timelinePath] = timeline
	files[roomMixPath] = roomMix
	return map[string]artifactRef{
		"room_timeline": artifactRefFor(timelinePath, files[timelinePath]),
		"room_mix":      artifactRefFor(roomMixPath, files[roomMixPath]),
	}
}

// baseManifest returns the manifest fields every fixture shape shares.
func baseManifest(clock time.Time, elapsed time.Duration, participants map[string]any, artifacts map[string]artifactRef) map[string]any {
	return map[string]any{
		"schema_version": 2,
		"finalized":      true,
		"clock_base":     clock.Format(time.RFC3339Nano),
		"timing": map[string]any{
			"started_at": clock.Format(time.RFC3339Nano),
			"ended_at":   clock.Add(elapsed).Format(time.RFC3339Nano),
			"elapsed":    elapsed.String(),
		},
		"pcm_format": map[string]any{
			"sample_rate_hz":    sampleRate,
			"channels":          1,
			"sample_width_bits": bitsPerSample,
			"byte_order":        "little",
			"encoding":          "signed_pcm16",
		},
		"participants": participants,
		"artifacts":    artifacts,
	}
}

// provenance returns the privacy provenance block with shape-specific fields
// merged over the shared consent, sanitization, and hash statements.
func provenance(fields map[string]any) map[string]any {
	result := map[string]any{
		"consent_review": "not applicable; all samples are generated from a fixed seeded pseudo-speech signal",
		"sanitization":   "no real speech entered the fixture; participant identities and prompts are synthetic",
		"hashes":         "sha256 and byte sizes for every replay artifact are embedded in this manifest",
	}
	for key, value := range fields {
		result[key] = value
	}
	return result
}

// Shared analysis tolerance profile values recorded in every manifest.
const (
	toleranceSilenceFloorDBFS     = -50
	toleranceBoundaryDelta        = 6000
	toleranceBoundaryQuietDBFS    = -24
	toleranceClipSampleThreshold  = 32700
	toleranceEdgeSampleThreshold  = 1000
	toleranceFinalFrameMaxRMSDBFS = -40
	toleranceMinPeerCorrelation   = 0.55
	toleranceMaxSelfCorrelation   = 0.30
	toleranceBargeInSpeechDBFS    = -40
	toleranceMaxLoudnessDiffDB    = 6
	toleranceMaxDriftFraction     = 0.001
)

// tolerances returns the shared analysis tolerance profile under name.
func tolerances(name string) map[string]any {
	return map[string]any{
		"name":                           name,
		"frame_duration":                 "20ms",
		"silence_floor_dbfs":             toleranceSilenceFloorDBFS,
		"max_natural_pause":              "750ms",
		"boundary_delta":                 toleranceBoundaryDelta,
		"boundary_quiet_dbfs":            toleranceBoundaryQuietDBFS,
		"clip_sample_threshold":          toleranceClipSampleThreshold,
		"edge_sample_threshold":          toleranceEdgeSampleThreshold,
		"final_frame_max_rms_dbfs":       toleranceFinalFrameMaxRMSDBFS,
		"correlation_lag_window":         map[string]any{"min": "-100ms", "max": "100ms"},
		"correlation_silence_floor_dbfs": toleranceSilenceFloorDBFS,
		"min_peer_correlation":           toleranceMinPeerCorrelation,
		"max_self_correlation":           toleranceMaxSelfCorrelation,
		"barge_in_speech_threshold_dbfs": toleranceBargeInSpeechDBFS,
		"max_barge_in_latency":           "500ms",
		"max_loudness_difference_db":     toleranceMaxLoudnessDiffDB,
		"max_drift_absolute":             "20ms",
		"max_drift_fraction":             toleranceMaxDriftFraction,
	}
}

func loudnessAnnotation(id string, startMS, endMS int) map[string]any {
	return map[string]any{
		"kind":     "loudness",
		"id":       id,
		"start_ms": startMS,
		"end_ms":   endMS,
		"left":     participantA,
		"right":    participantB,
	}
}

func regenerationCommand(shapeArgs, fixtureDir string) string {
	const generator = "go run ./go-agent-runtime/services/roomreplay/wire/testdata/room-audio/generate.go"
	const fixtureRoot = "go-agent-runtime/services/roomreplay/wire/testdata/room-audio/"
	return generator + shapeArgs + " --output " + fixtureRoot + fixtureDir
}
