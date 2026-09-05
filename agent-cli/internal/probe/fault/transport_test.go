package fault

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestWrapConnMidStreamCloseIsTypedAndDeterministic(t *testing.T) {
	inner := newFaultTestConn(
		[]faultTestFrame{{Type: 1, Payload: []byte("first")}, {Type: 2, Payload: []byte("second")}, {Type: 1, Payload: []byte("never delivered")}},
	)
	conn, err := WrapConn(inner, WithMidStreamCloseAfter(2))
	if err != nil {
		t.Fatalf("WrapConn: %v", err)
	}

	for index, want := range []faultTestFrame{
		{Type: 1, Payload: []byte("first")},
		{Type: 2, Payload: []byte("second")},
	} {
		gotType, gotPayload, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Fatalf("ReadMessage[%d]: %v", index, readErr)
		}
		if gotType != want.Type || string(gotPayload) != string(want.Payload) {
			t.Fatalf("ReadMessage[%d] = (%d, %q), want (%d, %q)", index, gotType, gotPayload, want.Type, want.Payload)
		}
	}

	_, _, readErr := conn.ReadMessage()
	var closeErr *MidStreamCloseError
	if !errors.As(readErr, &closeErr) {
		t.Fatalf("fault read error = %v, want *MidStreamCloseError", readErr)
	}
	if !errors.Is(readErr, ErrMidStreamClose) || !errors.Is(readErr, io.EOF) {
		t.Fatalf("fault read error = %v, want injected and EOF identities", readErr)
	}
	if closeErr.AfterFrames != 2 || closeErr.ObservedFrames != 2 {
		t.Fatalf("fault metadata = %#v, want after/observed 2", closeErr)
	}
	if got := conn.ReadFrames(); got != 2 {
		t.Fatalf("ReadFrames = %d, want 2", got)
	}
	if got := inner.CloseCount(); got != 1 {
		t.Fatalf("inner close count = %d, want 1", got)
	}

	// The fault remains stable on repeated operations and Close is idempotent;
	// neither behavior can wedge a session trying to tear down after the read.
	_, _, repeatedErr := conn.ReadMessage()
	if !errors.Is(repeatedErr, ErrMidStreamClose) {
		t.Fatalf("repeated read error = %v, want injected close", repeatedErr)
	}
	if writeErr := conn.WriteMessage(1, []byte("after close")); !errors.Is(writeErr, ErrMidStreamClose) {
		t.Fatalf("post-fault write error = %v, want injected close", writeErr)
	}
	if closeErr := conn.Close(); closeErr != nil {
		t.Fatalf("first Close: %v", closeErr)
	}
	if closeErr := conn.Close(); closeErr != nil {
		t.Fatalf("second Close: %v", closeErr)
	}
	if got := inner.CloseCount(); got != 1 {
		t.Fatalf("inner close count after repeated Close = %d, want 1", got)
	}
}

