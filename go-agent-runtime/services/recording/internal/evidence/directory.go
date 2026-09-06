package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"io"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	directoryEvidenceQueueCapacity = 256
	directoryEvidenceQueueMaxBytes = 16 << 20
)

type directoryRecorder struct {
	options     recording.LiveEvidenceOptions
	destination string
	lockPath    string
	lock        *os.File
	spool       string

	queue chan directoryEvidenceItem
	done  chan struct{}

	mu           sync.Mutex
	queuedBytes  int64
	closed       bool
	recordErr    error
	workerErr    error
	sequence     uint64
	client       *os.File
	inputFile    *os.File
	outputFile   *os.File
	inputBytes   uint64
	outputBytes  uint64
	writeSpool   func(*os.File, []byte) error
	agent        *os.File
	clientPath   string
	agentPath    string
	inputPaths   []string
	outputPaths  []string
	terminal     *transcript.RecordingTerminalSummary
	conversation evidenceConversation

	finalizeOnce sync.Once
	finalizeErr  error
}

type directoryEvidenceItem struct {
	kind      evidenceItemKind
	direction session.LiveRecordDirection
	admission session.LiveAudioAdmission
	timestamp time.Time
	payload   []byte
	frame     sharedaudio.PCMFrame
	bytes     int64
}

type evidenceItemKind uint8

const (
	evidenceMessage evidenceItemKind = iota + 1
	evidenceAudio
	evidenceEvent
)

// New opens one invocation-owned evidence spool. The returned recorder owns
// its drain worker and must be finalized, including after partial startup.
func New(options recording.LiveEvidenceOptions, source clock.Source) (session.LiveRecorder, error) {
	return newDirectoryRecorder(options, source)
}

func newDirectoryRecorder(options recording.LiveEvidenceOptions, source clock.Source) (*directoryRecorder, error) {
	if source == nil {
		return nil, errors.New("recording clock is required")
	}
	observed := source.Now()
	if options.ClockBase.IsZero() {
		options.ClockBase = observed
	}
	if options.WallClockStart.IsZero() {
		options.WallClockStart = observed
	}
	destination, lock, err := claimEvidenceDestination(options.Destination, observed)
	if err != nil {
		return nil, err
	}
	spool, err := os.MkdirTemp("", ".go-agent-runtime-recording-")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create recording spool: %w", err), releaseEvidenceClaim(lock, destination+".lock"))
	}

	recorder := &directoryRecorder{
		options:     cloneEvidenceOptions(options),
		writeSpool:  writeAll,
		destination: destination,
		lockPath:    destination + ".lock",
		lock:        lock,
		spool:       spool,
		queue:       make(chan directoryEvidenceItem, directoryEvidenceQueueCapacity),
		done:        make(chan struct{}),
		conversation: evidenceConversation{
			turn: evidenceTurn{toolEvents: make([]evidenceToolEvent, 0)},
		},
	}
	go recorder.run()
	return recorder, nil
}

func cloneEvidenceOptions(options recording.LiveEvidenceOptions) recording.LiveEvidenceOptions {
	options.Credentials = append([]string(nil), options.Credentials...)
	return options
}

