// Command c53-consumer is a source-built public-runtime regression consumer.
// It imports only exported contracts and uses a deterministic provider fixture
// to make the provider-close/public-terminal ordering observable.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const maxTraceEvents = 256

type fixtureDocument struct {
	Audio struct {
		Format     string `json:"format"`
		SampleRate int    `json:"sample_rate"`
		Channels   int    `json:"channels"`
	} `json:"audio"`
	Interruption struct {
		CancelledAudio   []int  `json:"cancelled_audio"`
		InterruptedAudio []int  `json:"interrupted_audio"`
		HealthyTail      []int  `json:"healthy_tail"`
		HealthyText      string `json:"healthy_text"`
	} `json:"interruption"`
	Terminal struct {
		Reason         string `json:"reason"`
		Classification string `json:"classification"`
		TerminalReason string `json:"terminal_reason"`
		Provenance     string `json:"provenance"`
		OutputState    string `json:"output_state"`
	} `json:"terminal"`
}

type traceRecord struct {
	Sequence       int    `json:"sequence"`
	Kind           string `json:"kind"`
	ResponseID     string `json:"response_id,omitempty"`
	Role           string `json:"role,omitempty"`
	Text           string `json:"text,omitempty"`
	PCMBytes       int    `json:"pcm_bytes,omitempty"`
	PCMSHA256      string `json:"pcm_sha256,omitempty"`
	TerminalSource string `json:"terminal_source,omitempty"`
}

