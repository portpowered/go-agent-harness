package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	// RoomEvidenceManifestPath is the stable filename written inside a room's
	// output directory. Every other artifact path in that manifest is relative
	// to the same directory.
	RoomEvidenceManifestPath = "run-manifest.json"
	// RoomRunManifestPath is the descriptive alias used by room-run callers.
	RoomRunManifestPath = RoomEvidenceManifestPath

	roomEvidenceSchemaVersion = 1
)

// roomEvidence owns all file-backed observations for one room. Participant
// sinks are created before any session is connected so a later startup or
// transport failure still leaves a complete, inspectable artifact set.
type roomEvidence struct {
	destination  string
	startedAt    time.Time
	manifest     room.Manifest
	secrets      []string
	participants map[string]*roomParticipantEvidence
	latency      *roomLatencyRecorder
	// source is the injectable platform clock the latency recorder samples;
	// distinct from roomClock below, which anchors offsets to room start.
	source platformclock.Source

	// clock anchors every recorded wall-clock timestamp (deltas, diagnostics,
	// room-timeline) to the room's real start time instead of the Unix epoch.
	clock       roomClock
	mix         *roomMixBuffer
	timeline    *roomTimeline
	audioFormat room.PCM16Format

	mu        sync.Mutex
	recordErr error
	// participantRecordErr and artifactRecordErr are the evidence-side
	// equivalent of transcript.RecordingStatus. They retain only the first
	// error for each scope so a degraded sink cannot replace the original cause
	// with a later cascade from the same failed file.
	participantRecordErr map[string]error
	artifactRecordErr    map[string]error

	finalizeOnce sync.Once
	finalizeErr  error
}

type roomParticipantEvidence struct {
	owner *roomEvidence
	id    string

	artifacts   roomEvidenceArtifactPaths
	audio       *selfPlayWAVRecorder
	diagnostics *selfPlayJSONLWriter
	deltas      *selfPlayJSONLWriter

	// sentPCM/receivedPCM capture both directions of this participant's raw
	// audio: sentPCM is what this participant spoke into the room (mirrors
	// audio, without a WAV header); receivedPCM is what the room actually
	// delivered to this participant (the mixed inbound stream), which earlier
	// bundles never captured at all.
	sentPCM        *rawPCMWriter
	receivedPCM    *rawPCMWriter
	sentSpeech     *roomSpeechTracker
	receivedSpeech *roomSpeechTracker
}

type roomEvidenceArtifactPaths struct {
	WAV         string `json:"wav"`
	Diagnostics string `json:"diagnostics"`
	Deltas      string `json:"deltas"`
	SentPCM     string `json:"sent_pcm"`
	ReceivedPCM string `json:"received_pcm"`
}

func newRoomEvidence(destination string, manifest room.Manifest, format room.PCM16Format, secrets []string, startedAt time.Time, sources ...platformclock.Source) (*roomEvidence, error) {
	if strings.TrimSpace(destination) == "" {
		return nil, errors.New("room evidence output directory is empty")
	}
	var source platformclock.Source
	if len(sources) > 0 {
		source = sources[0]
	}
	clock := platformclock.Ensure(source)
	if startedAt.IsZero() {
		startedAt = clock.Now().UTC()
	}
	if format.SampleRate <= 0 {
		format = room.DefaultPCM16Format()
	}

	evidence := &roomEvidence{
		destination:          filepath.Clean(destination),
		startedAt:            startedAt.UTC(),
		manifest:             manifest,
		secrets:              append([]string(nil), secrets...),
		participants:         make(map[string]*roomParticipantEvidence, len(manifest.Participants)),
		participantRecordErr: make(map[string]error, len(manifest.Participants)),
		artifactRecordErr:    make(map[string]error),
		audioFormat:          format,
		latency:              newRoomLatencyRecorder(clock, format),
		source:               clock,
	}
	evidence.clock = newRoomClock(evidence.startedAt, clock)
	evidence.mix = newRoomMixBuffer(format.SampleRate)
	var err error
	evidence.timeline, err = newRoomTimeline(filepath.Join(evidence.destination, RoomEvidenceTimelinePath), evidence.clock)
	if err != nil {
		evidence.cleanupSetup()
		return nil, fmt.Errorf("create room timeline evidence: %w", err)
	}

	stems := roomEvidenceArtifactStems(manifest.Participants)
	for _, participant := range manifest.Participants {
		stem := stems[participant.ID]
		participantEvidence := &roomParticipantEvidence{
			owner: evidence,
			id:    participant.ID,
			artifacts: roomEvidenceArtifactPaths{
				WAV:         "agent-" + stem + ".wav",
				Diagnostics: "agent-" + stem + ".diagnostics.jsonl",
				Deltas:      "agent-" + stem + ".deltas.jsonl",
				SentPCM:     filepath.Join("participants", stem, "sent.pcm"),
				ReceivedPCM: filepath.Join("participants", stem, "received.pcm"),
			},
			sentSpeech:     &roomSpeechTracker{},
			receivedSpeech: &roomSpeechTracker{},
		}
		evidence.participants[participant.ID] = participantEvidence

		if err := os.MkdirAll(filepath.Join(evidence.destination, "participants", stem), 0o700); err != nil {
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create room participant %q evidence directory: %w", participant.ID, err)
		}

		participantEvidence.audio, err = newSelfPlayWAVRecorder(filepath.Join(evidence.destination, participantEvidence.artifacts.WAV), format.SampleRate)
		if err != nil {
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create room participant %q WAV evidence: %w", participant.ID, err)
		}
		participantEvidence.diagnostics, err = newSelfPlayJSONLWriter(filepath.Join(evidence.destination, participantEvidence.artifacts.Diagnostics))
		if err != nil {
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create room participant %q diagnostics evidence: %w", participant.ID, err)
		}
		participantEvidence.deltas, err = newSelfPlayJSONLWriter(filepath.Join(evidence.destination, participantEvidence.artifacts.Deltas))
		if err != nil {
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create room participant %q delta evidence: %w", participant.ID, err)
		}
		participantEvidence.sentPCM, err = newRawPCMWriter(filepath.Join(evidence.destination, participantEvidence.artifacts.SentPCM))
		if err != nil {
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create room participant %q sent-audio evidence: %w", participant.ID, err)
		}
		participantEvidence.receivedPCM, err = newRawPCMWriter(filepath.Join(evidence.destination, participantEvidence.artifacts.ReceivedPCM))
		if err != nil {
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create room participant %q received-audio evidence: %w", participant.ID, err)
		}
	}
	return evidence, nil
}