func TestWrapDialerAppliesMidStreamCloseToEveryConnection(t *testing.T) {
	inner := &faultTestDialer{conn: newFaultTestConn([]faultTestFrame{{Type: 1, Payload: []byte("frame")}})}
	dialer, err := WrapDialer(inner, WithMidStreamClose(0))
	if err != nil {
		t.Fatalf("WrapDialer: %v", err)
	}

	conn, err := dialer.Dial("fault://scenario", map[string]string{"X-Test": "fault"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if _, _, err := conn.ReadMessage(); !errors.Is(err, ErrMidStreamClose) {
		t.Fatalf("zero-frame read error = %v, want injected close", err)
	}
	if inner.endpoint != "fault://scenario" || inner.headers["X-Test"] != "fault" {
		t.Fatalf("dial forwarding = endpoint %q headers %#v", inner.endpoint, inner.headers)
	}
	if got := inner.conn.CloseCount(); got != 1 {
		t.Fatalf("inner close count = %d, want 1", got)
	}
}

func TestWrapConnDropsSelectedReadAndWriteFramesAndReportsEvidence(t *testing.T) {
	readInner := newFaultTestConn([]faultTestFrame{
		{Type: 1, Payload: []byte("first")},
		{Type: 2, Payload: []byte("dropped")},
		{Type: 1, Payload: []byte("third")},
	})
	readConn, err := WrapConn(readInner, WithDropReadFrames(2))
	if err != nil {
		t.Fatalf("WrapConn(read): %v", err)
	}

	for index, want := range []faultTestFrame{
		{Type: 1, Payload: []byte("first")},
		{Type: 1, Payload: []byte("third")},
	} {
		gotType, gotPayload, readErr := readConn.ReadMessage()
		if readErr != nil {
			t.Fatalf("ReadMessage[%d]: %v", index, readErr)
		}
		if gotType != want.Type || string(gotPayload) != string(want.Payload) {
			t.Fatalf("ReadMessage[%d] = (%d, %q), want (%d, %q)", index, gotType, gotPayload, want.Type, want.Payload)
		}
	}

	readStats := readConn.Stats()
	if readStats.ReadAttempts != 3 || readStats.ReadFrames != 2 || readStats.DroppedReadFrames != 1 {
		t.Fatalf("read stats = %#v, want attempts=3 frames=2 dropped=1", readStats)
	}
	if len(readStats.Drops) != 1 || readStats.Drops[0] != (FrameEvent{Direction: DirectionInbound, Frame: 2}) {
		t.Fatalf("read drop events = %#v, want inbound frame 2", readStats.Drops)
	}

	writeInner := newFaultTestConn(nil)
	writeConn, err := WrapConn(writeInner, WithDropWriteFrames(2))
	if err != nil {
		t.Fatalf("WrapConn(write): %v", err)
	}
	for index := 1; index <= 3; index++ {
		if writeErr := writeConn.WriteMessage(index, []byte{byte(index)}); writeErr != nil {
			t.Fatalf("WriteMessage[%d]: %v", index, writeErr)
		}
	}

	writeStats := writeConn.Stats()
	if writeStats.WriteAttempts != 3 || writeStats.WrittenFrames != 2 || writeStats.DroppedWriteFrames != 1 {
		t.Fatalf("write stats = %#v, want attempts=3 written=2 dropped=1", writeStats)
	}
	writes := writeInner.Writes()
	if len(writes) != 2 || writes[0].Type != 1 || writes[1].Type != 3 {
		t.Fatalf("forwarded writes = %#v, want frame types [1 3]", writes)
	}
	if len(writeStats.Drops) != 1 || writeStats.Drops[0] != (FrameEvent{Direction: DirectionOutbound, Frame: 2}) {
		t.Fatalf("write drop events = %#v, want outbound frame 2", writeStats.Drops)
	}
}

func TestWrapConnDelaysSelectedFramesOnLogicalClock(t *testing.T) {
	logicalClock := platformclock.NewDeterministic(time.Time{}, time.Millisecond)
	inner := newFaultTestConn([]faultTestFrame{
		{Type: 1, Payload: []byte("immediate")},
		{Type: 1, Payload: []byte("delayed")},
	})
	conn, err := WrapConn(
		inner,
		WithClock(logicalClock),
		WithReadFrameDelay(2*time.Millisecond, 2),
	)
	if err != nil {
		t.Fatalf("WrapConn: %v", err)
	}

	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("immediate ReadMessage: %v", err)
	}
	if got := logicalClock.Tick(); got != 0 {
		t.Fatalf("logical clock after immediate frame = %d, want 0", got)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("delayed ReadMessage: %v", err)
	}
	if got := logicalClock.Tick(); got != 2 {
		t.Fatalf("logical clock after delayed frame = %d, want 2", got)
	}

	stats := conn.Stats()
	if stats.DelayedReadFrames != 1 || len(stats.Delays) != 1 {
		t.Fatalf("delay stats = %#v, want one delayed read", stats)
	}
	delay := stats.Delays[0]
	if delay.Direction != DirectionInbound || delay.Frame != 2 || delay.Duration != 2*time.Millisecond ||
		delay.BeforeTick != 0 || delay.AfterTick != 2 {
		t.Fatalf("delay event = %#v, want inbound frame 2 from tick 0 to 2", delay)
	}
}

func TestWrapConnStallsSelectedEgressFramesOnLogicalClock(t *testing.T) {
	logicalClock := platformclock.NewDeterministic(time.Time{}, time.Millisecond)
	inner := newFaultTestConn([]faultTestFrame{
		{Type: 1, Payload: []byte("immediate")},
		{Type: 1, Payload: []byte("stalled")},
	})
	conn, err := WrapConn(
		inner,
		WithClock(logicalClock),
		WithEgressStall(3*time.Millisecond, 2),
	)
	if err != nil {
		t.Fatalf("WrapConn: %v", err)
	}

	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("immediate ReadMessage: %v", err)
	}
	if got := logicalClock.Tick(); got != 0 {
		t.Fatalf("logical clock after immediate frame = %d, want 0", got)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("stalled ReadMessage: %v", err)
	}
	if got := logicalClock.Tick(); got != 3 {
		t.Fatalf("logical clock after stalled frame = %d, want 3", got)
	}

	stats := conn.Stats()
	if stats.StalledReadFrames != 1 || len(stats.Stalls) != 1 {
		t.Fatalf("stall stats = %#v, want one stalled read", stats)
	}
	stall := stats.Stalls[0]
	if stall != (StallEvent{
		Direction:  DirectionInbound,
		Frame:      2,
		Duration:   3 * time.Millisecond,
		BeforeTick: 0,
		AfterTick:  3,
	}) {
		t.Fatalf("stall event = %#v, want inbound frame 2 from tick 0 to 3", stall)
	}
}

