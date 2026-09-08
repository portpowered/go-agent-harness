package agentruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const roomEvidenceDirectoryMode = 0o700

// roomEvidenceArtifactIntegrity is one entry of roomEvidenceManifest's
// artifact_integrity map: the declared size and sha256 digest of one
// artifact file, computed from the file actually written to disk.
type roomEvidenceArtifactIntegrity struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// roomEvidenceHashArtifact stats and hashes one bundle-relative artifact path
// and reports whether it could be read. A missing or unreadable file (for
// example one behind a degraded recording sink) is reported as absent rather
// than as an integrity entry with a size of zero: an absent entry correctly
// makes replay admission reject that artifact as incomplete, instead of
// admitting a zero-byte stand-in as if it were the real, complete artifact.
func roomEvidenceHashArtifact(destination, relativePath string) (roomEvidenceArtifactIntegrity, bool) {
	if strings.TrimSpace(relativePath) == "" {
		return roomEvidenceArtifactIntegrity{}, false
	}
	data, err := os.ReadFile(filepath.Join(destination, relativePath))
	if err != nil {
		return roomEvidenceArtifactIntegrity{}, false
	}
	digest := sha256.Sum256(data)
	return roomEvidenceArtifactIntegrity{Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}, true
}

// hashArtifactInto is writeManifest's artifact_integrity population helper:
// it hashes one bundle-relative artifact path and, if the file could be
// read, records its size/sha256 into integrity keyed by that same path (the
// key the replay reader's artifact-metadata merge looks entries up by, not
// the artifact's role name). A relativePath that is empty (an artifact role
// a participant does not have, such as "capture" for a human participant) is
// silently skipped.
func (e *roomEvidence) hashArtifactInto(integrity map[string]roomEvidenceArtifactIntegrity, relativePath string) {
	if relativePath == "" {
		return
	}
	if entry, ok := roomEvidenceHashArtifact(e.destination, relativePath); ok {
		integrity[relativePath] = entry
	}
}

// roomEvidenceSource and roomEvidenceStart establish one timestamp source for
// every evidence writer. Keeping admission here with artifact integrity makes
// the evidence lifecycle explicit: all resources are opened from one admitted
// state, then finalized and hashed from that same state.
func roomEvidenceSource(sources []platformclock.Source) platformclock.Source {
	if len(sources) == 0 {
		return nil
	}
	return sources[0]
}

func roomEvidenceStart(startedAt time.Time, source platformclock.Source) time.Time {
	if startedAt.IsZero() {
		startedAt = source.Now()
	}
	return startedAt.UTC()
}

func normalizedRoomEvidenceFormat(format room.PCM16Format) room.PCM16Format {
	if format.SampleRate <= 0 {
		return room.DefaultPCM16Format()
	}
	return format
}

func newRoomEvidenceState(destination string, manifest room.Manifest, format room.PCM16Format, secrets []string, startedAt time.Time, source platformclock.Source, latencyService runtimeRooms.LatencyService) *roomEvidence {
	var latency runtimeRooms.LatencyRecorder
	if latencyService != nil {
		latency = latencyService.NewRecorder(source, runtimeRooms.AudioFormat{
			SampleRate: format.SampleRate, Channels: format.Channels, FrameDuration: format.FrameDuration,
		})
	}
	evidence := &roomEvidence{
		destination:          filepath.Clean(destination),
		startedAt:            startedAt,
		manifest:             manifest,
		secrets:              append([]string(nil), secrets...),
		participants:         make(map[string]*roomParticipantEvidence, len(manifest.Participants)),
		participantRecordErr: make(map[string]error, len(manifest.Participants)),
		artifactRecordErr:    make(map[string]error),
		audioFormat:          format,
		latency:              latency,
		source:               source,
		providerErrors:       make(map[string]struct{}, len(manifest.Participants)),
	}
	evidence.clock = newRoomClock(startedAt, source)
	evidence.mix = newRoomMixBuffer(format.SampleRate)
	return evidence
}

func (e *roomEvidence) openTimeline() error {
	timeline, err := newRoomTimeline(filepath.Join(e.destination, RoomEvidenceTimelinePath), e.clock)
	if err != nil {
		e.cleanupSetup()
		return fmt.Errorf("create room timeline evidence: %w", err)
	}
	e.timeline = timeline
	return nil
}

func (e *roomEvidence) openParticipants() error {
	stems := roomEvidenceArtifactStems(e.manifest.Participants)
	for _, participant := range e.manifest.Participants {
		if err := e.openParticipant(participant, stems[participant.ID]); err != nil {
			e.cleanupSetup()
			return err
		}
	}
	return nil
}