// RoomEvidenceTimelinePath and RoomEvidenceMixPath are the stable, top-level
// room evidence filenames documented for downstream lanes (audio-property
// assertions, record/replay) that consume the room bundle layout.
const (
	RoomEvidenceTimelinePath = "room-timeline.jsonl"
	RoomEvidenceMixPath      = "room-mix.wav"
)

// recordTimelineEvent is a nil-safe convenience wrapper so call sites do not
// need to guard both e and e.timeline before recording a room-level event.
func (e *roomEvidence) recordTimelineEvent(event, participant string, fields map[string]string) {
	if e == nil || e.timeline == nil {
		return
	}
	if err := e.timeline.record(event, participant, fields); err != nil {
		e.recordError("", RoomEvidenceTimelinePath, fmt.Errorf("record %s: %w", event, err))
	}
}

func (e *roomEvidence) participant(id string) *roomParticipantEvidence {
	if e == nil {
		return nil
	}
	return e.participants[id]
}

// setParticipantReady enriches the terminal manifest with runtime-selected
// metadata. Human device IDs are resolved by the device registry at startup,
// so they are not necessarily present in the original normalized manifest.
func (e *roomEvidence) setParticipantReady(ready RoomParticipantReady) {
	if e == nil || ready.ParticipantID == "" {
		return
	}
	for index := range e.manifest.Participants {
		participant := &e.manifest.Participants[index]
		if participant.ID != ready.ParticipantID {
			continue
		}
		participant.Kind = room.NormalizeParticipantKind(ready.Kind)
		participant.InputDevice = ready.InputDevice
		participant.OutputDevice = ready.OutputDevice
		participant.Provider = ready.Provider
		participant.Model = ready.Model
		return
	}
}

// recordError records an evidence-only failure. The runtime must not observe
// this as a participant or room failure: a sink can be unavailable while the
// conversation remains healthy. The raw error stays private to the evidence
// owner; all public status/manifest projections redact it at snapshot time.
func (e *roomEvidence) recordError(participantID, artifact string, err error) {
	if e == nil || err == nil {
		return
	}
	participantID = strings.TrimSpace(participantID)
	artifact = filepath.ToSlash(strings.TrimSpace(artifact))
	prefix := "room evidence"
	if participantID != "" {
		prefix = fmt.Sprintf("participant %q evidence", participantID)
	}
	if artifact != "" {
		prefix += " artifact " + artifact
	}
	wrapped := fmt.Errorf("%s: %w", prefix, err)
	e.mu.Lock()
	if e.recordErr == nil {
		e.recordErr = wrapped
	}
	if participantID != "" {
		if e.participantRecordErr == nil {
			e.participantRecordErr = make(map[string]error)
		}
		if _, exists := e.participantRecordErr[participantID]; !exists {
			e.participantRecordErr[participantID] = wrapped
		}
	}
	if artifact != "" {
		if e.artifactRecordErr == nil {
			e.artifactRecordErr = make(map[string]error)
		}
		if _, exists := e.artifactRecordErr[artifact]; !exists {
			e.artifactRecordErr[artifact] = wrapped
		}
	}
	e.mu.Unlock()
}

func (p *roomParticipantEvidence) recordError(artifact string, err error) error {
	if err == nil {
		return nil
	}
	if p != nil && p.owner != nil {
		p.owner.recordError(p.id, artifact, err)
	}
	return err
}