func TestWrapConnSlowConsumerRequiresLogicalClock(t *testing.T) {
	_, err := WrapConn(
		newFaultTestConn(nil),
		WithSlowConsumer(time.Millisecond),
	)
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("WrapConn error = %v, want ErrInvalidConfiguration", err)
	}
}

func TestMidStreamCloseChangesGrokSessionOutcomeFromCleanCompletion(t *testing.T) {
	clean := runFaultScenario(t)
	faulted := runFaultScenario(t, WithMidStreamCloseAfter(3))

	if clean.err != nil {
		t.Fatalf("clean scenario error: %v", clean.err)
	}
	if !clean.sawMessageEnd {
		t.Fatalf("clean scenario did not reach MESSAGE.END: %#v", clean.messages)
	}
	if clean.sawError {
		t.Fatalf("clean scenario emitted ERROR: %#v", clean.messages)
	}

	if faulted.err != nil {
		t.Fatalf("faulted scenario harness error: %v", faulted.err)
	}
	if !faulted.sawError {
		t.Fatalf("faulted scenario did not emit typed ERROR: %#v", faulted.messages)
	}
	if faulted.sawMessageEnd {
		t.Fatalf("faulted scenario reached clean MESSAGE.END: %#v", faulted.messages)
	}
	if !errors.Is(faulted.errorValue.Err, ErrMidStreamClose) {
		t.Fatalf("faulted stream error = %v, want injected close identity", faulted.errorValue.Err)
	}
	var closeErr *MidStreamCloseError
	if !errors.As(faulted.errorValue.Err, &closeErr) {
		t.Fatalf("faulted stream error = %v, want typed close error", faulted.errorValue.Err)
	}
	if closeErr.AfterFrames != 3 || closeErr.ObservedFrames != 3 {
		t.Fatalf("faulted close metadata = %#v, want after/observed 3", closeErr)
	}
	if faulted.errorValue.Classification != "transport" ||
		faulted.errorValue.TerminalReason != messages.TerminalReasonTerminalFailure ||
		faulted.errorValue.TerminalProvenance != messages.TerminalProvenanceProvider {
		t.Fatalf("faulted terminal value = %#v, want typed transport failure", faulted.errorValue)
	}
}