func (e *roomEvidence) openParticipant(participant room.Participant, stem string) error {
	paths := roomEvidenceParticipantArtifactPaths(participant, stem)
	participantEvidence := &roomParticipantEvidence{
		owner:          e,
		id:             participant.ID,
		artifacts:      paths,
		sentSpeech:     &roomSpeechTracker{},
		receivedSpeech: &roomSpeechTracker{},
	}
	e.participants[participant.ID] = participantEvidence

	directory := filepath.Join(e.destination, "participants", stem)
	if err := os.MkdirAll(directory, roomEvidenceDirectoryMode); err != nil {
		return fmt.Errorf("create room participant %q evidence directory: %w", participant.ID, err)
	}
	var err error
	participantEvidence.audio, err = newSelfPlayWAVRecorder(filepath.Join(e.destination, paths.WAV), e.audioFormat.SampleRate)
	if err != nil {
		return fmt.Errorf("create room participant %q WAV evidence: %w", participant.ID, err)
	}
	participantEvidence.diagnostics, err = newSelfPlayJSONLWriter(filepath.Join(e.destination, paths.Diagnostics))
	if err != nil {
		return fmt.Errorf("create room participant %q diagnostics evidence: %w", participant.ID, err)
	}
	participantEvidence.deltas, err = newSelfPlayJSONLWriter(filepath.Join(e.destination, paths.Deltas))
	if err != nil {
		return fmt.Errorf("create room participant %q delta evidence: %w", participant.ID, err)
	}
	participantEvidence.events, err = newSelfPlayJSONLWriter(filepath.Join(e.destination, paths.Events))
	if err != nil {
		return fmt.Errorf("create room participant %q event evidence: %w", participant.ID, err)
	}
	participantEvidence.sentPCM, err = newRawPCMWriter(filepath.Join(e.destination, paths.SentPCM))
	if err != nil {
		return fmt.Errorf("create room participant %q sent-audio evidence: %w", participant.ID, err)
	}
	participantEvidence.receivedPCM, err = newRawPCMWriter(filepath.Join(e.destination, paths.ReceivedPCM))
	if err != nil {
		return fmt.Errorf("create room participant %q received-audio evidence: %w", participant.ID, err)
	}
	return nil
}

func roomEvidenceParticipantArtifactPaths(participant room.Participant, stem string) roomEvidenceArtifactPaths {
	paths := roomEvidenceArtifactPaths{
		WAV:         "agent-" + stem + ".wav",
		Diagnostics: "agent-" + stem + ".diagnostics.jsonl",
		Deltas:      "agent-" + stem + ".deltas.jsonl",
		SentPCM:     filepath.Join("participants", stem, "sent.pcm"),
		ReceivedPCM: filepath.Join("participants", stem, "received.pcm"),
		Events:      filepath.Join("participants", stem, "events.jsonl"),
	}
	if room.NormalizeParticipantKind(participant.Kind) != room.ParticipantKindHuman {
		paths.Capture = filepath.Join("participants", stem, "capture.json")
	}
	return paths
}

func (e *roomEvidence) finalizeResult(result RoomResult, runErr error, endedAt time.Time) {
	reason := roomResultTerminationReason(result)
	// run_terminated's own timestamp is read fresh here, strictly after the
	// caller captured endedAt. Folding that instant into the declared span keeps
	// every timeline record inside the replay interval.
	if finalizedAt := e.recordFinalTimelineEvent("run_terminated", "", map[string]string{"reason": string(reason)}); finalizedAt.After(endedAt) {
		endedAt = finalizedAt
	}
	e.closeParticipantArtifacts()
	e.closeRoomArtifacts(endedAt)
	if latencyErr := e.writeLatencyBundle(); latencyErr != nil {
		e.recordError("", runtimeRooms.RoomLatencyArtifactPath, latencyErr)
	}
	// Recording degradation is deliberately excluded from runErr: the room
	// result and its runtime termination reason describe live work, while
	// recording_status/degraded_artifacts describe evidence health.
	manifestErr := e.writeManifest(result, runErr, endedAt.UTC())
	if manifestErr != nil {
		e.recordError("", RoomEvidenceManifestPath, manifestErr)
	}
	e.finalizeErr = errors.Join(e.err(), manifestErr)
}

func roomResultTerminationReason(result RoomResult) RoomTerminationReason {
	if result.TerminationReason != "" {
		return result.TerminationReason
	}
	return result.Reason
}

func (e *roomEvidence) closeParticipantArtifacts() {
	for _, participant := range e.participants {
		e.closeParticipantArtifactsFor(participant)
	}
}

func (e *roomEvidence) closeParticipantArtifactsFor(participant *roomParticipantEvidence) {
	if participant == nil {
		return
	}
	e.closeParticipantStructuredArtifacts(participant)
	e.closeParticipantPCMArtifacts(participant)
}

func (e *roomEvidence) closeParticipantStructuredArtifacts(participant *roomParticipantEvidence) {
	if participant.audio != nil {
		if err := participant.audio.close(); err != nil {
			e.recordError(participant.id, participant.artifacts.WAV, err)
		}
	}
	if participant.diagnostics != nil {
		if err := participant.diagnostics.close(); err != nil {
			e.recordError(participant.id, participant.artifacts.Diagnostics, err)
		}
	}
	if participant.deltas != nil {
		if err := participant.deltas.close(); err != nil {
			e.recordError(participant.id, participant.artifacts.Deltas, err)
		}
	}
	if participant.events != nil {
		if err := participant.events.close(); err != nil {
			e.recordError(participant.id, participant.artifacts.Events, err)
		}
	}
}

func (e *roomEvidence) closeParticipantPCMArtifacts(participant *roomParticipantEvidence) {
	if participant.sentPCM != nil {
		if err := participant.sentPCM.close(); err != nil {
			e.recordError(participant.id, participant.artifacts.SentPCM, err)
		}
	}
	if participant.receivedPCM != nil {
		if err := participant.receivedPCM.close(); err != nil {
			e.recordError(participant.id, participant.artifacts.ReceivedPCM, err)
		}
	}
}

func (e *roomEvidence) closeRoomArtifacts(endedAt time.Time) {
	if e.timeline != nil {
		if err := e.timeline.close(); err != nil {
			e.recordError("", RoomEvidenceTimelinePath, err)
		}
	}
	if e.mix == nil {
		return
	}
	span := endedAt.Sub(e.startedAt)
	if span < 0 {
		span = 0
	}
	if err := e.mix.finalize(span, filepath.Join(e.destination, RoomEvidenceMixPath)); err != nil {
		e.recordError("", RoomEvidenceMixPath, fmt.Errorf("write room mix evidence: %w", err))
	}
}