func roomRecordingStatus(err error, secrets []string) *transcript.RecordingStatus {
	if err == nil {
		return nil
	}
	reason := strings.TrimSpace(sanitizeRoomError(err, secrets))
	if reason == "" {
		reason = "recording degraded"
	}
	return &transcript.RecordingStatus{State: transcript.RecordingStatusPartial, Reason: reason}
}

func cloneRoomRecordingStatus(status *transcript.RecordingStatus) *transcript.RecordingStatus {
	if status == nil {
		return nil
	}
	clone := *status
	return &clone
}

// recordingHealth snapshots the first degraded reason and the affected
// relative artifact paths without exposing mutable error maps to callers.
func (e *roomEvidence) recordingHealth() (*transcript.RecordingStatus, map[string]string, map[string]*transcript.RecordingStatus, map[string]map[string]string) {
	if e == nil {
		return nil, nil, nil, nil
	}
	e.mu.Lock()
	recordErr := e.recordErr
	artifactErrs := make(map[string]error, len(e.artifactRecordErr))
	for path, err := range e.artifactRecordErr {
		artifactErrs[path] = err
	}
	participantErrs := make(map[string]error, len(e.participantRecordErr))
	for participantID, err := range e.participantRecordErr {
		participantErrs[participantID] = err
	}
	secrets := append([]string(nil), e.secrets...)
	participants := make(map[string]roomEvidenceArtifactPaths, len(e.participants))
	for participantID, participant := range e.participants {
		if participant != nil {
			participants[participantID] = participant.artifacts
		}
	}
	e.mu.Unlock()

	roomStatus := roomRecordingStatus(recordErr, secrets)
	roomArtifacts := make(map[string]string, len(artifactErrs))
	for path, err := range artifactErrs {
		roomArtifacts[path] = strings.TrimSpace(sanitizeRoomError(err, secrets))
	}
	participantStatuses := make(map[string]*transcript.RecordingStatus, len(participantErrs))
	participantArtifacts := make(map[string]map[string]string, len(participants))
	for participantID, err := range participantErrs {
		participantStatuses[participantID] = roomRecordingStatus(err, secrets)
	}
	for participantID, paths := range participants {
		for _, path := range []string{paths.WAV, paths.Diagnostics, paths.Deltas, paths.SentPCM, paths.ReceivedPCM} {
			if err, exists := artifactErrs[filepath.ToSlash(path)]; exists {
				if participantArtifacts[participantID] == nil {
					participantArtifacts[participantID] = make(map[string]string)
				}
				participantArtifacts[participantID][filepath.ToSlash(path)] = strings.TrimSpace(sanitizeRoomError(err, secrets))
			}
		}
	}
	return roomStatus, roomArtifacts, participantStatuses, participantArtifacts
}

func (e *roomEvidence) applyRecordingHealth(result *RoomResult) {
	if e == nil || result == nil {
		return
	}
	roomStatus, roomArtifacts, participantStatuses, _ := e.recordingHealth()
	result.RecordingStatus = cloneRoomRecordingStatus(roomStatus)
	result.DegradedArtifacts = cloneRoomStringMap(roomArtifacts)
	if result.Participants == nil {
		return
	}
	for participantID, participant := range result.Participants {
		participant.RecordingStatus = cloneRoomRecordingStatus(participantStatuses[participantID])
		result.Participants[participantID] = participant
	}
}

func (e *roomEvidence) err() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.recordErr
}

func (e *roomEvidence) cleanupSetup() {
	if e == nil {
		return
	}
	if e.timeline != nil {
		_ = e.timeline.close()
		_ = os.Remove(filepath.Join(e.destination, RoomEvidenceTimelinePath))
	}
	for _, participant := range e.participants {
		if participant == nil {
			continue
		}
		if participant.audio != nil {
			_ = participant.audio.close()
		}
		if participant.diagnostics != nil {
			_ = participant.diagnostics.close()
		}
		if participant.deltas != nil {
			_ = participant.deltas.close()
		}
		if participant.sentPCM != nil {
			_ = participant.sentPCM.close()
		}
		if participant.receivedPCM != nil {
			_ = participant.receivedPCM.close()
		}
		for _, path := range []string{
			filepath.Join(e.destination, participant.artifacts.WAV),
			filepath.Join(e.destination, participant.artifacts.Diagnostics),
			filepath.Join(e.destination, participant.artifacts.Deltas),
			filepath.Join(e.destination, participant.artifacts.SentPCM),
			filepath.Join(e.destination, participant.artifacts.ReceivedPCM),
		} {
			_ = os.Remove(path)
		}
	}
}