func TestDroppedFrameChangesGrokSessionOutcomeAndReportsDrop(t *testing.T) {
	clean := runFaultScenario(t)
	faulted := runFaultScenario(t, WithDropReadFrames(3))

	if clean.err != nil || faulted.err != nil {
		t.Fatalf("clean/faulted scenario errors = %v/%v", clean.err, faulted.err)
	}
	if !clean.sawMessageEnd || !faulted.sawMessageEnd {
		t.Fatalf("clean/faulted completion = %v/%v; messages=%#v/%#v", clean.sawMessageEnd, faulted.sawMessageEnd, clean.messages, faulted.messages)
	}
	if !clean.sawTextDelta {
		t.Fatalf("clean scenario did not contain the scripted text delta: %#v", clean.messages)
	}
	if faulted.sawTextDelta {
		t.Fatalf("dropped scenario retained the scripted text delta: %#v", faulted.messages)
	}
	if len(clean.messages) == len(faulted.messages) {
		t.Fatalf("dropped scenario did not change message stream length: clean=%#v faulted=%#v", clean.messages, faulted.messages)
	}

	stats := faulted.faultConn.Stats()
	if stats.DroppedReadFrames != 1 || stats.ReadAttempts != 4 || stats.ReadFrames != 3 {
		t.Fatalf("faulted drop stats = %#v, want one dropped frame from four attempts", stats)
	}
	if len(stats.Drops) != 1 || stats.Drops[0].Direction != DirectionInbound || stats.Drops[0].Frame != 3 {
		t.Fatalf("faulted drop evidence = %#v, want inbound frame 3", stats.Drops)
	}
}

func TestDelayedFrameChangesGrokLogicalTimelineAndReportsDelay(t *testing.T) {
	clean := runFaultScenario(t)
	logicalClock := platformclock.NewDeterministic(time.Time{}, time.Millisecond)
	faulted := runFaultScenario(t, WithClock(logicalClock), WithReadFrameDelay(2*time.Millisecond, 3))

	if clean.err != nil || faulted.err != nil {
		t.Fatalf("clean/faulted scenario errors = %v/%v", clean.err, faulted.err)
	}
	if !clean.sawMessageEnd || !faulted.sawMessageEnd {
		t.Fatalf("clean/faulted completion = %v/%v", clean.sawMessageEnd, faulted.sawMessageEnd)
	}
	if len(clean.messages) != len(faulted.messages) {
		t.Fatalf("delay changed message count unexpectedly: clean=%d faulted=%d", len(clean.messages), len(faulted.messages))
	}
	for index := range clean.messages {
		if clean.messages[index].Type != faulted.messages[index].Type {
			t.Fatalf("message type[%d] = %q, want clean %q", index, faulted.messages[index].Type, clean.messages[index].Type)
		}
	}
	if got := logicalClock.Tick(); got != 2 {
		t.Fatalf("logical clock after delayed scenario = %d, want 2", got)
	}

	stats := faulted.faultConn.Stats()
	if stats.DelayedReadFrames != 1 || len(stats.Delays) != 1 {
		t.Fatalf("faulted delay stats = %#v, want one delayed frame", stats)
	}
	delay := stats.Delays[0]
	if delay.Direction != DirectionInbound || delay.Frame != 3 || delay.Duration != 2*time.Millisecond ||
		delay.BeforeTick != 0 || delay.AfterTick != 2 {
		t.Fatalf("faulted delay evidence = %#v, want inbound frame 3 from tick 0 to 2", delay)
	}
}

