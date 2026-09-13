package agentruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	roomevidencewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/wire"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	RoomEvidenceManifestPath         = roomevidence.ManifestPath
	RoomRunManifestPath              = RoomEvidenceManifestPath
	roomEvidenceSchemaVersion        = roomevidence.SchemaVersion
	roomEvidenceAudioEncoding        = roomevidence.AudioEncoding
	roomEvidenceAudioSampleWidthBits = roomevidence.AudioSampleWidthBit
	roomEvidenceAudioByteOrder       = roomevidence.AudioByteOrder
)

const (
	RoomEvidenceTimelinePath = roomevidence.TimelinePath
	RoomEvidenceMixPath      = roomevidence.MixPath
)

// roomEvidence is a compatibility adapter for the legacy CLI orchestration.
// The adapter retains only the old call shapes; all real evidence resources
// belong to roomevidence. The local sink fields exist solely for old tests
// that construct a partial value directly or close a sink to inject failure.
type roomEvidence struct {
	destination    string
	manifest       room.Manifest
	participants   map[string]*roomParticipantEvidence
	providerErrors map[string]struct{}
	latency        runtimeRooms.LatencyRecorder
	timeline       *roomTimeline
	service        roomevidence.Recorder

	mu           sync.Mutex
	recordErr    error
	finalizeOnce sync.Once
	finalizeErr  error
}

type roomParticipantEvidence struct {
	owner       *roomEvidence
	id          string
	artifacts   roomEvidenceArtifactPaths
	deltas      *selfPlayJSONLWriter // test-only failure injection handle
	service     roomevidence.ParticipantRecorder
	compatPaths []string
}

type roomEvidenceArtifactPaths struct {
	WAV         string `json:"wav"`
	Diagnostics string `json:"diagnostics"`
	Deltas      string `json:"deltas"`
	SentPCM     string `json:"sent_pcm"`
	ReceivedPCM string `json:"received_pcm"`
	Events      string `json:"events"`
	Capture     string `json:"capture,omitempty"`
}

func newRoomEvidence(destination string, manifest room.Manifest, format room.PCM16Format, secrets []string, startedAt time.Time, sources ...platformclock.Source) (*roomEvidence, error) {
	return newRoomEvidenceWithLatency(destination, manifest, format, secrets, startedAt, nil, sources...)
}

func newRoomEvidenceWithLatency(destination string, manifest room.Manifest, format room.PCM16Format, secrets []string, startedAt time.Time, latencyService runtimeRooms.LatencyService, sources ...platformclock.Source) (*roomEvidence, error) {
	clock := platformclock.Ensure(roomEvidenceSource(sources))
	startedAt = roomEvidenceStart(startedAt, clock)
	format = normalizedRoomEvidenceFormat(format)
	runtimeFormat := runtimeRooms.AudioFormat{SampleRate: format.SampleRate, Channels: format.Channels, FrameDuration: format.FrameDuration}
	var latencyRecorder runtimeRooms.LatencyRecorder
	if latencyService != nil {
		latencyRecorder = latencyService.NewRecorder(clock, runtimeFormat)
	}
	recorder, err := roomevidencewire.NewService().Open(roomevidence.Options{
		Destination:     destination,
		Manifest:        manifest,
		AudioFormat:     runtimeFormat,
		Secrets:         secrets,
		StartedAt:       startedAt,
		Clock:           clock,
		Latency:         latencyService,
		LatencyRecorder: latencyRecorder,
	})
	if err != nil {
		return nil, err
	}
	evidence := &roomEvidence{
		destination: recorder.Destination(), manifest: manifest,
		participants:   make(map[string]*roomParticipantEvidence, len(manifest.Participants)),
		providerErrors: make(map[string]struct{}, len(manifest.Participants)), latency: latencyRecorder,
		service: recorder,
	}
	// recordRoomTimelineEvent historically checked this marker before calling
	// the adapter. It carries no writer; the runtime service owns that file.
	evidence.timeline = &roomTimeline{recorder: recorder}
	for _, configured := range manifest.Participants {
		participant := &roomParticipantEvidence{owner: evidence, id: configured.ID, service: recorder.Participant(configured.ID)}
		paths := participant.service.Artifacts()
		participant.artifacts = roomEvidenceArtifactPaths{WAV: paths.WAV, Diagnostics: paths.Diagnostics, Deltas: paths.Deltas, SentPCM: paths.SentPCM, ReceivedPCM: paths.ReceivedPCM, Events: paths.Events, Capture: paths.Capture}
		// This inert compatibility handle lets the existing degradation test
		// close the legacy field. It is not an evidence artifact and is removed
		// immediately after finalization.
		file, createErr := os.CreateTemp("", ".room-evidence-compat-*.jsonl")
		if createErr != nil {
			if closeErr := recorder.Close(); closeErr != nil {
				createErr = errors.Join(createErr, closeErr)
			}
			return nil, fmt.Errorf("create room evidence compatibility handle: %w", createErr)
		}
		participant.deltas = &selfPlayJSONLWriter{path: file.Name(), file: file}
		participant.compatPaths = append(participant.compatPaths, file.Name())
		evidence.participants[configured.ID] = participant
	}
	return evidence, nil
}