// RecordSessionDiagnostic implements SessionDiagnosticSink. The diagnostic
// record is already structured and credential-free; the writer still uses
// the shared redaction path as defense in depth for provider-supplied fields.
func (p *roomParticipantEvidence) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	if p == nil {
		return
	}
	if p.owner == nil {
		return
	}
	if p.diagnostics == nil {
		p.recordError(p.artifacts.Diagnostics, errors.New("diagnostics sink is not initialized"))
		return
	}
	data, err := json.Marshal(selfPlayDiagnosticLine{
		Event:  record.Event,
		Fields: cloneSelfPlayStringMap(record.Fields),
	})
	if err != nil {
		p.recordError(p.artifacts.Diagnostics, fmt.Errorf("marshal diagnostic record: %w", err))
		return
	}
	data = p.owner.redactJSON(data)
	stamped, stampErr := p.owner.stampWallClock(data)
	if stampErr != nil {
		p.recordError(p.artifacts.Diagnostics, fmt.Errorf("stamp diagnostic wall clock: %w", stampErr))
		return
	}
	if err := p.diagnostics.writeRaw(stamped); err != nil {
		p.recordError(p.artifacts.Diagnostics, err)
	}
}

func (p *roomParticipantEvidence) observeDelta(msg messages.StreamMessage) error {
	if p == nil {
		return errors.New("room participant delta sink is not initialized")
	}
	if p.owner == nil || p.deltas == nil {
		return p.recordError(p.artifacts.Deltas, errors.New("room participant delta sink is not initialized"))
	}
	data, err := gwtesting.MarshalStreamMessage(msg)
	if err != nil {
		return p.recordError(p.artifacts.Deltas, fmt.Errorf("marshal stream delta: %w", err))
	}
	stamped, err := p.owner.stampWallClock(p.owner.redactJSON(data))
	if err != nil {
		return p.recordError(p.artifacts.Deltas, fmt.Errorf("stamp delta wall clock: %w", err))
	}
	return p.recordError(p.artifacts.Deltas, p.deltas.writeRaw(stamped))
}

func (p *roomParticipantEvidence) observeAudio(pcm []byte) error {
	if p == nil {
		return errors.New("room participant WAV sink is not initialized")
	}
	if p.audio == nil {
		return p.recordError(p.artifacts.WAV, errors.New("room participant WAV sink is not initialized"))
	}
	return p.recordError(p.artifacts.WAV, p.audio.write(context.Background(), pcm))
}

// observeSentAudio records one chunk of this participant's own outbound
// (spoken) audio: the existing agent-<id>.wav for backward compatibility,
// the new raw participants/<id>/sent.pcm, the room's composite mix at this
// chunk's real wall-clock offset, and a speech_start/speech_end room-timeline
// transition derived from the chunk's own energy.
func (p *roomParticipantEvidence) observeSentAudio(pcm []byte) error {
	if p == nil || p.owner == nil {
		return errors.New("room participant audio evidence is not initialized")
	}
	return errors.Join(p.observeAudio(pcm), p.observeSentStream(pcm))
}

// observeSentStream records everything observeSentAudio does except the
// participant WAV write: the sent-PCM stream, the room mix placement, and the
// speech-segment timeline. It is split out so the room stream observer can keep
// this on the provider-to-peer critical path (where the room mix and timeline
// need the un-delayed offset) while deferring the WAV write until after the
// bounded handoff.
func (p *roomParticipantEvidence) observeSentStream(pcm []byte) error {
	if p == nil || p.owner == nil {
		return errors.New("room participant audio evidence is not initialized")
	}
	var writeErr error
	if p.sentPCM != nil {
		writeErr = p.recordError(p.artifacts.SentPCM, p.sentPCM.write(pcm))
	} else {
		writeErr = p.recordError(p.artifacts.SentPCM, errors.New("room participant sent-audio sink is not initialized"))
	}
	offset, _ := p.owner.clock.now()
	if p.owner.mix != nil {
		p.owner.mix.mixAt(offset, pcm)
	}
	if event := p.sentSpeech.transition(pcm16HasSignal(pcm)); event != "" {
		p.owner.recordTimelineEvent("speech_"+event, p.id, nil)
	}
	return writeErr
}

// closeSentSpeechSegment force-closes an in-progress sent-speech segment on
// an explicit AUDIO.END boundary, since a provider audio stream can end
// without ever emitting a silent trailing chunk for the energy-based tracker
// to observe.
func (p *roomParticipantEvidence) closeSentSpeechSegment() {
	if p == nil || p.sentSpeech == nil {
		return
	}
	if event := p.sentSpeech.transition(false); event != "" && p.owner != nil {
		p.owner.recordTimelineEvent("speech_"+event, p.id, nil)
	}
}

// observeReceivedAudio records one chunk of what the room actually delivered
// to this participant (the mixed inbound stream fed to SendAudioInput, or the
// human's mixer output): participants/<id>/received.pcm plus a
// received_speech_start/end room-timeline transition. This is the artifact
// that makes room mixing/delivery observable at all -- earlier bundles
// recorded only each participant's own output, never what it received.
func (p *roomParticipantEvidence) observeReceivedAudio(pcm []byte) error {
	if p == nil {
		return errors.New("room participant audio evidence is not initialized")
	}
	var writeErr error
	if p.receivedPCM != nil {
		writeErr = p.recordError(p.artifacts.ReceivedPCM, p.receivedPCM.write(pcm))
	} else {
		writeErr = p.recordError(p.artifacts.ReceivedPCM, errors.New("room participant received-audio sink is not initialized"))
	}
	if p.owner != nil {
		if event := p.receivedSpeech.transition(pcm16HasSignal(pcm)); event != "" {
			p.owner.recordTimelineEvent("received_speech_"+event, p.id, nil)
		}
	}
	return writeErr
}

