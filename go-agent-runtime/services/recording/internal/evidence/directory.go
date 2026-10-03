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
)

const (
	directoryEvidenceQueueCapacity = 256
	directoryEvidenceQueueMaxBytes = 16 << 20
)

type directoryRecorder struct {
	options     recording.LiveEvidenceOptions
	budget      evidenceResourceBudget
	destination string
	claim       *destinationClaim
	lockPath    string
	lock        *os.File
	spool       string

	queue chan directoryEvidenceItem
	done  chan struct{}

	mu              sync.Mutex
	queuedBytes     int64
	queuedItems     int64
	closed          bool
	recordErr       error
	workerErr       error
	sequence        uint64
	client          *os.File
	inputFile       *os.File
	outputFile      *os.File
	inputBytes      uint64
	outputBytes     uint64
	sidecar         *os.File
	sidecarWritten  bool
	writeSpool      func(*os.File, []byte) error
	agent           *os.File
	clientPath      string
	agentPath       string
	inputPaths      []string
	outputPaths     []string
	runtimeAudio    bool
	terminal        *transcript.RecordingTerminalSummary
	conversation    evidenceConversation
	browserArtifact *transcript.BrowserArtifact
	usageMu         sync.Mutex
	usage           recording.ResourceUsage

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
	admitted, err := claimDestination(recording.ClaimOptions{Destination: options.Destination, Kind: recording.ClaimKindDirectory}, options.SyncFile)
	if err != nil {
		return nil, err
	}
	claim, ok := admitted.(*destinationClaim)
	if !ok {
		return nil, errors.Join(errors.New("recording directory claim has an unexpected implementation"), admitted.Release())
	}
	spool, err := os.MkdirTemp("", ".go-agent-runtime-recording-")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create recording spool: %w", err), claim.Release())
	}

	recorder := &directoryRecorder{
		options:      cloneEvidenceOptions(options),
		budget:       budget,
		writeSpool:   writeAll,
		destination:  claim.destination,
		claim:        claim,
		lockPath:     claim.lockPath,
		lock:         claim.lock,
		spool:        spool,
		queue:        make(chan directoryEvidenceItem, directoryEvidenceQueueCapacity),
		done:         make(chan struct{}),
		conversation: newEvidenceConversation(budget.limits.SummaryBytes),
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
	message, redact := redactToolMessage(record.Message, r.options.Credentials)
	payload, err := marshalEvidenceStreamMessage(message)
	if err != nil {
		r.latch(recordingWriteError("encode stream message", err))
		return nil
	}
	payload = redact(payload)
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
	if record.Admission != "" {
		r.mu.Lock()
		r.runtimeAudio = true
		r.mu.Unlock()
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
	errorText = redactEventError(errorText, r.options.Credentials)
	event.DelegationTool = redactDelegationTool(event.DelegationTool, r.options.Credentials)
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

func (r *directoryRecorder) RecordBrowserArtifact(ctx context.Context, artifact *transcript.BrowserArtifact) error {
	if r == nil {
		return recording.ErrLiveEvidenceClosed
	}
	if ctx != nil {
		if err := contextError(ctx); err != nil {
			return err
		}
	}
	if artifact == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return recording.ErrLiveEvidenceClosed
	}
	if r.browserArtifact != nil {
		return errors.New("recording already has a browser artifact")
	}
	cloned := *artifact
	cloned.Data = append([]byte(nil), artifact.Data...)
	r.browserArtifact = &cloned
	return nil
}

// redactDelegationTool keeps a delegation tool call's audit payload with the
// session credentials, in each form transcript.CredentialForms lists, redacted
// from the whole arguments and result before they are bounded, as the trace
// does: transcript payloads are base64, so the bundle's byte redaction
// cannot reach them.
func redactDelegationTool(tool *session.LiveDelegationTool, credentials []string) *session.LiveDelegationTool {
	if tool == nil {
		return nil
	}
	forms := transcript.CredentialForms(credentials)
	audited := tool.Audited(func(value string) string {
		return transcript.RedactCredentialForms(value, forms, transcript.RecordingRedactionMarker)
	})
	return &audited
}

// redactEventError redacts the session credentials from the error text an
// event is recorded with; it reaches the bundle inside a base64 payload too.
func redactEventError(errorText string, credentials []string) string {
	if errorText == "" || len(credentials) == 0 {
		return errorText
	}
	return transcript.RedactCredentialForms(errorText, transcript.CredentialForms(credentials), transcript.RecordingRedactionMarker)
}

// redactToolMessage redacts the session credentials, in each form
// transcript.CredentialForms lists, from the encoded payload of a tool call
// (its name and arguments) or a tool result before it is spooled: transcript
// payloads are base64, out of reach of the bundle's byte redaction. A
// streamed TOOLCALL.DELTA keeps no argument text at all, because a
// credential can be split across two deltas where no per-message redaction
// sees it whole; TOOLCALL.END carries the whole arguments, redacted.
func redactToolMessage(message messages.StreamMessage, credentials []string) (messages.StreamMessage, func([]byte) []byte) {
	keep := func(payload []byte) []byte { return payload }
	switch {
	case len(credentials) == 0:
		return message, keep
	case message.Type == messages.StreamTypeToolCallDelta:
		if delta, ok := message.Value.(*messages.ToolCallDeltaValue); ok && delta != nil {
			blanked := *delta
			blanked.PartialJSON = ""
			message.Value = &blanked
		}
	case message.Type == messages.StreamTypeToolCallStart || message.Type == messages.StreamTypeToolCallEnd:
	case message.Role == messages.RoleTool:
	default:
		return message, keep
	}
	forms := transcript.CredentialForms(credentials)
	return message, func(payload []byte) []byte { return redactRecordingBytes(payload, forms) }
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
	cloned := *value
	cloned.Type = boundedEventText(cloned.Type)
	cloned.SessionID = boundedEventText(cloned.SessionID)
	cloned.Reason = boundedEventText(cloned.Reason)
	cloned.Classification = boundedEventText(cloned.Classification)
	cloned.TerminalReason = messages.TerminalReason(boundedEventText(string(cloned.TerminalReason)))
	cloned.TerminalProvenance = messages.TerminalProvenance(boundedEventText(string(cloned.TerminalProvenance)))
	cloned.OutputState = messages.TerminalOutputState(boundedEventText(string(cloned.OutputState)))
	return &cloned
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

var _ session.LiveRecorder = (*directoryRecorder)(nil)
