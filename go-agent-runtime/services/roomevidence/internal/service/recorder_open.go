package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/internal/latency"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/internal/pathguard"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func normalizedFormat(format rooms.AudioFormat) rooms.AudioFormat {
	if format.SampleRate <= 0 {
		format.SampleRate = defaultSampleRate
	}
	if format.Channels <= 0 {
		format.Channels = defaultChannels
	}
	if format.FrameDuration <= 0 {
		format.FrameDuration = defaultFrameDuration
	}
	return format
}

func newRecorder(options roomevidence.RecordingRequest) (roomevidence.Recorder, error) {
	destination := filepath.Clean(strings.TrimSpace(options.Destination))
	if destination == "." || destination == "" {
		return nil, fmt.Errorf("%w: directory is required", roomevidence.ErrInvalidOutput)
	}
	if err := validateOutputTarget(destination); err != nil {
		return nil, err
	}
	if err := mkdirOutput(destination); err != nil {
		return nil, err
	}
	format := normalizedFormat(options.AudioFormat)
	if format.Channels != 1 {
		return nil, fmt.Errorf("%w: room evidence requires mono PCM16, got %d channels", roomevidence.ErrInvalidOutput, format.Channels)
	}
	formatSpec := mixer.Format(format)
	if _, err := formatSpec.FrameSamples(); err != nil {
		return nil, fmt.Errorf("%w: %w", roomevidence.ErrInvalidOutput, err)
	}
	source := platformclock.Ensure(options.Clock)
	startedAt := options.StartedAt
	if startedAt.IsZero() {
		startedAt = source.Now()
	}
	startedAt = startedAt.UTC()
	mix, err := mixer.NewPCMAccumulator(mixer.Format(format), options.Manifest.Room.MaxDuration)
	if err != nil {
		return nil, fmt.Errorf("create room evidence mix: %w", err)
	}
	r := &recorder{
		destination:          destination,
		manifest:             cloneManifest(options.Manifest),
		format:               format,
		startedAt:            startedAt,
		secrets:              cloneStrings(options.Secrets),
		clock:                newClockState(startedAt, source),
		mix:                  mix,
		participants:         make(map[string]*participantRecorder, len(options.Manifest.Participants)),
		captureSeen:          make(map[string]bool, len(options.Manifest.Participants)),
		providerErrs:         make(map[string]struct{}, len(options.Manifest.Participants)),
		participantRecordErr: make(map[string]error, len(options.Manifest.Participants)),
		artifactRecordErr:    make(map[string]error),
		finalizeDone:         make(chan struct{}),
	}
	if options.LatencyRecorder != nil {
		r.latency = options.LatencyRecorder
	} else if options.Latency != nil {
		r.latency = options.Latency.NewRecorder(source, format)
	} else {
		r.latency = latency.NewService().NewRecorder(source, format)
	}
	r.timeline, err = newJSONLWriter(filepath.Join(destination, roomevidence.TimelinePath))
	if err != nil {
		return nil, fmt.Errorf("create room timeline evidence: %w", err)
	}
	if err := r.openParticipants(); err != nil {
		r.cleanupOpenFiles()
		return nil, err
	}
	return r, nil
}

func cloneManifest(value rooms.Manifest) rooms.Manifest {
	clone := value
	clone.Participants = append([]rooms.Participant(nil), value.Participants...)
	if value.Room.Recording != nil {
		recording := *value.Room.Recording
		clone.Room.Recording = &recording
	}
	for index, participant := range clone.Participants {
		participant.Tools = cloneStrings(participant.Tools)
		if participant.BrowserTools != nil {
			browser := *participant.BrowserTools
			browser.Policy.AllowedOrigins = cloneStrings(browser.Policy.AllowedOrigins)
			browser.Policy.DeniedOrigins = cloneStrings(browser.Policy.DeniedOrigins)
			participant.BrowserTools = &browser
		}
		clone.Participants[index] = participant
	}
	return clone
}

func mkdirOutput(destination string) error {
	if err := pathguard.ValidateNoSymlinkPath(destination); err != nil {
		return fmt.Errorf("%w: output path is unsafe: %w", roomevidence.ErrInvalidOutput, err)
	}
	if err := os.MkdirAll(destination, evidenceDirectoryMode); err != nil {
		return fmt.Errorf("create room evidence output directory %q: %w", destination, err)
	}
	if err := pathguard.ValidateNoSymlinkPath(destination); err != nil {
		return fmt.Errorf("%w: output path is unsafe: %w", roomevidence.ErrInvalidOutput, err)
	}
	return nil
}