// recordAudioDropped emits an explicit diagnostic when incoming audio that
// carried real signal was not forwarded to this participant's session,
// instead of leaving that failure indistinguishable from ordinary silence
// (the earlier symptom: a silent input_audio_bytes: 0 that hid a real
// delivery defect).
func (p *roomParticipantEvidence) recordAudioDropped(reason string, byteCount int) {
	if p == nil {
		return
	}
	fields := map[string]string{"reason": reason, "bytes": strconv.Itoa(byteCount)}
	p.RecordSessionDiagnostic(SessionDiagnosticRecord{Event: "room.audio.input_dropped", Fields: fields})
	if p.owner != nil {
		p.owner.recordTimelineEvent("audio_input_dropped", p.id, fields)
	}
}

// stampWallClock adds t_offset_ms/t_unix_ms to an encoded JSON event using
// this room's shared clock.
func (e *roomEvidence) stampWallClock(data []byte) ([]byte, error) {
	if e == nil {
		return data, nil
	}
	offset, unixMs := e.clock.now()
	return injectRoomWallClock(data, offset, unixMs)
}

func (e *roomEvidence) redactText(value string) string {
	if e == nil || value == "" {
		return value
	}
	for _, secret := range e.secrets {
		value = redactSelfPlayError(value, secret)
	}
	return value
}

// redactJSON walks decoded JSON strings before re-encoding them. Redacting
// the serialized bytes directly can remove a closing quote when an error
// string contains an authorization marker, leaving an invalid artifact.
func (e *roomEvidence) redactJSON(data []byte) []byte {
	if e == nil || len(data) == 0 {
		return data
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		// All callers provide valid JSON. Keep a defensive fallback that still
		// replaces exact credential values if a future caller violates that
		// invariant.
		return []byte(e.redactText(string(data)))
	}
	redacted := redactRoomJSONValue(value, e.redactText)
	result, err := json.Marshal(redacted)
	if err != nil {
		return data
	}
	return result
}

func (e *roomEvidence) observeSpeakerAudio(sourceID string, targetIDs []string, pcm []byte) {
	if e == nil || e.latency == nil {
		return
	}
	e.latency.observeSpeakerAudio(sourceID, targetIDs, pcm)
}

func (e *roomEvidence) observeSpeechStopped(participantID string) {
	if e == nil || e.latency == nil {
		return
	}
	e.latency.observeSpeechStopped(participantID)
}

func (e *roomEvidence) observeProviderAudio(participantID string, responseID string) {
	if e == nil || e.latency == nil {
		return
	}
	e.latency.observeProviderAudio(participantID, responseID)
}

func (e *roomEvidence) observePeerAudio(sourceID, targetID string, pcm []byte) {
	if e == nil || e.latency == nil {
		return
	}
	e.latency.observePeerAudio(sourceID, targetID, pcm)
}

func redactRoomJSONValue(value any, redact func(string) string) any {
	switch typed := value.(type) {
	case string:
		return redact(typed)
	case []any:
		for index := range typed {
			typed[index] = redactRoomJSONValue(typed[index], redact)
		}
	case map[string]any:
		for key, nested := range typed {
			typed[key] = redactRoomJSONValue(nested, redact)
		}
	}
	return value
}