func (r *directoryRecorder) RecordMessage(ctx context.Context, record session.LiveRecord) error {
	if r == nil {
		return recording.ErrLiveEvidenceClosed
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if record.Timestamp.IsZero() {
		r.latch(recordingWriteError("observe message clock", errors.New("message timestamp is unavailable")))
	}
	payload, err := gatewaytesting.MarshalStreamMessage(record.Message)
	if err != nil {
		r.latch(recordingWriteError("encode stream message", err))
		return nil
	}
	item := directoryEvidenceItem{
		kind: evidenceMessage, direction: record.Direction, timestamp: record.Timestamp,
		payload: payload, bytes: int64(len(payload)) * 2,
	}
	return r.enqueue(item)
}

func (r *directoryRecorder) RecordAudio(ctx context.Context, record session.LiveAudioRecord) error {
	if r == nil {
		return recording.ErrLiveEvidenceClosed
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if record.Timestamp.IsZero() {
		r.latch(recordingWriteError("observe audio clock", errors.New("audio timestamp is unavailable")))
	}
	if err := record.Frame.Format.Validate(); err != nil {
		r.latch(recordingWriteError("observe audio format", err))
	}
	if len(record.Frame.Samples) == 0 && !record.Frame.EndOfResponse {
		return nil
	}
	if len(record.Frame.Samples) > directoryEvidenceQueueMaxBytes/2 {
		r.latch(recordingWriteError("admit audio evidence", io.ErrShortBuffer))
		return nil
	}
	frame := record.Frame
	item := directoryEvidenceItem{
		kind: evidenceAudio, direction: record.Direction, admission: record.Admission, timestamp: record.Timestamp,
		frame: frame, bytes: int64(len(frame.Samples)) * 2,
	}
	return r.enqueue(item)
}

func (r *directoryRecorder) RecordEvent(ctx context.Context, event session.LiveEvent) error {
	if r == nil {
		return recording.ErrLiveEvidenceClosed
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if event.Timestamp.IsZero() {
		r.latch(recordingWriteError("observe event clock", errors.New("event timestamp is unavailable")))
	}
	if event.Dropped != 0 {
		r.latch(recordingWriteError("observe runtime events", fmt.Errorf("runtime dropped %d observations", event.Dropped)))
	}
	if event.Terminal != nil {
		r.retainTerminal(terminalSummary(event.Terminal))
	}
	errorText := ""
	if event.Error != nil {
		errorText = event.Error.Error()
	}
	event.Error = nil
	payload, err := json.Marshal(struct {
		Event session.LiveEvent `json:"event"`
		Error string            `json:"error,omitempty"`
	}{Event: event, Error: errorText})
	if err != nil {
		r.latch(recordingWriteError("encode runtime event", err))
		return nil
	}
	return r.enqueue(directoryEvidenceItem{kind: evidenceEvent, direction: session.LiveRecordAgent, timestamp: event.Timestamp, payload: payload, bytes: int64(len(payload)) * 2})
}

func (r *directoryRecorder) retainTerminal(summary *transcript.RecordingTerminalSummary) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if r.terminal == nil {
		r.terminal = summary
		return
	}
	if *r.terminal != *summary {
		r.latchLocked(recordingWriteError("capture terminal summary", errors.New("conflicting terminal summaries")))
	}
}

func (r *directoryRecorder) enqueue(item directoryEvidenceItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return recording.ErrLiveEvidenceClosed
	}
	if item.bytes < 0 || item.bytes > directoryEvidenceQueueMaxBytes || r.queuedBytes > directoryEvidenceQueueMaxBytes-item.bytes {
		r.latchLocked(recordingWriteError("enqueue recording evidence", errors.New("recording evidence queue is full")))
		return nil
	}
	// Copy PCM only after budget admission, while other producers cannot
	// consume that reservation. No filesystem work acquires this lock.
	if item.kind == evidenceAudio {
		item.frame.Samples = append([]int16(nil), item.frame.Samples...)
	}
	select {
	case r.queue <- item:
		r.queuedBytes += item.bytes
	default:
		r.latchLocked(recordingWriteError("enqueue recording evidence", errors.New("recording evidence queue is full")))
	}
	return nil
}

func (r *directoryRecorder) run() {
	defer close(r.done)
	for item := range r.queue {
		switch item.kind {
		case evidenceMessage:
			r.processMessage(item)
		case evidenceAudio:
			r.processAudio(item)
		case evidenceEvent:
			if r.workerErr == nil {
				r.workerErr = r.writeTranscript(item, transcript.StreamRuntimeEvent, item.payload)
			}
		}
		r.mu.Lock()
		r.queuedBytes -= item.bytes
		r.mu.Unlock()
	}
}

var _ session.LiveRecorder = (*directoryRecorder)(nil)