func (r *recorder) openParticipants() error {
	stems := participantStems(r.manifest.Participants)
	for _, manifest := range r.manifest.Participants {
		if strings.TrimSpace(manifest.ID) == "" {
			return fmt.Errorf("room evidence participant ID is empty")
		}
		if _, exists := r.participants[manifest.ID]; exists {
			return fmt.Errorf("room evidence participant %q is declared more than once", manifest.ID)
		}
		if err := r.openParticipant(manifest, stems[manifest.ID]); err != nil {
			return err
		}
	}
	return nil
}

func (r *recorder) openParticipant(manifest rooms.Participant, stem string) error {
	paths := participantArtifacts(manifest, stem)
	participant := &participantRecorder{
		owner: r, id: manifest.ID, manifest: manifest, artifacts: paths,
		sentSpeech: &speechTracker{}, receivedSpeech: &speechTracker{},
	}
	r.participants[manifest.ID] = participant
	directory := filepath.Join(r.destination, "participants", stem)
	if err := mkdirOutputDirectory(directory); err != nil {
		return fmt.Errorf("create room participant %q evidence directory: %w", manifest.ID, err)
	}
	var err error
	if participant.wav, err = newWAVWriter(filepath.Join(r.destination, paths.WAV), r.format.SampleRate); err != nil {
		return fmt.Errorf("create room participant %q WAV evidence: %w", manifest.ID, err)
	}
	if participant.diagnostics, err = newJSONLWriter(filepath.Join(r.destination, paths.Diagnostics)); err != nil {
		return fmt.Errorf("create room participant %q diagnostics evidence: %w", manifest.ID, err)
	}
	if participant.deltas, err = newJSONLWriter(filepath.Join(r.destination, paths.Deltas)); err != nil {
		return fmt.Errorf("create room participant %q delta evidence: %w", manifest.ID, err)
	}
	if participant.events, err = newJSONLWriter(filepath.Join(r.destination, paths.Events)); err != nil {
		return fmt.Errorf("create room participant %q event evidence: %w", manifest.ID, err)
	}
	if participant.sentPCM, err = newPCMWriter(filepath.Join(r.destination, paths.SentPCM)); err != nil {
		return fmt.Errorf("create room participant %q sent-audio evidence: %w", manifest.ID, err)
	}
	if participant.receivedPCM, err = newPCMWriter(filepath.Join(r.destination, paths.ReceivedPCM)); err != nil {
		return fmt.Errorf("create room participant %q received-audio evidence: %w", manifest.ID, err)
	}
	return nil
}

func participantArtifacts(manifest rooms.Participant, stem string) roomevidence.ArtifactPaths {
	paths := roomevidence.ArtifactPaths{
		WAV:         filepath.ToSlash("agent-" + stem + ".wav"),
		Diagnostics: filepath.ToSlash("agent-" + stem + ".diagnostics.jsonl"),
		Deltas:      filepath.ToSlash("agent-" + stem + ".deltas.jsonl"),
		SentPCM:     filepath.ToSlash(filepath.Join("participants", stem, "sent.pcm")),
		ReceivedPCM: filepath.ToSlash(filepath.Join("participants", stem, "received.pcm")),
		Events:      filepath.ToSlash(filepath.Join("participants", stem, "events.jsonl")),
	}
	if normalizeParticipantKind(manifest.Kind) != rooms.ParticipantKindHuman {
		paths.Capture = filepath.ToSlash(filepath.Join("participants", stem, "capture.json"))
	}
	return paths
}

func participantStems(participants []rooms.Participant) map[string]string {
	counts := make(map[string]int, len(participants))
	base := make(map[string]string, len(participants))
	for _, participant := range participants {
		stem := normalizeStem(participant.ID)
		base[participant.ID] = stem
		counts[stem]++
	}
	result := make(map[string]string, len(participants))
	for _, participant := range participants {
		stem := base[participant.ID]
		if counts[stem] > 1 {
			stem += "-" + shortHash(participant.ID)
		}
		result[participant.ID] = stem
	}
	return result
}

func normalizeStem(id string) string {
	var builder strings.Builder
	unsafe := false
	for _, value := range strings.TrimSpace(id) {
		safe := unicode.IsLetter(value) || unicode.IsDigit(value) || value == '-' || value == '_' || value == '.'
		if safe {
			builder.WriteRune(value)
			unsafe = false
		} else if !unsafe {
			builder.WriteByte('_')
			unsafe = true
		}
	}
	stem := strings.Trim(builder.String(), ".")
	if stem == "" || stem == ".." {
		return "participant"
	}
	return stem
}

func shortHash(value string) string {
	// A four-byte digest is enough to disambiguate filesystem stems while the
	// manifest remains authoritative for the complete participant ID.
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}

func normalizeParticipantKind(kind rooms.ParticipantKind) rooms.ParticipantKind {
	switch value := rooms.ParticipantKind(strings.ToLower(strings.TrimSpace(string(kind)))); value {
	case "", rooms.ParticipantKindAgent:
		return rooms.ParticipantKindAgent
	case rooms.ParticipantKindHuman, rooms.ParticipantKindCustomer:
		return rooms.ParticipantKindHuman
	default:
		return value
	}
}