func (e *roomEvidence) finalize(result RoomResult, runErr error, endedAt time.Time) error {
	if e == nil {
		return nil
	}
	e.finalizeOnce.Do(func() {
		reason := result.TerminationReason
		if reason == "" {
			reason = result.Reason
		}
		e.recordTimelineEvent("run_terminated", "", map[string]string{"reason": string(reason)})

		for _, participant := range e.participants {
			if participant == nil {
				continue
			}
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
		if e.timeline != nil {
			if err := e.timeline.close(); err != nil {
				e.recordError("", RoomEvidenceTimelinePath, err)
			}
		}
		if e.mix != nil {
			span := endedAt.Sub(e.startedAt)
			if span < 0 {
				span = 0
			}
			if mixErr := e.mix.finalize(span, filepath.Join(e.destination, RoomEvidenceMixPath)); mixErr != nil {
				e.recordError("", RoomEvidenceMixPath, fmt.Errorf("write room mix evidence: %w", mixErr))
			}
		}

		if latencyErr := e.writeLatencyBundle(); latencyErr != nil {
			e.recordError("", RoomLatencyArtifactPath, latencyErr)
		}
		// Recording degradation is deliberately excluded from runErr: the room
		// result and its runtime termination reason describe live work, while
		// recording_status/degraded_artifacts describe evidence health.
		manifestErr := e.writeManifest(result, runErr, endedAt.UTC())
		if manifestErr != nil {
			e.recordError("", RoomEvidenceManifestPath, manifestErr)
		}
		e.finalizeErr = errors.Join(e.err(), manifestErr)
	})
	return e.finalizeErr
}

type roomEvidenceManifest struct {
	SchemaVersion     int                                        `json:"schema_version"`
	Finalized         bool                                       `json:"finalized"`
	Timing            roomEvidenceTiming                         `json:"timing"`
	Bounds            roomEvidenceBounds                         `json:"bounds"`
	TerminationReason RoomTerminationReason                      `json:"termination_reason"`
	Reason            RoomTerminationReason                      `json:"reason,omitempty"`
	Participants      map[string]roomEvidenceParticipantManifest `json:"participants"`
	TurnCounts        map[string]int                             `json:"turn_counts"`
	// AudioFormat names the raw PCM16 rate/channel contract shared by every
	// sent.pcm/received.pcm and the WAV/mix artifacts, so a bundle reader
	// never has to guess it.
	AudioFormat roomEvidenceAudioFormat `json:"audio_format"`
	// RoomMix and RoomTimeline are the two room-level (not per-participant)
	// artifacts: the composite "fly on the wall" mix and the ordered,
	// wall-clock-stamped log of the conversation's shape.
	RoomMix      string            `json:"room_mix"`
	RoomTimeline string            `json:"room_timeline"`
	Artifacts    map[string]string `json:"artifacts"`
	// RecordingStatus follows transcript's shared complete/partial contract;
	// a partial room bundle is still a valid room result with degraded
	// evidence, not a failed conversation.
	RecordingStatus   *transcript.RecordingStatus `json:"recording_status,omitempty"`
	DegradedArtifacts map[string]string           `json:"degraded_artifacts,omitempty"`
	Error             string                      `json:"error,omitempty"`
}

type roomEvidenceAudioFormat struct {
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Encoding   string `json:"encoding"`
}

type roomEvidenceTiming struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Elapsed   string `json:"elapsed"`
	// ClockBase is the same instant as StartedAt, named explicitly for
	// downstream lanes that compute per-turn/cross-participant latency by
	// subtracting this anchor from every recorded t_unix_ms field. Earlier
	// bundles had no such anchor at all (or, in the unrelated single-session
	// recording path, a fixed 1970-01-01 placeholder that made latency
	// uncomputable); this is always the room's real start time.
	ClockBase string `json:"clock_base"`
}

type roomEvidenceBounds struct {
	MaxTurns    int    `json:"max_turns,omitempty"`
	MaxDuration string `json:"max_duration,omitempty"`
}

type roomEvidenceParticipantManifest struct {
	ID                 string                       `json:"id"`
	Kind               room.ParticipantKind         `json:"kind"`
	SystemPrompt       string                       `json:"system_prompt"`
	OpeningPrompt      string                       `json:"opening_prompt,omitempty"`
	Provider           string                       `json:"provider"`
	Model              string                       `json:"model"`
	APIKeyEnv          string                       `json:"api_key_env"`
	Voice              string                       `json:"voice,omitempty"`
	Tools              []string                     `json:"tools"`
	BrowserTools       *room.BrowserToolsConfig     `json:"browser_tools,omitempty"`
	CompletedTurns     int                          `json:"completed_turns"`
	TerminationReason  ParticipantTerminationReason `json:"termination_reason"`
	Reason             ParticipantTerminationReason `json:"reason,omitempty"`
	Connected          bool                         `json:"connected"`
	Classification     string                       `json:"classification,omitempty"`
	TerminalReason     messages.TerminalReason      `json:"terminal_reason,omitempty"`
	TerminalProvenance messages.TerminalProvenance  `json:"terminal_provenance,omitempty"`
	OutputState        messages.TerminalOutputState `json:"output_state,omitempty"`
	InputDevice        string                       `json:"input_device,omitempty"`
	OutputDevice       string                       `json:"output_device,omitempty"`
	Error              string                       `json:"error,omitempty"`
	Artifacts          roomEvidenceArtifactPaths    `json:"artifacts"`
	RecordingStatus    *transcript.RecordingStatus  `json:"recording_status,omitempty"`
	DegradedArtifacts  map[string]string            `json:"degraded_artifacts,omitempty"`
}

