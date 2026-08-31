package services

import (
	"context"
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

	// roomEvidenceAudioEncoding, roomEvidenceAudioSampleWidthBits, and
	// roomEvidenceAudioByteOrder describe the room runtime's one and only raw
	// PCM contract (see room.PCM16Format / room.DefaultPCM16Format): signed
	// 16-bit little-endian samples. They are constants, not derived values,
	// because nothing in the room runtime ever records a different width or
	// byte order.
	roomEvidenceAudioEncoding        = "pcm_s16le"
	roomEvidenceAudioSampleWidthBits = 16
	roomEvidenceAudioByteOrder       = "little"
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
	source         platformclock.Source
	providerErrors map[string]struct{}

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
	// events is the participant-level event stream artifact role required by
	// replay bundle admission (roomReplayArtifactRoleEvents), independently
	// declared from deltas so the two artifact roles never share one
	// filesystem path (the replay reader treats two roles claiming the same
	// path as an ownership conflict). It currently carries the same
	// wall-clock-stamped StreamMessage content as deltas: every event this
	// participant's session observed.
	events *selfPlayJSONLWriter

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
	// Events is always populated (see roomParticipantEvidence.events above).
	Events string `json:"events"`
	// Capture is empty for a human participant: it has no provider session
	// to capture. It is also empty for a provider participant whose live
	// session was constructed through an injected SessionInferencer/custom
	// SessionFactory instead of the real websocket dialer (deterministic
	// tests that never touch a real or hermetic websocket transport cannot
	// produce one); recording only happens on the genuine live-construction
	// path, matching how solo `agent session run --record` behaves.
	Capture string `json:"capture,omitempty"`
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
		providerErrors:       make(map[string]struct{}, len(manifest.Participants)),
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
		artifactPaths := roomEvidenceArtifactPaths{
			WAV:         "agent-" + stem + ".wav",
			Diagnostics: "agent-" + stem + ".diagnostics.jsonl",
			Deltas:      "agent-" + stem + ".deltas.jsonl",
			SentPCM:     filepath.Join("participants", stem, "sent.pcm"),
			ReceivedPCM: filepath.Join("participants", stem, "received.pcm"),
			Events:      filepath.Join("participants", stem, "events.jsonl"),
		}
		if room.NormalizeParticipantKind(participant.Kind) != room.ParticipantKindHuman {
			artifactPaths.Capture = filepath.Join("participants", stem, "capture.json")
		}
		participantEvidence := &roomParticipantEvidence{
			owner:          evidence,
			id:             participant.ID,
			artifacts:      artifactPaths,
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
		participantEvidence.events, err = newSelfPlayJSONLWriter(filepath.Join(evidence.destination, participantEvidence.artifacts.Events))
		if err != nil {
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create room participant %q event evidence: %w", participant.ID, err)
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

// recordFinalTimelineEvent records the room's terminal timeline entry and
// returns the exact wall-clock instant it was recorded at (the room's start
// time plus the offset actually written), so a caller that also declares the
// room's overall span end (run-manifest.json's timing.ended_at) can use that
// same instant rather than an independently-read timestamp that necessarily
// precedes it. Returns the zero Time if there is no timeline to record to.
func (e *roomEvidence) recordFinalTimelineEvent(event, participant string, fields map[string]string) time.Time {
	if e == nil || e.timeline == nil {
		return time.Time{}
	}
	offset, _, err := e.timeline.recordNow(event, participant, fields)
	if err != nil {
		e.recordError("", RoomEvidenceTimelinePath, fmt.Errorf("record %s: %w", event, err))
	}
	return e.clock.start.Add(offset).UTC()
}

func (e *roomEvidence) recordProviderErrorTimeline(participant string, fields map[string]string) {
	if e == nil || e.timeline == nil {
		return
	}
	e.mu.Lock()
	if e.providerErrors == nil {
		e.providerErrors = make(map[string]struct{})
	}
	if _, seen := e.providerErrors[participant]; seen {
		e.mu.Unlock()
		return
	}
	e.providerErrors[participant] = struct{}{}
	e.mu.Unlock()
	e.recordTimelineEvent("provider_error", participant, fields)
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
		for _, path := range []string{paths.WAV, paths.Diagnostics, paths.Deltas, paths.SentPCM, paths.ReceivedPCM, paths.Events, paths.Capture} {
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
		if participant.events != nil {
			_ = participant.events.close()
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
			filepath.Join(e.destination, participant.artifacts.Events),
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
	deltaErr := p.recordError(p.artifacts.Deltas, p.deltas.writeRaw(stamped))
	// events.jsonl is the replay bundle's independently-declared participant
	// event stream (roomReplayArtifactRoleEvents): the replay reader requires
	// it as its own artifact, distinct from deltas.jsonl, so it cannot simply
	// alias the same file. It carries the same wall-clock-stamped record.
	eventsErr := error(nil)
	if p.events != nil {
		eventsErr = p.recordError(p.artifacts.Events, p.events.writeRaw(stamped))
	} else {
		eventsErr = p.recordError(p.artifacts.Events, errors.New("room participant event sink is not initialized"))
	}
	return errors.Join(deltaErr, eventsErr)
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
		// run_terminated's own timestamp is read fresh here, strictly after
		// the caller captured endedAt. Any real time elapsed between those
		// two independent clock reads (even nanoseconds) would otherwise let
		// this final timeline event's own offset exceed the room span the
		// manifest declares it must fall inside -- which the replay reader
		// rejects. Recording it through recordFinalTimelineEvent and folding
		// its own instant into endedAt (never earlier, only later) makes the
		// declared span the true superset of everything actually recorded,
		// by construction instead of by racing two clock reads.
		if finalizedAt := e.recordFinalTimelineEvent("run_terminated", "", map[string]string{"reason": string(reason)}); finalizedAt.After(endedAt) {
			endedAt = finalizedAt
		}

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
			if participant.events != nil {
				if err := participant.events.close(); err != nil {
					e.recordError(participant.id, participant.artifacts.Events, err)
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