func (e *roomEvidence) participant(id string) *roomParticipantEvidence {
	if e == nil {
		return nil
	}
	return e.participants[id]
}

func (e *roomEvidence) recordTimelineEvent(event, participant string, fields map[string]string) {
	if e == nil || e.timeline == nil {
		return
	}
	if e.service == nil {
		return
	}
	if err := e.service.RecordTimeline(event, participant, fields); err != nil {
		e.recordError("", RoomEvidenceTimelinePath, err)
	}
}

func (e *roomEvidence) recordProviderErrorTimeline(participant string, fields map[string]string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	if _, seen := e.providerErrors[participant]; seen {
		e.mu.Unlock()
		return
	}
	e.providerErrors[participant] = struct{}{}
	e.mu.Unlock()
	e.recordTimelineEvent("provider_error", participant, fields)
}

func (e *roomEvidence) setParticipantReady(ready RoomParticipantReady) {
	if e == nil || ready.ParticipantID == "" {
		return
	}
	for index := range e.manifest.Participants {
		if e.manifest.Participants[index].ID == ready.ParticipantID {
			e.manifest.Participants[index].Kind = ready.Kind
			e.manifest.Participants[index].InputDevice = ready.InputDevice
			e.manifest.Participants[index].OutputDevice = ready.OutputDevice
			e.manifest.Participants[index].Provider = ready.Provider
			e.manifest.Participants[index].Model = ready.Model
		}
	}
	if e.service != nil {
		if err := e.service.SetParticipantReady(runtimeRooms.RoomParticipantReady{ID: ready.ID, ParticipantID: ready.ParticipantID, Kind: ready.Kind, InputDevice: ready.InputDevice, OutputDevice: ready.OutputDevice, Provider: ready.Provider, Model: ready.Model}); err != nil {
			e.recordError(ready.ParticipantID, "", err)
		}
	}
}

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
	if e.service != nil {
		e.service.MarkError(participantID, artifact, err)
		return
	}
	e.mu.Lock()
	if e.recordErr == nil {
		e.recordErr = wrapped
	}
	e.mu.Unlock()
}

func (p *roomParticipantEvidence) recordError(artifact string, err error) error {
	if err != nil && p != nil && p.owner != nil {
		p.owner.recordError(p.id, artifact, err)
	}
	return err
}

func (e *roomEvidence) recordingHealth() (*transcript.RecordingStatus, map[string]string, map[string]*transcript.RecordingStatus, map[string]map[string]string) {
	if e == nil || e.service == nil {
		return nil, nil, nil, nil
	}
	health := e.service.Health()
	return health.Status, health.DegradedArtifacts, health.ParticipantStatuses, health.ParticipantArtifacts
}