func (e *roomEvidence) writeManifest(result RoomResult, runErr error, endedAt time.Time) error {
	if e == nil {
		return nil
	}
	if endedAt.IsZero() {
		endedAt = e.source.Now().UTC()
	}
	reason := result.TerminationReason
	if reason == "" {
		reason = result.Reason
	}
	if reason == "" {
		reason = RoomTerminationFailed
	}
	recordingStatus, degradedArtifacts, participantStatuses, participantArtifacts := e.recordingHealth()
	manifest := roomEvidenceManifest{
		SchemaVersion: roomEvidenceSchemaVersion,
		Finalized:     runErr == nil,
		Timing: roomEvidenceTiming{
			StartedAt: e.startedAt.UTC().Format(time.RFC3339Nano),
			EndedAt:   endedAt.UTC().Format(time.RFC3339Nano),
			Elapsed:   endedAt.Sub(e.startedAt).String(),
			ClockBase: e.startedAt.UTC().Format(time.RFC3339Nano),
		},
		Bounds:            roomEvidenceBounds{MaxTurns: e.manifest.Room.MaxTurns, MaxDuration: durationString(e.manifest.Room.MaxDuration)},
		TerminationReason: reason,
		Reason:            reason,
		Participants:      make(map[string]roomEvidenceParticipantManifest, len(e.manifest.Participants)),
		TurnCounts:        make(map[string]int, len(e.manifest.Participants)),
		AudioFormat: roomEvidenceAudioFormat{
			SampleRate: e.audioFormat.SampleRate,
			Channels:   e.audioFormat.Channels,
			Encoding:   "pcm_s16le",
		},
		RoomMix:           RoomEvidenceMixPath,
		RoomTimeline:      RoomEvidenceTimelinePath,
		Artifacts:         make(map[string]string, len(e.manifest.Participants)*5+2),
		RecordingStatus:   cloneRoomRecordingStatus(recordingStatus),
		DegradedArtifacts: cloneRoomStringMap(degradedArtifacts),
	}
	manifest.Artifacts["room_mix"] = RoomEvidenceMixPath
	manifest.Artifacts["room_timeline"] = RoomEvidenceTimelinePath
	manifest.Artifacts["room.latency"] = RoomLatencyArtifactPath
	if runErr != nil {
		manifest.Error = e.redactText(runErr.Error())
	}
	for _, participant := range e.manifest.Participants {
		participantEvidence := e.participant(participant.ID)
		participantResult, exists := result.Participants[participant.ID]
		if !exists {
			participantResult = RoomParticipantResult{
				ID:                participant.ID,
				ParticipantID:     participant.ID,
				TerminationReason: ParticipantTerminationError,
				Reason:            ParticipantTerminationError,
				Error:             sanitizeRoomError(runErr, e.secrets),
			}
		}
		participantReason := participantResult.TerminationReason
		if participantReason == "" {
			participantReason = participantResult.Reason
		}
		if participantReason == "" {
			participantReason = ParticipantTerminationError
		}
		paths := roomEvidenceArtifactPaths{}
		if participantEvidence != nil {
			paths = participantEvidence.artifacts
		}
		manifest.Participants[participant.ID] = roomEvidenceParticipantManifest{
			ID:                 participant.ID,
			Kind:               room.NormalizeParticipantKind(participant.Kind),
			SystemPrompt:       e.redactText(participant.SystemPrompt),
			OpeningPrompt:      e.redactText(participant.OpeningPrompt),
			Provider:           e.redactText(participant.Provider),
			Model:              e.redactText(participant.Model),
			APIKeyEnv:          e.redactText(participant.APIKeyEnv),
			Voice:              e.redactText(participant.Voice),
			Tools:              redactRoomStrings(participant.Tools, e.redactText),
			BrowserTools:       participant.BrowserTools,
			CompletedTurns:     participantResult.TurnsCompleted,
			TerminationReason:  participantReason,
			Reason:             participantReason,
			Connected:          participantResult.Connected,
			Classification:     participantResult.Classification,
			TerminalReason:     participantResult.TerminalReason,
			TerminalProvenance: participantResult.TerminalProvenance,
			OutputState:        participantResult.OutputState,
			InputDevice:        e.redactText(participant.InputDevice),
			OutputDevice:       e.redactText(participant.OutputDevice),
			Error:              e.redactText(participantResult.Error),
			Artifacts:          paths,
			RecordingStatus:    cloneRoomRecordingStatus(participantStatuses[participant.ID]),
			DegradedArtifacts:  cloneRoomStringMap(participantArtifacts[participant.ID]),
		}
		manifest.TurnCounts[participant.ID] = participantResult.TurnsCompleted
		manifest.Artifacts[participant.ID+".wav"] = paths.WAV
		manifest.Artifacts[participant.ID+".diagnostics"] = paths.Diagnostics
		manifest.Artifacts[participant.ID+".deltas"] = paths.Deltas
		manifest.Artifacts[participant.ID+".sent_pcm"] = paths.SentPCM
		manifest.Artifacts[participant.ID+".received_pcm"] = paths.ReceivedPCM
	}
	return writeRoomEvidenceManifestFile(filepath.Join(e.destination, RoomEvidenceManifestPath), manifest, e.secrets)
}

func cloneRoomStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func (e *roomEvidence) writeLatencyBundle() error {
	if e == nil || e.latency == nil {
		return nil
	}
	return e.latency.write(filepath.Join(e.destination, RoomLatencyArtifactPath), e.secrets)
}

func writeRoomEvidenceManifestFile(path string, manifest roomEvidenceManifest, secrets []string) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal room run manifest: %w", err)
	}
	data = redactRoomEvidenceJSON(data, secrets)
	data = append(data, '\n')

	destination := filepath.Dir(path)
	temporary, err := os.CreateTemp(destination, ".run-manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create room run manifest temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := writeSelfPlayAll(temporary, data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write room run manifest temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync room run manifest temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close room run manifest temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace room run manifest: %w", err)
	}
	removeTemporary = false
	return nil
}

