package evidence

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomlatency "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/evidence/latency"
	roommanifest "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/manifest"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

const (
	recorderSchemaVersion = rooms.RoomReplayBundleSchemaVersion
	recorderEncoding      = "pcm_s16le"
	recorderWidthBits     = 16
	recorderByteOrder     = "little"
	recorderQueueCapacity = 256
	maxFrameDuration      = 2 * time.Second
)

// Recorder writes one finalized room bundle. It is intentionally an
// observation owner: recording failures are latched as degraded artifacts and
// do not turn a healthy provider run into a provider failure.
type Recorder struct {
	destination  string
	manifest     rooms.Manifest
	format       rooms.AudioFormat
	startedAt    time.Time
	clock        platformclock.Source
	timeline     *jsonlWriter
	mix          *mixAccumulator
	latency      *roomlatency.Recorder
	participants map[string]*participantRecorder
	jobs         chan func()
	workerDone   chan struct{}

	mu              sync.Mutex
	closed          bool
	startWorker     sync.Once
	closeWorker     sync.Once
	recordErr       error
	artifactErr     map[string]error
	maxFrameSamples int
	finalizeOnce    sync.Once
	finalizeErr     error
	status          *transcript.RecordingStatus
	degraded        map[string]string
}

type participantRecorder struct {
	id          string
	manifest    rooms.Participant
	artifacts   artifactPaths
	wav         *wavWriter
	diagnostics *jsonlWriter
	deltas      *jsonlWriter
	events      *jsonlWriter
	sent        *pcmWriter
	received    *pcmWriter
}

type artifactPaths struct {
	WAV         string `json:"wav"`
	Diagnostics string `json:"diagnostics"`
	Deltas      string `json:"deltas"`
	SentPCM     string `json:"sent_pcm"`
	ReceivedPCM string `json:"received_pcm"`
	Events      string `json:"events"`
	Capture     string `json:"capture,omitempty"`
}

// NewRecorder creates every participant sink before live admission. A later
// provider or device failure therefore leaves a complete artifact inventory.
func NewRecorder(destination string, manifest rooms.Manifest, format rooms.AudioFormat, startedAt time.Time, source platformclock.Source) (*Recorder, error) {
	destination, format, err := normalizeRecorderInput(destination, format)
	if err != nil {
		return nil, err
	}
	clock := platformclock.Ensure(source)
	if startedAt.IsZero() {
		startedAt = clock.Now()
	}
	recorder := newRecorderState(destination, manifest, format, startedAt, clock)
	if err := recorder.openResources(); err != nil {
		return nil, errors.Join(err, recorder.cleanup())
	}
	recorder.start()
	return recorder, nil
}

func normalizeRecorderInput(destination string, format rooms.AudioFormat) (string, rooms.AudioFormat, error) {
	destination = filepath.Clean(strings.TrimSpace(destination))
	if destination == "." || destination == "" {
		return "", rooms.AudioFormat{}, errors.New("room evidence output directory is empty")
	}
	if format.SampleRate <= 0 {
		format = mixer.DefaultFormat()
	}
	if format.Channels != 1 {
		return "", rooms.AudioFormat{}, fmt.Errorf("room evidence requires mono PCM16, got %d channels", format.Channels)
	}
	if err := os.MkdirAll(destination, evidenceDirectoryMode); err != nil {
		return "", rooms.AudioFormat{}, fmt.Errorf("create room evidence output directory: %w", err)
	}
	return destination, format, nil
}

func newRecorderState(destination string, manifest rooms.Manifest, format rooms.AudioFormat, startedAt time.Time, clock platformclock.Source) *Recorder {
	return &Recorder{
		destination:     destination,
		manifest:        manifest,
		format:          format,
		startedAt:       startedAt,
		clock:           clock,
		latency:         roomlatency.New(clock, format),
		participants:    make(map[string]*participantRecorder, len(manifest.Participants)),
		artifactErr:     make(map[string]error),
		maxFrameSamples: format.SampleRate * int(maxFrameDuration/time.Second),
		jobs:            make(chan func(), recorderQueueCapacity),
		workerDone:      make(chan struct{}),
	}
}

func (r *Recorder) openResources() error {
	var err error
	r.timeline, err = newJSONLWriter(filepath.Join(r.destination, rooms.RoomEvidenceTimelinePath))
	if err != nil {
		return fmt.Errorf("create room timeline evidence: %w", err)
	}
	r.mix, err = newMixAccumulator(r.format, r.manifest.Room.MaxDuration)
	if err != nil {
		return err
	}
	r.recordTimeline("room_started", "", nil)
	usedStems := make(map[string]struct{}, len(r.manifest.Participants))
	for _, participant := range r.manifest.Participants {
		stem := uniqueStem(participant.ID, usedStems)
		if err := r.openParticipant(participant, stem); err != nil {
			return err
		}
	}
	return nil
}