func TestSlowConsumerChangesGrokAudioStreamThroughTransportBackpressure(t *testing.T) {
	cleanClock := platformclock.NewDeterministic(time.Time{}, time.Millisecond)
	cleanConn := newScheduledFaultTestConn(audioBurstScenarioFrames(), cleanClock)
	clean := runFaultScenarioWithConn(t, cleanConn)
	clean.sourceDrops = cleanConn.SourceDrops()
	faultClock := platformclock.NewDeterministic(time.Time{}, time.Millisecond)
	faultConn := newScheduledFaultTestConn(audioBurstScenarioFrames(), faultClock)
	faulted := runFaultScenarioWithConn(
		t,
		faultConn,
		WithClock(faultClock),
		WithSlowConsumer(3*time.Millisecond, 3),
	)
	faulted.sourceDrops = faultConn.SourceDrops()

	if clean.err != nil || faulted.err != nil {
		t.Fatalf("clean/faulted scenario errors = %v/%v", clean.err, faulted.err)
	}
	if !clean.sawMessageEnd || !faulted.sawMessageEnd {
		t.Fatalf("clean/faulted completion = %v/%v; messages=%#v/%#v", clean.sawMessageEnd, faulted.sawMessageEnd, clean.messages, faulted.messages)
	}
	if clean.sawError || faulted.sawError {
		t.Fatalf("clean/faulted audio scenarios emitted ERROR: %#v/%#v", clean.messages, faulted.messages)
	}
	if !clean.sawAudioDelta || !faulted.sawAudioDelta {
		t.Fatalf("clean/faulted scenarios did not deliver audio: %#v/%#v", clean.messages, faulted.messages)
	}
	if faulted.audioDeltaCount >= clean.audioDeltaCount {
		t.Fatalf("slow consumer did not degrade emitted audio: clean=%d faulted=%d; messages=%#v/%#v", clean.audioDeltaCount, faulted.audioDeltaCount, clean.messages, faulted.messages)
	}
	if faulted.sourceDrops == 0 {
		t.Fatalf("slow consumer changed no scheduled egress frames: clean=%#v faulted=%#v", clean.messages, faulted.messages)
	}

	stats := faulted.faultConn.Stats()
	if stats.StalledReadFrames != 1 || len(stats.Stalls) != 1 {
		t.Fatalf("faulted stall stats = %#v, want one bounded egress stall", stats)
	}
	stall := stats.Stalls[0]
	if stall.Direction != DirectionInbound || stall.Frame != 3 || stall.Duration != 3*time.Millisecond ||
		stall.BeforeTick != 2 || stall.AfterTick != 5 {
		t.Fatalf("faulted stall evidence = %#v, want inbound frame 3 from tick 2 to 5", stall)
	}
}