func cloneRoomRecordingStatus(status *transcript.RecordingStatus) *transcript.RecordingStatus {
	if status == nil {
		return nil
	}
	clone := *status
	return &clone
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

func (e *roomEvidence) applyRecordingHealth(result *RoomResult) {
	if e == nil || result == nil {
		return
	}
	status, degraded, participantStatuses, _ := e.recordingHealth()
	result.RecordingStatus = cloneRoomRecordingStatus(status)
	result.DegradedArtifacts = cloneRoomStringMap(degraded)
	for id, participant := range result.Participants {
		participant.RecordingStatus = cloneRoomRecordingStatus(participantStatuses[id])
		result.Participants[id] = participant
	}
}

func (e *roomEvidence) err() error {
	if e == nil {
		return nil
	}
	if e.service != nil {
		return e.service.Error()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.recordErr
}

func (e *roomEvidence) observeSpeakerAudio(sourceID string, targetIDs []string, pcm []byte) {
	if e != nil && e.service != nil {
		e.service.ObserveSpeakerAudio(sourceID, targetIDs, pcm)
	}
}
func (e *roomEvidence) observeSpeechStopped(id string) {
	if e != nil && e.service != nil {
		e.service.ObserveSpeechStopped(id)
	}
}
func (e *roomEvidence) observeProviderAudio(id, responseID string) {
	if e != nil && e.service != nil {
		e.service.ObserveProviderAudio(id, responseID)
	}
}
func (e *roomEvidence) observePeerAudio(sourceID, targetID string, pcm []byte) {
	if e != nil && e.service != nil {
		e.service.ObservePeerAudio(sourceID, targetID, pcm)
	}
}

func (e *roomEvidence) finalize(result RoomResult, runErr error, endedAt time.Time) error {
	if e == nil {
		return nil
	}
	e.finalizeOnce.Do(func() {
		for _, participant := range e.participants {
			if participant != nil && participant.deltas != nil && participant.deltas.closed {
				e.recordError(participant.id, participant.artifacts.Deltas, errors.New("compatibility delta sink was closed"))
			}
		}
		if e.service != nil {
			e.finalizeErr = e.service.Finalize(runtimeRoomResult(result), runErr, endedAt)
		} else {
			e.finalizeErr = e.err()
		}
		e.cleanupCompatibilityHandles()
	})
	return e.finalizeErr
}

func (e *roomEvidence) cleanupCompatibilityHandles() {
	for _, participant := range e.participants {
		if participant == nil {
			continue
		}
		if participant.deltas != nil {
			if err := participant.deltas.close(); err != nil {
				e.recordError(participant.id, participant.artifacts.Deltas, err)
			}
		}
		for _, path := range participant.compatPaths {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				e.recordError(participant.id, participant.artifacts.Deltas, err)
			}
		}
	}
}

func runtimeRoomResult(result RoomResult) runtimeRooms.RoomResult {
	runtimeResult := runtimeRooms.RoomResult{TerminationReason: runtimeRooms.RoomTerminationReason(result.TerminationReason), Reason: runtimeRooms.RoomTerminationReason(result.Reason), ActiveParticipants: append([]string(nil), result.ActiveParticipants...), Error: result.Error, RecordingStatus: result.RecordingStatus, DegradedArtifacts: cloneRoomStringMap(result.DegradedArtifacts), Participants: make(map[string]runtimeRooms.RoomParticipantResult, len(result.Participants))}
	for id, value := range result.Participants {
		runtimeResult.Participants[id] = runtimeRooms.RoomParticipantResult{ID: value.ID, ParticipantID: value.ParticipantID, TerminationReason: runtimeRooms.ParticipantTerminationReason(value.TerminationReason), Reason: runtimeRooms.ParticipantTerminationReason(value.Reason), TerminationTrigger: value.TerminationTrigger, TerminationDisposition: value.TerminationDisposition, Classification: value.Classification, TerminalReason: value.TerminalReason, TerminalProvenance: value.TerminalProvenance, OutputState: value.OutputState, TurnsCompleted: value.TurnsCompleted, Connected: value.Connected, Error: value.Error, RecordingStatus: value.RecordingStatus}
	}
	return runtimeResult
}

func prepareRoomEvidenceOutput(path string) (string, error) {
	return roomevidencewire.NewService().PrepareOutput(path)
}
func ValidateRoomEvidenceOutput(path string) error {
	return roomevidencewire.NewService().ValidateOutput(path)
}

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
	if format.Channels <= 0 {
		format.Channels = 1
	}
	if format.FrameDuration <= 0 {
		format.FrameDuration = room.DefaultPCM16Format().FrameDuration
	}
	return format
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

// The following schema types remain local decode fixtures for existing CLI
// tests and replay readers. They are not used as the recorder implementation.
type roomEvidenceArtifactIntegrity struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
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
	AudioFormat       roomEvidenceAudioFormat                    `json:"audio_format"`
	RoomMix           string                                     `json:"room_mix"`
	RoomTimeline      string                                     `json:"room_timeline"`
	RoomLatency       string                                     `json:"room_latency,omitempty"`
	Artifacts         map[string]string                          `json:"artifacts"`
	ArtifactIntegrity map[string]roomEvidenceArtifactIntegrity   `json:"artifact_integrity,omitempty"`
	RecordingStatus   *transcript.RecordingStatus                `json:"recording_status,omitempty"`
	DegradedArtifacts map[string]string                          `json:"degraded_artifacts,omitempty"`
	Error             string                                     `json:"error,omitempty"`
}
type roomEvidenceAudioFormat struct {
	SampleRate      int    `json:"sample_rate"`
	Channels        int    `json:"channels"`
	Encoding        string `json:"encoding"`
	SampleWidthBits int    `json:"sample_width_bits"`
	ByteOrder       string `json:"byte_order"`
}
type roomEvidenceTiming struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Elapsed   string `json:"elapsed"`
	ClockBase string `json:"clock_base"`
}
type roomEvidenceBounds struct {
	MaxTurns    int    `json:"max_turns,omitempty"`
	MaxDuration string `json:"max_duration,omitempty"`
}
type roomEvidenceParticipantManifest struct {
	ID                     string                       `json:"id"`
	Kind                   room.ParticipantKind         `json:"kind"`
	SystemPrompt           string                       `json:"system_prompt"`
	OpeningPrompt          string                       `json:"opening_prompt,omitempty"`
	Provider               string                       `json:"provider"`
	Model                  string                       `json:"model"`
	APIKeyEnv              string                       `json:"api_key_env"`
	Voice                  string                       `json:"voice,omitempty"`
	Tools                  []string                     `json:"tools"`
	BrowserTools           *room.BrowserToolsConfig     `json:"browser_tools,omitempty"`
	CompletedTurns         int                          `json:"completed_turns"`
	TerminationReason      ParticipantTerminationReason `json:"termination_reason"`
	Reason                 ParticipantTerminationReason `json:"reason,omitempty"`
	TerminationTrigger     string                       `json:"termination_trigger"`
	TerminationDisposition string                       `json:"termination_disposition"`
	Classification         string                       `json:"classification"`
	TerminalReason         string                       `json:"terminal_reason"`
	TerminalProvenance     string                       `json:"terminal_provenance"`
	OutputState            string                       `json:"output_state"`
	Connected              bool                         `json:"connected"`
	InputDevice            string                       `json:"input_device,omitempty"`
	OutputDevice           string                       `json:"output_device,omitempty"`
	Error                  string                       `json:"error,omitempty"`
	Artifacts              roomEvidenceArtifactPaths    `json:"artifacts"`
	RecordingStatus        *transcript.RecordingStatus  `json:"recording_status,omitempty"`
	DegradedArtifacts      map[string]string            `json:"degraded_artifacts,omitempty"`
}
