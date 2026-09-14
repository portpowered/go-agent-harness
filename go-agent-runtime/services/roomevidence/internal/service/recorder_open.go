package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

func newRecorder(options roomevidence.Options) (roomevidence.Recorder, error) {
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
		providerErrs:         make(map[string]struct{}, len(options.Manifest.Participants)),
		participantRecordErr: make(map[string]error, len(options.Manifest.Participants)),
		artifactRecordErr:    make(map[string]error),
		finalizeDone:         make(chan struct{}),
	}
	if options.LatencyRecorder != nil {
		r.latency = options.LatencyRecorder
	} else if options.Latency != nil {
		r.latency = options.Latency.NewRecorder(source, format)
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
	if err := os.MkdirAll(destination, evidenceDirectoryMode); err != nil {
		return fmt.Errorf("create room evidence output directory %q: %w", destination, err)
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