func (r *recorder) RecordLiveEvent(participantID string, event session.LiveEvent) error {
	if r == nil {
		return roomevidence.ErrRecorderClosed
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	if err := r.checkOpen(); err != nil {
		return err
	}
	participantID = liveEventParticipantID(participantID, event)
	fields := liveEventFields(event)
	at := event.Timestamp.UTC()
	if at.IsZero() {
		at = r.clock.source.Now().UTC()
	}
	if err := r.writeTimelineAt(at, "live_"+normalizeLiveEventKind(event.Kind), participantID, fields); err != nil {
		return err
	}
	r.markCaptureSeen(participantID)
	if participant := r.participants[participantID]; participant != nil {
		if err := participant.recordDiagnostic(roomevidence.DiagnosticRecord{Event: event.Kind, Fields: fields, At: at}); err != nil {
			return err
		}
	}
	r.observeLatencyEvent(participantID, event)
	return nil
}

func liveEventParticipantID(participantID string, event session.LiveEvent) string {
	participantID = strings.TrimSpace(participantID)
	if participantID == "" {
		return strings.TrimSpace(event.ParticipantID)
	}
	return participantID
}

func (r *recorder) markCaptureSeen(participantID string) {
	if r == nil || participantID == "" {
		return
	}
	r.mu.Lock()
	r.captureSeen[participantID] = true
	r.mu.Unlock()
}

func liveEventFields(event session.LiveEvent) map[string]string {
	fields := map[string]string{}
	if event.Sequence != 0 {
		fields["sequence"] = strconv.FormatUint(event.Sequence, 10)
	}
	if event.SessionID != "" {
		fields["session_id"] = event.SessionID
	}
	if event.ResponseID != "" {
		fields["response_id"] = event.ResponseID
	}
	if event.ItemID != "" {
		fields["item_id"] = event.ItemID
	}
	if event.ToolCallID != "" {
		fields["tool_call_id"] = event.ToolCallID
	}
	if event.Role != "" {
		fields["role"] = string(event.Role)
	}
	if event.Text != "" {
		fields["text"] = event.Text
	}
	if event.Reason != "" {
		fields["reason"] = event.Reason
	}
	if event.State != "" {
		fields["state"] = event.State
	}
	if event.Dropped != 0 {
		fields["dropped"] = strconv.FormatUint(event.Dropped, 10)
	}
	if event.Error != nil {
		fields["error"] = fmt.Sprint(event.Error)
	}
	if event.Message != nil {
		fields["message_type"] = string(event.Message.Type)
	}
	if event.Liveness != nil {
		fields["classification"] = event.Liveness.Classification
		fields["terminal_reason"] = string(event.Liveness.TerminalReason)
		fields["terminal_provenance"] = string(event.Liveness.TerminalProvenance)
		fields["output_state"] = string(event.Liveness.OutputState)
	}
	return fields
}

func normalizeLiveEventKind(kind string) string {
	var builder strings.Builder
	unsafe := false
	for _, value := range strings.ToLower(strings.TrimSpace(kind)) {
		if (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9') || value == '_' {
			builder.WriteRune(value)
			unsafe = false
		} else if !unsafe {
			builder.WriteByte('_')
			unsafe = true
		}
	}
	return strings.Trim(builder.String(), "_")
}

func (r *recorder) observeLatencyEvent(participantID string, event session.LiveEvent) {
	if r == nil || r.latency == nil || strings.TrimSpace(participantID) == "" {
		return
	}
	kind := strings.ToLower(strings.TrimSpace(event.Kind))
	if event.Message != nil {
		kind = strings.ToLower(strings.TrimSpace(string(event.Message.Type)))
	}
	switch kind {
	case "speech_stopped", "vad_speech_stopped":
		r.latency.ObserveSpeechStopped(participantID)
	case "input_commit", "input_item_added", "input_audio_buffer_committed":
		r.latency.ObserveRuntime(participantID, rooms.LatencyObservation{Kind: rooms.LatencyObservationInputCommit, Timestamp: event.Timestamp})
	case "response_create", "response_created":
		r.latency.ObserveRuntime(participantID, rooms.LatencyObservation{Kind: rooms.LatencyObservationResponseCreate, ResponseID: event.ResponseID, Timestamp: event.Timestamp})
	case "audio_delta":
		if event.ResponseID == "" {
			return
		}
		if withTimestamp, ok := r.latency.(interface {
			ObserveProviderAudioAt(string, string, time.Time, uint64)
		}); ok {
			withTimestamp.ObserveProviderAudioAt(participantID, event.ResponseID, event.Timestamp, 0)
			return
		}
		r.latency.ObserveProviderAudio(participantID, event.ResponseID)
	}
}