type audioRecord struct {
	Format     string `json:"format"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Bytes      int    `json:"bytes"`
	SHA256     string `json:"sha256"`
}

type terminalRecord struct {
	Kind           string `json:"kind"`
	Reason         string `json:"reason"`
	Classification string `json:"classification"`
	Provenance     string `json:"provenance"`
	OutputState    string `json:"output_state"`
}

type interruptionRecord struct {
	CancelSent                   bool   `json:"cancel_sent"`
	CancelResponseID             string `json:"cancel_response_id"`
	CancelBoundarySequence       int    `json:"cancel_boundary_sequence"`
	CancellationTerminalObserved bool   `json:"cancellation_terminal_observed"`
	DelayedAudioObserved         bool   `json:"delayed_audio_observed"`
	DelayedAudioBytes            int    `json:"delayed_audio_bytes"`
	DelayedAudioPubliclyEmitted  bool   `json:"delayed_audio_publicly_emitted"`
	CancelledOutputRejected      bool   `json:"cancelled_output_rejected"`
	HealthyResponseID            string `json:"healthy_response_id"`
	HealthyTailBytes             int    `json:"healthy_tail_bytes"`
	HealthyTailSHA256            string `json:"healthy_tail_sha256"`
	HealthyTailNonEmpty          bool   `json:"healthy_tail_nonempty"`
}

type providerTraceRecord struct {
	Sequence   int    `json:"sequence"`
	Kind       string `json:"kind"`
	ResponseID string `json:"response_id,omitempty"`
	PCMBytes   int    `json:"pcm_bytes,omitempty"`
}

type providerRecord struct {
	Controls             []string              `json:"controls"`
	Trace                []providerTraceRecord `json:"trace"`
	DelayedAudioObserved bool                  `json:"delayed_audio_observed"`
	DelayedAudioBytes    int                   `json:"delayed_audio_bytes"`
	Closed               bool                  `json:"closed"`
}

type lifecycleRecord struct {
	Opened      bool   `json:"opened"`
	Started     bool   `json:"started"`
	CloseCalled bool   `json:"close_called"`
	WaitCalled  bool   `json:"wait_called"`
	CloseError  string `json:"close_error,omitempty"`
	WaitError   string `json:"wait_error,omitempty"`
}

type report struct {
	Schema          string             `json:"schema"`
	Scenario        string             `json:"scenario"`
	Mutation        string             `json:"mutation,omitempty"`
	SourceRevision  string             `json:"source_revision"`
	FixtureSHA256   string             `json:"fixture_sha256"`
	ConsumerSurface string             `json:"consumer_surface"`
	Trace           []traceRecord      `json:"trace"`
	PublicPCM       audioRecord        `json:"public_pcm"`
	HealthyPCM      audioRecord        `json:"healthy_pcm"`
	Terminal        terminalRecord     `json:"terminal"`
	Interruption    interruptionRecord `json:"interruption"`
	Provider        providerRecord     `json:"provider"`
	Lifecycle       lifecycleRecord    `json:"lifecycle"`
	CleanShutdown   bool               `json:"clean_shutdown"`
	TraceComplete   bool               `json:"trace_complete"`
	Error           string             `json:"error,omitempty"`
}

type eventCollector struct {
	trace             []traceRecord
	publicPCM         []byte
	responseAudio     map[string][]byte
	responseText      map[string]string
	healthyEndSource  messages.TerminalSource
	cancellationEnd   bool
	sessionCloseCount int
	terminalCount     int
	terminal          *messages.SessionCloseValue
}

func newEventCollector() *eventCollector {
	return &eventCollector{responseAudio: make(map[string][]byte), responseText: make(map[string]string)}
}

func (c *eventCollector) record(event session.LiveEvent) {
	if len(c.trace) >= maxTraceEvents {
		return
	}
	item := traceRecord{Sequence: len(c.trace), Kind: event.Kind, Role: string(event.Role), ResponseID: event.ResponseID}
	if event.Message != nil {
		msg := event.Message
		item.Kind, item.ResponseID, item.Role = string(msg.Type), msg.ResponseID, string(msg.Role)
		switch value := msg.Value.(type) {
		case *messages.AudioDeltaValue:
			item.PCMBytes, item.PCMSHA256 = len(value.Content), sha256Hex(value.Content)
			c.publicPCM = append(c.publicPCM, value.Content...)
			c.responseAudio[msg.ResponseID] = append(c.responseAudio[msg.ResponseID], value.Content...)
		case *messages.TextDeltaValue:
			item.Text = value.Content
			c.responseText[msg.ResponseID] += value.Content
		case *messages.MessageEndValue:
			item.TerminalSource = string(messages.MessageEndTerminalSource(value))
			if msg.ResponseID == "interrupt-resp-1" {
				c.cancellationEnd = true
			}
			if msg.ResponseID == "healthy-resp-2" {
				c.healthyEndSource = messages.MessageEndTerminalSource(value)
			}
		case *messages.SessionCloseValue:
			c.terminal = value
		}
		if msg.Type == messages.StreamTypeSessionClose {
			c.sessionCloseCount++
			if value, ok := msg.Value.(*messages.SessionCloseValue); ok {
				c.terminal = value
			}
		}
	} else if event.Kind == string(session.LiveEventTerminal) {
		c.terminalCount++
		c.terminal = event.Terminal
	}
	c.trace = append(c.trace, item)
}

type fixtureSession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	started   bool
	cancelled bool
	healthy   bool
	mutation  string
	controls  []string
	trace     []providerTraceRecord
	delayed   []byte
}

type fixtureInferencer struct{ provider *fixtureSession }

func (i fixtureInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.provider, nil
}

func newFixtureSession(mutation string) *fixtureSession {
	return &fixtureSession{receive: messages.NewTypedBuffer[messages.StreamMessage](64), done: make(chan struct{}), mutation: mutation}
}

func (s *fixtureSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	switch msg.Type {
	case messages.StreamTypeTextDelta:
		s.mu.Lock()
		first := !s.started
		s.started = true
		s.mu.Unlock()
		if first {
			initialAudio := []byte{1, 2, 3, 4}
			if s.mutation == "admitted-cancelled-audio" {
				initialAudio = []byte{9, 9, 9, 9}
			}
			s.queue(
				messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "interrupt-resp-1", Value: messages.NewMessageStartValue()},
				messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "interrupt-resp-1", Value: messages.NewTextDeltaValue("partial-before-cancel")},
				messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, ResponseID: "interrupt-resp-1", Value: messages.NewAudioStartValue()},
				messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: "interrupt-resp-1", Value: messages.NewAudioDeltaValue(initialAudio)},
			)
		}
	case messages.StreamTypeResponseCancel:
		s.mu.Lock()
		first := !s.cancelled
		s.cancelled = true
		s.controls = append(s.controls, string(messages.StreamTypeResponseCancel))
		s.mu.Unlock()
		if first {
			s.delayed = []byte{9, 9, 9, 9}
			responseID := "interrupt-resp-1"
			if s.mutation == "admitted-cancelled-audio" {
				responseID = "healthy-resp-2"
			}
			s.queue(
				messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewAudioDeltaValue(s.delayed)},
				messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "interrupt-resp-1", Value: messages.NewMessageEndValueWithTerminal(messages.TokenUsage{}, messages.TerminalReasonCancellation, messages.TerminalProvenanceProvider, messages.TerminalOutputPartial)},
			)
		}
	case messages.StreamTypeResponseCreate:
		s.mu.Lock()
		ready := s.cancelled && !s.healthy
		s.healthy = true
		s.controls = append(s.controls, string(messages.StreamTypeResponseCreate))
		s.mu.Unlock()
		if ready {
			s.queueHealthy()
			s.Close()
		}
	}
	return true
}

func (s *fixtureSession) queue(events ...messages.StreamMessage) {
	for _, event := range events {
		if !s.receive.WriteWaitContextOrDone(context.Background(), s.done, event).OK() {
			return
		}
		s.mu.Lock()
		item := providerTraceRecord{Sequence: len(s.trace), Kind: string(event.Type), ResponseID: event.ResponseID}
		if value, ok := event.Value.(*messages.AudioDeltaValue); ok {
			item.PCMBytes = len(value.Content)
		}
		s.trace = append(s.trace, item)
		s.mu.Unlock()
	}
}

func (s *fixtureSession) queueHealthy() {
	tail := []byte{21, 34, 55, 89, 144, 233, 13, 8}
	if s.mutation == "mutated-healthy-pcm" {
		tail[0]++
	}
	end := messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "healthy-resp-2", Value: messages.NewMessageEndValueWithTerminal(messages.TokenUsage{}, messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)}
	closeMessage := messages.StreamMessage{Type: messages.StreamTypeSessionClose, ResponseID: "healthy-resp-2", Value: messages.NewSessionCloseValueWithTerminal("c53-provider", "fixture_complete", "fixture", messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)}
	healthy := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "healthy-resp-2", Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, ResponseID: "healthy-resp-2", Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: "healthy-resp-2", Value: messages.NewAudioDeltaValue(tail)},
		{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, ResponseID: "healthy-resp-2", Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "healthy-resp-2", Value: messages.NewTextDeltaValue("healthy-after-cancel")},
		end,
		closeMessage,
	}
	switch s.mutation {
	case "missing-message-end":
		healthy = append(healthy[:5], healthy[6:]...)
	case "duplicate-message-end":
		healthy = append(healthy[:6], append([]messages.StreamMessage{end}, healthy[6:]...)...)
	case "reordered-session-close":
		healthy[5], healthy[6] = healthy[6], healthy[5]
	}
	s.queue(healthy...)
}

func (s *fixtureSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *fixtureSession) Done() <-chan struct{}                                  { return s.done }
func (s *fixtureSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *fixtureSession) snapshot() providerRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	trace := append([]providerTraceRecord(nil), s.trace...)
	controls := append([]string(nil), s.controls...)
	closed := false
	select {
	case <-s.done:
		closed = true
	default:
	}
	return providerRecord{Controls: controls, Trace: trace, DelayedAudioObserved: len(s.delayed) == 4, DelayedAudioBytes: len(s.delayed), Closed: closed}
}

func run(fixturePath, mutation, sourceRevision string) (*report, error) {
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		return nil, fmt.Errorf("read fixture: %w", err)
	}
	var fixture fixtureDocument
	if err := json.Unmarshal(raw, &fixture); err != nil {
		return nil, fmt.Errorf("decode fixture: %w", err)
	}
	provider := newFixtureSession(mutation)
	base := clock.NewDeterministic(time.Date(2026, time.January, 2, 3, 5, 5, 0, time.UTC), time.Millisecond)
	service := sessionwire.NewLiveService(sessionwire.LiveDependencies{InferencerFactory: func(_ context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
		provider.queue(messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue(request.SessionID, "audio_inference")})
		return fixtureInferencer{provider: provider}, nil
	}, EventCapacity: 128, Clock: base.Now, Scheduler: clock.Real{}})
	r := &report{Schema: "audio-runtime.c53.v1", Scenario: "provider-close-overtake", Mutation: mutation, SourceRevision: sourceRevision, FixtureSHA256: sha256Hex(raw), ConsumerSurface: "public-live-service", TraceComplete: true}
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{SessionID: "c53-provider-close", ParticipantID: "fixture", Provider: "deterministic", Model: "c53-fixture", OpeningPrompt: "start interrupt fixture", OpeningPromptPresent: true, OutputAudioSampleRate: fixture.Audio.SampleRate, OutputAudioContinuous: true})
	if err != nil {
		return r, err
	}
	r.Lifecycle.Opened = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := handle.Start(ctx); err != nil {
		r.Lifecycle.Started = true
		r.Error = err.Error()
		_ = handle.Close()
		_ = handle.Wait()
		return r, err
	}
	r.Lifecycle.Started = true
	collector := newEventCollector()
	cancelSent, createSent := false, false
	interruption := interruptionRecord{}
	eventErr := error(nil)
	for {
		select {
		case event, ok := <-handle.Events():
			if !ok {
				goto finished
			}
			collector.record(event)
			if event.Error != nil && event.Kind != string(session.LiveEventTerminal) && event.Kind != string(session.LiveEventOverflow) {
				eventErr = event.Error
			}
			if event.Message == nil {
				continue
			}
			msg := event.Message
			if !cancelSent && msg.ResponseID == "interrupt-resp-1" && msg.Type == messages.StreamTypeAudioDelta {
				if err := handle.Send(ctx, session.LiveControl{Kind: session.LiveControlResponseCancel}); err != nil {
					eventErr = fmt.Errorf("send response.cancel: %w", err)
					goto finished
				}
				cancelSent = true
				interruption.CancelSent = true
				interruption.CancelResponseID = msg.ResponseID
				interruption.CancelBoundarySequence = len(collector.trace) - 1
			}
			if cancelSent && !createSent && msg.ResponseID == "interrupt-resp-1" && msg.Type == messages.StreamTypeMessageEnd {
				interruption.CancellationTerminalObserved = true
				if err := handle.Send(ctx, session.LiveControl{Kind: session.LiveControlResponseCreate}); err != nil {
					eventErr = fmt.Errorf("send response.create: %w", err)
					goto finished
				}
				createSent = true
			}
			if createSent && msg.ResponseID == "healthy-resp-2" && msg.Type == messages.StreamTypeMessageEnd {
				interruption.HealthyResponseID = msg.ResponseID
			}
		case <-ctx.Done():
			eventErr = ctx.Err()
			goto finished
		}
	}

finished:
	r.Lifecycle.CloseCalled = true
	closeErr := handle.Close()
	r.Lifecycle.WaitCalled = true
	waitErr := handle.Wait()
	if closeErr != nil {
		r.Lifecycle.CloseError = closeErr.Error()
	}
	if waitErr != nil {
		r.Lifecycle.WaitError = waitErr.Error()
	}
	r.Trace = collector.trace
	r.Provider = provider.snapshot()
	r.PublicPCM = audioRecord{Format: fixture.Audio.Format, SampleRate: fixture.Audio.SampleRate, Channels: fixture.Audio.Channels, Bytes: len(collector.publicPCM), SHA256: sha256Hex(collector.publicPCM)}
	healthy := collector.responseAudio["healthy-resp-2"]
	r.HealthyPCM = audioRecord{Format: fixture.Audio.Format, SampleRate: fixture.Audio.SampleRate, Channels: fixture.Audio.Channels, Bytes: len(healthy), SHA256: sha256Hex(healthy)}
	interruption.DelayedAudioObserved, interruption.DelayedAudioBytes = r.Provider.DelayedAudioObserved, r.Provider.DelayedAudioBytes
	interruption.DelayedAudioPubliclyEmitted = hasPublicAudio(collector, []byte{9, 9, 9, 9})
	interruption.CancelledOutputRejected = bytes.Equal(collector.responseAudio["interrupt-resp-1"], []byte{1, 2, 3, 4}) && !interruption.DelayedAudioPubliclyEmitted
	interruption.HealthyTailBytes, interruption.HealthyTailSHA256 = len(healthy), sha256Hex(healthy)
	interruption.HealthyTailNonEmpty = len(healthy) > 0
	r.Interruption = interruption
	if collector.terminal != nil {
		r.Terminal = terminalRecord{Kind: "terminal", Reason: collector.terminal.Reason, Classification: collector.terminal.Classification, Provenance: string(collector.terminal.TerminalProvenance), OutputState: string(collector.terminal.OutputState)}
	}
	r.Terminal.Kind = "terminal"
	r.CleanShutdown = closeErr == nil && waitErr == nil && r.Provider.Closed && collector.terminalCount == 1
	r.TraceComplete = len(collector.trace) > 0 && collector.terminalCount == 1 && collector.trace[len(collector.trace)-1].Kind == string(session.LiveEventTerminal)
	if eventErr != nil {
		r.Error = eventErr.Error()
	}
	if waitErr != nil {
		r.Error = waitErr.Error()
	}
	if closeErr != nil {
		r.Error = closeErr.Error()
	}
	if err := validatePositive(r, fixture, collector); err != nil {
		r.Error = err.Error()
		return r, err
	}
	return r, nil
}

func hasDelayedPublicAudio(collector *eventCollector, boundary int, delayed []byte) bool {
	for _, event := range collector.trace {
		if event.Sequence > boundary && event.Kind == string(messages.StreamTypeAudioDelta) && event.PCMSHA256 == sha256Hex(delayed) {
			return true
		}
	}
	return false
}

func hasPublicAudio(collector *eventCollector, delayed []byte) bool {
	for _, event := range collector.trace {
		if event.Kind != string(messages.StreamTypeAudioDelta) || event.PCMSHA256 != sha256Hex(delayed) {
			continue
		}
		return true
	}
	return false
}

func validatePositive(r *report, fixture fixtureDocument, collector *eventCollector) error {
	wantKinds := []string{"started", "SESSION.OPEN", "MESSAGE.START", "TEXT.DELTA", "AUDIO.START", "AUDIO.DELTA", "MESSAGE.END", "MESSAGE.START", "AUDIO.START", "AUDIO.DELTA", "AUDIO.END", "TEXT.DELTA", "MESSAGE.END", "SESSION.CLOSE", "terminal"}
	if len(r.Trace) != len(wantKinds) {
		return fmt.Errorf("public trace length = %d, want %d", len(r.Trace), len(wantKinds))
	}
	for index, want := range wantKinds {
		if r.Trace[index].Kind != want {
			return fmt.Errorf("public trace[%d] = %s, want %s", index, r.Trace[index].Kind, want)
		}
	}
	if !r.Interruption.CancelSent || !r.Interruption.CancellationTerminalObserved || r.Interruption.HealthyResponseID != "healthy-resp-2" {
		return errors.New("interruption controls did not reach the healthy response")
	}
	if !r.Provider.DelayedAudioObserved || r.Provider.DelayedAudioBytes != 4 || r.Interruption.DelayedAudioPubliclyEmitted || !r.Interruption.CancelledOutputRejected {
		return errors.New("cancelled provider audio was not observed-and-rejected exactly")
	}
	if collector.responseText["healthy-resp-2"] != fixture.Interruption.HealthyText {
		return fmt.Errorf("healthy text = %q, want %q", collector.responseText["healthy-resp-2"], fixture.Interruption.HealthyText)
	}
	wantTail := bytesFromInts(fixture.Interruption.HealthyTail)
	if !bytes.Equal(collector.responseAudio["healthy-resp-2"], wantTail) {
		return fmt.Errorf("healthy PCM = %s, want %s", sha256Hex(collector.responseAudio["healthy-resp-2"]), sha256Hex(wantTail))
	}
	if collector.healthyEndSource != messages.TerminalSourceProvider {
		return fmt.Errorf("healthy MESSAGE.END source = %s, want provider", collector.healthyEndSource)
	}
	if r.Trace[13].ResponseID != "" || r.Terminal.Reason != fixture.Terminal.Reason || r.Terminal.Classification != fixture.Terminal.Classification || r.Terminal.Provenance != fixture.Terminal.Provenance || r.Terminal.OutputState != fixture.Terminal.OutputState {
		return fmt.Errorf("provider close metadata = %+v, want fixture provider completion", r.Terminal)
	}
	if !r.CleanShutdown || !r.TraceComplete || r.Lifecycle.CloseError != "" || r.Lifecycle.WaitError != "" {
		return errors.New("public provider-close run did not shut down cleanly")
	}
	return nil
}

func bytesFromInts(values []int) []byte {
	result := make([]byte, len(values))
	for index, value := range values {
		if value < 0 || value > 255 {
			return nil
		}
		result[index] = byte(value)
	}
	return result
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func main() {
	fixturePath := flag.String("fixture", "", "fixture JSON path")
	outputPath := flag.String("output", "", "report JSON path")
	mutation := flag.String("mutation", "", "negative control mutation")
	sourceRevision := flag.String("source-revision", os.Getenv("C53_SOURCE_REVISION"), "candidate source revision")
	flag.Parse()
	if *fixturePath == "" || *outputPath == "" {
		fmt.Fprintln(os.Stderr, "fixture and output are required")
		os.Exit(2)
	}
	r, err := run(*fixturePath, *mutation, *sourceRevision)
	if r == nil {
		r = &report{Schema: "audio-runtime.c53.v1", Scenario: "provider-close-overtake", Mutation: *mutation, SourceRevision: *sourceRevision, ConsumerSurface: "public-live-service"}
	}
	if err != nil && r.Error == "" {
		r.Error = err.Error()
	}
	if makeErr := os.MkdirAll(filepath.Dir(*outputPath), 0o755); makeErr == nil {
		if raw, marshalErr := json.MarshalIndent(r, "", "  "); marshalErr == nil {
			_ = os.WriteFile(*outputPath, append(raw, '\n'), 0o644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
