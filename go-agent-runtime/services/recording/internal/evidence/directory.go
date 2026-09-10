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

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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
	budget      evidenceResourceBudget
	destination string
	lockPath    string
	lock        *os.File
	spool       string

	queue chan directoryEvidenceItem
	done  chan struct{}

	mu             sync.Mutex
	queuedBytes    int64
	queuedItems    int64
	closed         bool
	recordErr      error
	workerErr      error
	sequence       uint64
	client         *os.File
	inputFile      *os.File
	outputFile     *os.File
	inputBytes     uint64
	outputBytes    uint64
	sidecar        *os.File
	sidecarWritten bool
	writeSpool     func(*os.File, []byte) error
	agent          *os.File
	clientPath     string
	agentPath      string
	inputPaths     []string
	outputPaths    []string
	terminal       *transcript.RecordingTerminalSummary
	conversation   evidenceConversation
	usageMu        sync.Mutex
	usage          recording.ResourceUsage

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
	terminal  *messages.SessionCloseValue
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
	budget, err := newEvidenceResourceBudget(options.Limits)
	if err != nil {
		return nil, err
	}
	options.Limits = budget.limits
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
		options:      cloneEvidenceOptions(options),
		budget:       budget,
		writeSpool:   writeAll,
		destination:  destination,
		lockPath:     destination + ".lock",
		lock:         lock,
		spool:        spool,
		queue:        make(chan directoryEvidenceItem, directoryEvidenceQueueCapacity),
		done:         make(chan struct{}),
		conversation: newEvidenceConversation(),
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
		event.Terminal = boundedTerminalValue(event.Terminal)
		r.retainTerminal(terminalSummary(event.Terminal))
	}
	errorText := ""
	if event.Error != nil {
		errorText = event.Error.Error()
	}
	event.Error = nil
	payload, err := encodeRuntimeEvent(event, errorText)
	if err != nil {
		r.latch(recordingWriteError("encode runtime event", err))
		return nil
	}
	item := directoryEvidenceItem{kind: evidenceEvent, direction: session.LiveRecordAgent, timestamp: event.Timestamp, payload: payload, bytes: int64(len(payload)) * 2}
	if event.Terminal != nil {
		terminal := *event.Terminal
		item.terminal = &terminal
	}
	return r.enqueue(item)
}

func encodeRuntimeEvent(event session.LiveEvent, errorText string) ([]byte, error) {
	payload, err := json.Marshal(struct {
		Event session.LiveEvent `json:"event"`
		Error string            `json:"error,omitempty"`
	}{Event: event, Error: errorText})
	if err != nil || int64(len(payload))*2 <= directoryEvidenceQueueMaxBytes {
		return payload, err
	}
	if event.Terminal == nil {
		return nil, io.ErrShortBuffer
	}
	event.Terminal = boundedTerminalValue(event.Terminal)
	event.Message = nil
	event.Capability = nil
	event.Liveness = nil
	event.Text = boundedEventText(event.Text)
	event.Reason = boundedEventText(event.Reason)
	payload, err = json.Marshal(struct {
		Event session.LiveEvent `json:"event"`
		Error string            `json:"error,omitempty"`
	}{Event: event, Error: boundedEventText(errorText)})
	if err != nil {
		return nil, err
	}
	if int64(len(payload))*2 > directoryEvidenceQueueMaxBytes {
		return nil, io.ErrShortBuffer
	}
	return payload, nil
}

func boundedEventText(value string) string {
	const maxEventTextBytes = 2048
	if len(value) <= maxEventTextBytes {
		return value
	}
	return value[:maxEventTextBytes]
}

func boundedTerminalValue(value *messages.SessionCloseValue) *messages.SessionCloseValue {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Type = boundedEventText(copy.Type)
	copy.SessionID = boundedEventText(copy.SessionID)
	copy.Reason = boundedEventText(copy.Reason)
	copy.Classification = boundedEventText(copy.Classification)
	copy.TerminalReason = messages.TerminalReason(boundedEventText(string(copy.TerminalReason)))
	copy.TerminalProvenance = messages.TerminalProvenance(boundedEventText(string(copy.TerminalProvenance)))
	copy.OutputState = messages.TerminalOutputState(boundedEventText(string(copy.OutputState)))
	return &copy
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
		r.queuedItems++
		r.usageMu.Lock()
		r.usage.QueueBytes = r.queuedBytes
		r.usage.QueueItems = r.queuedItems
		if r.queuedBytes > r.usage.PeakQueueBytes {
			r.usage.PeakQueueBytes = r.queuedBytes
		}
		if r.queuedItems > r.usage.PeakQueueItems {
			r.usage.PeakQueueItems = r.queuedItems
		}
		r.usage.AcceptedItems++
		switch item.kind {
		case evidenceMessage:
			r.usage.AcceptedMessages++
		case evidenceAudio:
			r.usage.AcceptedAudio++
		case evidenceEvent:
			r.usage.AcceptedEvents++
		}
		r.usageMu.Unlock()
	default:
		r.latchLocked(recordingWriteError("enqueue recording evidence", errors.New("recording evidence queue is full")))
	}
	return nil
}

func (r *directoryRecorder) run() {
	defer close(r.done)
	for item := range r.queue {
		r.processItem(item)
		r.releaseQueueItem(item)
	}
}

func (r *directoryRecorder) processItem(item directoryEvidenceItem) {
	defer r.captureUsage(true)
	switch item.kind {
	case evidenceMessage:
		r.processMessage(item)
	case evidenceAudio:
		r.processAudio(item)
	case evidenceEvent:
		r.processEvent(item)
	}
}

func (r *directoryRecorder) processEvent(item directoryEvidenceItem) {
	if r.workerErr == nil {
		if err := r.writeTranscript(item, transcript.StreamRuntimeEvent, item.payload); err != nil && !isEvidenceBudgetError(err) {
			r.workerErr = err
		}
	}
	if item.terminal != nil {
		if err := r.writeDurationSidecarTerminal(item.timestamp, item.terminal); err != nil && r.workerErr == nil && !isEvidenceBudgetError(err) {
			r.workerErr = err
		}
	}
}

func (r *directoryRecorder) releaseQueueItem(item directoryEvidenceItem) {
	r.mu.Lock()
	r.queuedBytes -= item.bytes
	if r.queuedItems > 0 {
		r.queuedItems--
	}
	queuedBytes, queuedItems := r.queuedBytes, r.queuedItems
	r.mu.Unlock()
	r.usageMu.Lock()
	r.usage.QueueBytes = queuedBytes
	r.usage.QueueItems = queuedItems
	r.usageMu.Unlock()
}

var _ session.LiveRecorder = (*directoryRecorder)(nil)