func TestGrokTransportFaultRemainsObservableWithFullReceiveBuffer(t *testing.T) {
	frames := fullReceiveBufferFaultFrames()
	rawConn := newFaultTestConn(frames)
	wrapped, err := WrapConn(rawConn, WithMidStreamCloseAfter(len(frames)))
	if err != nil {
		t.Fatalf("WrapConn: %v", err)
	}
	provider := grok.New(
		grok.WithAPIKey("fault-test-key"),
		grok.WithBaseURL("wss://fault.test/v1/realtime"),
		grok.WithWebSocketDialer(&staticFaultDialer{conn: wrapped}),
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session, err := provider.ConnectSession(ctx, structSessionConfig())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	select {
	case <-session.Done():
	case <-ctx.Done():
		t.Fatalf("faulted session did not terminate: %v", ctx.Err())
	}

	var terminal *messages.ErrorValue
	for {
		msg, ok := session.Receive().Read()
		if !ok {
			break
		}
		if msg.Type != messages.StreamTypeError {
			continue
		}
		value, ok := msg.Value.(*messages.ErrorValue)
		if !ok || value == nil {
			t.Fatalf("terminal ERROR value = %T, want *messages.ErrorValue", msg.Value)
		}
		terminal = value
	}
	if terminal == nil {
		t.Fatal("full receive buffer swallowed the injected terminal ERROR")
	}
	if !errors.Is(terminal.Err, ErrMidStreamClose) {
		t.Fatalf("terminal error = %v, want injected mid-stream close", terminal.Err)
	}
	var closeErr *MidStreamCloseError
	if !errors.As(terminal.Err, &closeErr) {
		t.Fatalf("terminal error = %v, want typed MidStreamCloseError", terminal.Err)
	}
	if terminal.Classification != "transport" ||
		terminal.TerminalReason != messages.TerminalReasonTerminalFailure ||
		terminal.TerminalProvenance != messages.TerminalProvenanceProvider {
		t.Fatalf("terminal value = %#v, want typed transport terminal failure", terminal)
	}
}

type faultScenarioResult struct {
	messages        []messages.StreamMessage
	sawMessageEnd   bool
	sawError        bool
	sawTextDelta    bool
	sawAudioDelta   bool
	audioDeltaCount int
	sourceDrops     int
	errorValue      *messages.ErrorValue
	faultConn       *Conn
	err             error
}

func runFaultScenario(t *testing.T, options ...Option) faultScenarioResult {
	return runFaultScenarioFrames(t, faultScenarioFrames(), options...)
}

func runFaultScenarioFrames(t *testing.T, frames []faultTestFrame, options ...Option) faultScenarioResult {
	t.Helper()
	return runFaultScenarioWithConn(t, newFaultTestConn(frames), options...)
}

func runFaultScenarioWithConn(t *testing.T, rawConn transport.Conn, options ...Option) faultScenarioResult {
	t.Helper()
	var dialer transport.Dialer = &staticFaultDialer{conn: rawConn}
	var faultConn *Conn
	if len(options) > 0 {
		wrapped, err := WrapConn(rawConn, options...)
		if err != nil {
			t.Fatalf("WrapConn: %v", err)
		}
		faultConn = wrapped
		dialer = &staticFaultDialer{conn: wrapped}
	}
	provider := grok.New(
		grok.WithAPIKey("fault-test-key"),
		grok.WithBaseURL("wss://fault.test/v1/realtime"),
		grok.WithWebSocketDialer(dialer),
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session, err := provider.ConnectSession(ctx, structSessionConfig())
	if err != nil {
		return faultScenarioResult{faultConn: faultConn, err: err}
	}
	defer func() { _ = session.Close() }()

	result := faultScenarioResult{faultConn: faultConn}
	for {
		msg, ok := session.Receive().ReadBlockingContext(ctx)
		if !ok {
			result.err = ctx.Err()
			return result
		}
		result.messages = append(result.messages, msg)
		switch msg.Type {
		case messages.StreamTypeTextDelta:
			result.sawTextDelta = true
		case messages.StreamTypeAudioDelta:
			result.sawAudioDelta = true
			result.audioDeltaCount++
		case messages.StreamTypeMessageEnd:
			result.sawMessageEnd = true
			return result
		case messages.StreamTypeError:
			result.sawError = true
			value, ok := msg.Value.(*messages.ErrorValue)
			if !ok || value == nil {
				result.err = errors.New("session ERROR carried a non-ErrorValue")
				return result
			}
			result.errorValue = value
			return result
		}
	}
}

func structSessionConfig() models.SessionConfig {
	return models.SessionConfig{Model: "grok-fault-injection"}
}

func faultScenarioFrames() []faultTestFrame {
	return []faultTestFrame{
		{Type: 1, Payload: []byte(`{"type":"session.created","session_id":"fault-session","model":"grok-fault-injection"}`)},
		{Type: 1, Payload: []byte(`{"type":"response.created"}`)},
		{Type: 1, Payload: []byte(`{"type":"response.text.delta","delta":"same deterministic answer"}`)},
		{Type: 1, Payload: []byte(`{"type":"response.done"}`)},
	}
}

func audioBurstScenarioFrames() []faultTestFrame {
	frames := []faultTestFrame{
		{Type: 1, Payload: []byte(`{"type":"session.created","session_id":"fault-audio-burst","model":"grok-fault-injection"}`)},
		{Type: 1, Payload: []byte(`{"type":"response.created"}`)},
	}
	for i := 0; i < 6; i++ {
		frames = append(frames, faultTestFrame{Type: 1, Payload: []byte(`{"type":"response.audio.delta","delta":"AQIDBA=="}`)})
	}
	return append(frames,
		faultTestFrame{Type: 1, Payload: []byte(`{"type":"response.audio.done"}`)},
		faultTestFrame{Type: 1, Payload: []byte(`{"type":"response.done"}`)},
	)
}

func fullReceiveBufferFaultFrames() []faultTestFrame {
	frames := []faultTestFrame{
		{Type: 1, Payload: []byte(`{"type":"session.created","session_id":"fault-full-buffer","model":"grok-fault-injection"}`)},
		{Type: 1, Payload: []byte(`{"type":"response.created"}`)},
	}
	for i := 0; i < 70; i++ {
		frames = append(frames, faultTestFrame{Type: 1, Payload: []byte(`{"type":"response.audio.delta","delta":"AQIDBA=="}`)})
	}
	return frames
}

type faultTestFrame struct {
	Type    int
	Payload []byte
}

type faultTestConn struct {
	mu        sync.Mutex
	frames    []faultTestFrame
	writes    []faultTestFrame
	readIdx   int
	closed    bool
	closeCh   chan struct{}
	closeOnce sync.Once
	closeN    int
}

// scheduledFaultTestConn models an upstream egress producer that schedules one
// frame per logical tick. If the transport consumer is stalled, frames whose
// delivery ticks have passed are discarded before the next read. This keeps
// the slow-consumer proof deterministic while exercising a changed session
// output stream instead of asserting only decorator-local stall statistics.
type scheduledFaultTestConn struct {
	*faultTestConn
	clock       *platformclock.Deterministic
	nextTick    uint64
	sourceDrops int
}

func newScheduledFaultTestConn(frames []faultTestFrame, clock *platformclock.Deterministic) *scheduledFaultTestConn {
	return &scheduledFaultTestConn{
		faultTestConn: newFaultTestConn(frames),
		clock:         clock,
	}
}

func (c *scheduledFaultTestConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, nil, io.EOF
	}

	currentTick := c.clock.Tick()
	for c.readIdx < len(c.frames) && c.nextTick < currentTick {
		c.readIdx++
		c.nextTick++
		c.sourceDrops++
	}
	if c.readIdx >= len(c.frames) {
		return 0, nil, io.EOF
	}
	if currentTick < c.nextTick {
		c.clock.AdvanceTo(c.nextTick)
	}
	frame := c.frames[c.readIdx]
	c.readIdx++
	c.nextTick++
	return frame.Type, append([]byte(nil), frame.Payload...), nil
}

func (c *scheduledFaultTestConn) SourceDrops() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sourceDrops
}