func (r *Recorder) openParticipant(participant rooms.Participant, stem string) error {
	paths := participantArtifacts(stem, participant.Kind)
	entry := &participantRecorder{id: participant.ID, manifest: participant, artifacts: paths}
	r.participants[participant.ID] = entry
	base := filepath.Join(r.destination, "participants", stem)
	if err := os.MkdirAll(base, evidenceDirectoryMode); err != nil {
		return fmt.Errorf("create participant %q evidence directory: %w", participant.ID, err)
	}
	var err error
	if entry.wav, err = newWAVWriter(filepath.Join(r.destination, paths.WAV), r.format.SampleRate); err != nil {
		return fmt.Errorf("create participant %q WAV evidence: %w", participant.ID, err)
	}
	if entry.diagnostics, err = newJSONLWriter(filepath.Join(r.destination, paths.Diagnostics)); err != nil {
		return fmt.Errorf("create participant %q diagnostics evidence: %w", participant.ID, err)
	}
	if entry.deltas, err = newJSONLWriter(filepath.Join(r.destination, paths.Deltas)); err != nil {
		return fmt.Errorf("create participant %q delta evidence: %w", participant.ID, err)
	}
	if entry.events, err = newJSONLWriter(filepath.Join(r.destination, paths.Events)); err != nil {
		return fmt.Errorf("create participant %q event evidence: %w", participant.ID, err)
	}
	if entry.sent, err = newPCMWriter(filepath.Join(r.destination, paths.SentPCM)); err != nil {
		return fmt.Errorf("create participant %q sent PCM evidence: %w", participant.ID, err)
	}
	if entry.received, err = newPCMWriter(filepath.Join(r.destination, paths.ReceivedPCM)); err != nil {
		return fmt.Errorf("create participant %q received PCM evidence: %w", participant.ID, err)
	}
	r.enqueueRecordingStart(entry)
	return nil
}

func participantArtifacts(stem string, kind rooms.ParticipantKind) artifactPaths {
	paths := artifactPaths{
		WAV:         filepath.ToSlash(filepath.Join("agent-" + stem + ".wav")),
		Diagnostics: filepath.ToSlash(filepath.Join("agent-" + stem + ".diagnostics.jsonl")),
		Deltas:      filepath.ToSlash(filepath.Join("agent-" + stem + ".deltas.jsonl")),
		SentPCM:     filepath.ToSlash(filepath.Join("participants", stem, "sent.pcm")),
		ReceivedPCM: filepath.ToSlash(filepath.Join("participants", stem, "received.pcm")),
		Events:      filepath.ToSlash(filepath.Join("participants", stem, "events.jsonl")),
	}
	if roommanifest.NormalizeParticipantKind(kind) == rooms.ParticipantKindAgent {
		paths.Capture = filepath.ToSlash(filepath.Join("participants", stem, "capture.json"))
	}
	return paths
}

func (r *Recorder) enqueueRecordingStart(entry *participantRecorder) {
	at := r.clock.Now()
	if err := r.enqueue(entry.artifacts.Events, func() {
		if err := entry.events.write(eventRecord{Kind: "recording_started", ParticipantID: entry.id, Timestamp: at.UTC()}); err != nil {
			r.recordError(entry.id, entry.artifacts.Events, err)
		}
	}); err != nil {
		return
	}
}

func (r *Recorder) start() {
	if r == nil {
		return
	}
	r.startWorker.Do(func() { go r.runWorker() })
}

func (r *Recorder) runWorker() {
	if r == nil {
		return
	}
	defer close(r.workerDone)
	for job := range r.jobs {
		if job != nil {
			job()
		}
	}
}

// enqueue is deliberately non-blocking. A full evidence queue marks the
// affected bundle partial and lets the media owner keep its negotiated cadence.
func (r *Recorder) enqueue(artifact string, job func()) error {
	if r == nil || job == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		err := errors.New("room evidence recorder is closed")
		r.recordError("", artifact, err)
		return err
	}
	select {
	case r.jobs <- job:
		r.mu.Unlock()
		return nil
	default:
		r.mu.Unlock()
		err := errors.New("room evidence queue is full")
		r.recordError("", artifact, err)
		return err
	}
}

func (r *Recorder) stopWorker() {
	if r == nil {
		return
	}
	r.closeWorker.Do(func() {
		r.start()
		r.mu.Lock()
		r.closed = true
		close(r.jobs)
		r.mu.Unlock()
		<-r.workerDone
	})
}

// CapturePath returns the recorder-owned provider capture destination. The
// session service may write a raw provider capture there during Start/Close.
func (r *Recorder) CapturePath(participantID string) string {
	if r == nil {
		return ""
	}
	participant := r.participant(participantID)
	if participant == nil || participant.artifacts.Capture == "" {
		return ""
	}
	return filepath.Join(r.destination, filepath.FromSlash(participant.artifacts.Capture))
}

func uniqueStem(id string, used map[string]struct{}) string {
	var builder strings.Builder
	for _, runeValue := range strings.ToLower(strings.TrimSpace(id)) {
		if unicode.IsLetter(runeValue) || unicode.IsDigit(runeValue) || runeValue == '_' || runeValue == '-' {
			builder.WriteRune(runeValue)
		} else {
			builder.WriteByte('_')
		}
	}
	stem := strings.Trim(builder.String(), "._-")
	if stem == "" {
		stem = "participant"
	}
	base := stem
	for suffix := 2; ; suffix++ {
		if _, exists := used[stem]; !exists {
			used[stem] = struct{}{}
			return stem
		}
		stem = fmt.Sprintf("%s-%d", base, suffix)
	}
}

func cloneStrings(fields map[string]string) map[string]string {
	if fields == nil {
		return nil
	}
	result := make(map[string]string, len(fields))
	for key, value := range fields {
		result[key] = value
	}
	return result
}

func cloneStatus(status *transcript.RecordingStatus) *transcript.RecordingStatus {
	if status == nil {
		return nil
	}
	copy := *status
	return &copy
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func normalizeKind(kind string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(kind), ".", "_"), "-", "_"))
}

func isTranscriptDelta(kind string) bool {
	value := normalizeKind(kind)
	return strings.Contains(value, "transcript") && strings.Contains(value, "delta")
}

func isTranscriptEnd(kind string) bool {
	value := normalizeKind(kind)
	return strings.Contains(value, "transcript") && (strings.Contains(value, "done") || strings.Contains(value, "end"))
}

var _ rooms.EventSink = (*Recorder)(nil)