func redactRoomEvidenceJSON(data []byte, secrets []string) []byte {
	if len(data) == 0 || len(secrets) == 0 {
		return data
	}
	redact := func(value string) string {
		for _, secret := range secrets {
			value = redactSelfPlayError(value, secret)
		}
		return value
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return []byte(redact(string(data)))
	}
	redacted := redactRoomJSONValue(value, redact)
	result, err := json.Marshal(redacted)
	if err != nil {
		return data
	}
	return result
}

func redactRoomStrings(values []string, redact func(string) string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = redact(value)
	}
	return result
}

func prepareRoomEvidenceOutput(path string) (string, error) {
	destination := filepath.Clean(strings.TrimSpace(path))
	if err := ValidateRoomEvidenceOutput(path); err != nil {
		return "", err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return "", fmt.Errorf("create room evidence output directory %q: %w", destination, err)
	}
	return destination, nil
}

// ValidateRoomEvidenceOutput checks that a room evidence destination is a
// safe, writable empty directory target without creating the destination.
// Its parent may be created for the write probe, matching the runtime
// preparation performed immediately before live session construction.
func ValidateRoomEvidenceOutput(path string) error {
	rawPath := strings.TrimSpace(path)
	destination := filepath.Clean(rawPath)
	if rawPath == "" || destination == "." {
		return errors.New("room evidence output directory is required")
	}
	return validateRoomEvidenceOutputTarget(destination)
}

func validateRoomEvidenceOutputTarget(destination string) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("prepare room evidence output parent %q: %w", destination, err)
	}
	info, err := os.Lstat(destination)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("room evidence output target %q must be a non-symlink directory", destination)
		}
		entries, readErr := os.ReadDir(destination)
		if readErr != nil {
			return fmt.Errorf("inspect room evidence output directory %q: %w", destination, readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("room evidence output directory %q is not safe: it must be empty", destination)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect room evidence output target %q: %w", destination, err)
	}
	probe, err := os.CreateTemp(parent, ".room-evidence-probe-")
	if err != nil {
		return fmt.Errorf("probe room evidence output target %q: %w", destination, err)
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	if closeErr != nil {
		return fmt.Errorf("close room evidence output probe %q: %w", destination, closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("remove room evidence output probe %q: %w", destination, removeErr)
	}
	return nil
}

func roomEvidenceArtifactStems(participants []room.Participant) map[string]string {
	counts := make(map[string]int, len(participants))
	base := make(map[string]string, len(participants))
	for _, participant := range participants {
		stem := normalizeRoomArtifactStem(participant.ID)
		base[participant.ID] = stem
		counts[stem]++
	}
	stems := make(map[string]string, len(participants))
	for _, participant := range participants {
		stem := base[participant.ID]
		if counts[stem] > 1 {
			hash := sha256.Sum256([]byte(participant.ID))
			stem += "-" + hex.EncodeToString(hash[:4])
		}
		stems[participant.ID] = stem
	}
	return stems
}

func normalizeRoomArtifactStem(id string) string {
	id = strings.TrimSpace(id)
	var builder strings.Builder
	lastUnsafe := false
	for _, value := range id {
		safe := value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '-' || value == '_' || value == '.'
		if safe {
			builder.WriteRune(value)
			lastUnsafe = false
			continue
		}
		if !lastUnsafe {
			builder.WriteByte('_')
			lastUnsafe = true
		}
	}
	stem := strings.Trim(builder.String(), ".")
	if stem == "" || stem == ".." {
		return "participant"
	}
	return stem
}

func durationString(value time.Duration) string {
	if value <= 0 {
		return ""
	}
	return value.String()
}

func roomCredentialSecrets(manifest room.Manifest, options room.ValidationOptions) []string {
	lookup := options.LookupCredential
	if lookup == nil {
		lookup = os.LookupEnv
	}
	secrets := make([]string, 0, len(manifest.Participants))
	for _, participant := range manifest.Participants {
		if value, ok := lookup(participant.APIKeyEnv); ok && value != "" {
			secrets = append(secrets, value)
		}
	}
	return secrets
}

// roomMixerConfigForOptions centralizes the format selection used by both the
// live mixer and its WAV evidence, preventing a test cadence override from
// producing a misleading artifact header.
func roomMixerConfigForOptions(opts RoomRunOptions) room.PCM16MixerConfig {
	config := opts.MixerConfig
	if opts.PCMFormat != (room.PCM16Format{}) {
		config.Format = opts.PCMFormat
	} else if opts.FrameSamples > 0 {
		format := room.DefaultPCM16Format()
		format.FrameDuration = time.Duration(opts.FrameSamples) * time.Second / time.Duration(format.SampleRate)
		config.Format = format
	}
	return config
}

func roomFormatForOptions(opts RoomRunOptions) room.PCM16Format {
	format := roomMixerConfigForOptions(opts).Format
	if format == (room.PCM16Format{}) {
		return room.DefaultPCM16Format()
	}
	return format
}