func newFaultTestConn(frames []faultTestFrame) *faultTestConn {
	owned := make([]faultTestFrame, len(frames))
	for i, frame := range frames {
		owned[i] = faultTestFrame{Type: frame.Type, Payload: append([]byte(nil), frame.Payload...)}
	}
	return &faultTestConn{frames: owned, closeCh: make(chan struct{})}
}

func (c *faultTestConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, nil, io.EOF
	}
	if c.readIdx < len(c.frames) {
		frame := c.frames[c.readIdx]
		c.readIdx++
		c.mu.Unlock()
		return frame.Type, append([]byte(nil), frame.Payload...), nil
	}
	c.mu.Unlock()

	<-c.closeCh
	return 0, nil, io.EOF
}

func (c *faultTestConn) WriteMessage(messageType int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return io.ErrClosedPipe
	}
	c.writes = append(c.writes, faultTestFrame{Type: messageType, Payload: append([]byte(nil), payload...)})
	return nil
}

func (c *faultTestConn) Writes() []faultTestFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	writes := make([]faultTestFrame, len(c.writes))
	for index, frame := range c.writes {
		writes[index] = faultTestFrame{Type: frame.Type, Payload: append([]byte(nil), frame.Payload...)}
	}
	return writes
}

func (c *faultTestConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.closeN++
		c.mu.Unlock()
		close(c.closeCh)
	})
	return nil
}

func (c *faultTestConn) CloseCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeN
}

type faultTestDialer struct {
	conn     *faultTestConn
	endpoint string
	headers  map[string]string
}

type staticFaultDialer struct {
	conn transport.Conn
}

var _ transport.Dialer = (*staticFaultDialer)(nil)

func (d *staticFaultDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return d.conn, nil
}

var _ transport.Dialer = (*faultTestDialer)(nil)

func (d *faultTestDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	d.endpoint = endpoint
	d.headers = make(map[string]string, len(headers))
	for key, value := range headers {
		d.headers[key] = value
	}
	return d.conn, nil
}
