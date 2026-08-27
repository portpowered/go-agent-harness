package integration

// s2s async-tool-result collision vertical: CLI-verified hermetic (T1) proof
// that an outstanding provider tool call can overlap a later response's
// streamed audio without losing the local result, changing the audio bytes,
// or wedging session teardown.
//
// The replay transport is deliberately gated at the supported websocket
// dialer seam. The real CLI schedules one first audio turn, the transport
// waits for its tool response to complete, then accepts the positional prompt
// as a distinct later user turn while the controllable executor remains
// blocked. It emits one collision audio delta, releases the executor only
// after that delta crosses the CLI stream observer, and holds the remaining
// collision audio until the normalized tool result is ready. This makes
// the overlap causal rather than timing-based while keeping the behavior
// assertion at the public `agent session` command boundary.
//
// The production session path forwards normalized tool results through the
// provider-facing stream. The verifier therefore requires the real outbound
// function_call_output, rather than treating local RoleTool delivery or a
// scripted capture record as a substitute for wire evidence.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const (
	asyncCollisionPrompt             = "finish the pending weather lookup"
	asyncCollisionCallID             = "call_async_weather_1"
	asyncCollisionToolName           = "get_weather"
	asyncCollisionToolArgs           = `{"city":"Lisbon"}`
	asyncCollisionResult             = `{"temperature_c":24,"condition":"clear","sentinel":"async-result-001"}`
	asyncCollisionSessionID          = "sess_async_tool_result_interrupts_speech"
	asyncCollisionResponseOne        = "resp_async_tool_1"
	asyncCollisionResponseTwo        = "resp_async_speech"
	asyncCollisionResponseThree      = "resp_async_continuation"
	asyncCollisionCloseReason        = "async_collision_complete"
	asyncCollisionDeltaSamples       = 1600
	asyncCollisionDeltaCount         = 2
	asyncCollisionInputSamples       = audio.FrameSize
	asyncCollisionMaxDuration        = 10 * time.Second
	asyncCollisionControlMaxDuration = 250 * time.Millisecond
	asyncCollisionDisposition        = "queue/sequence"
)

// asyncCollisionTrace records the causal milestones asserted by the positive
// proof. The observer and executor use the same mutex, so the order is based on
// runtime events rather than elapsed time.
type asyncCollisionTrace struct {
	mu     sync.Mutex
	events []string
}

func (t *asyncCollisionTrace) record(event string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.events = append(t.events, event)
	t.mu.Unlock()
}

func (t *asyncCollisionTrace) snapshot() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.events...)
}

// asyncCollisionToolExecutor blocks the single provider-issued call until the
// stream observer sees the first audio delta of the unrelated later response.
// It records both the incoming call and the deterministic sentinel response.
type asyncCollisionToolExecutor struct {
	trace *asyncCollisionTrace

	started         chan struct{}
	release         chan struct{}
	resultReady     chan struct{}
	startedOnce     sync.Once
	releaseOnce     sync.Once
	resultReadyOnce sync.Once

	mu       sync.Mutex
	calls    []messages.ToolCall
	returned []messages.ToolCallResponse
}

func newAsyncCollisionToolExecutor(trace *asyncCollisionTrace) *asyncCollisionToolExecutor {
	return &asyncCollisionToolExecutor{
		trace:       trace,
		started:     make(chan struct{}),
		release:     make(chan struct{}),
		resultReady: make(chan struct{}),
	}
}

func (e *asyncCollisionToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	e.startedOnce.Do(func() {
		e.trace.record("tool_started")
		close(e.started)
	})

	select {
	case <-e.release:
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}

	e.trace.record("tool_returned")
	response := messages.ToolCallResponse{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    asyncCollisionResult,
	}
	e.mu.Lock()
	e.returned = append(e.returned, response)
	e.mu.Unlock()
	e.resultReadyOnce.Do(func() { close(e.resultReady) })
	return response, nil
}

func (e *asyncCollisionToolExecutor) releaseResult() {
	e.releaseOnce.Do(func() { close(e.release) })
}

func (e *asyncCollisionToolExecutor) snapshot() (calls []messages.ToolCall, returned []messages.ToolCallResponse) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...), append([]messages.ToolCallResponse(nil), e.returned...)
}

// asyncCollisionObserver watches the deltas consumed by the CLI's session
// runner. The first collision audio delta releases the blocked executor; the
// sentinel RoleTool text delta records local result delivery. This is the
// causal overlap contract used by the test.
type asyncCollisionObserver struct {
	trace       *asyncCollisionTrace
	executor    *asyncCollisionToolExecutor
	firstAudio  []byte
	secondAudio []byte

	turnOneCompleted       chan struct{}
	turnOneCompletedOnce   sync.Once
	collisionCompleted     chan struct{}
	collisionCompletedOnce sync.Once

	mu                        sync.Mutex
	deltas                    []messages.StreamMessage
	responseCompleted         int
	collisionAudioSeen        int
	collisionResponseComplete bool
}

func newAsyncCollisionObserver(trace *asyncCollisionTrace, executor *asyncCollisionToolExecutor, firstAudio, secondAudio []byte) *asyncCollisionObserver {
	return &asyncCollisionObserver{
		trace:              trace,
		executor:           executor,
		firstAudio:         append([]byte(nil), firstAudio...),
		secondAudio:        append([]byte(nil), secondAudio...),
		turnOneCompleted:   make(chan struct{}),
		collisionCompleted: make(chan struct{}),
	}
}

func (o *asyncCollisionObserver) observe(msg messages.StreamMessage) {
	o.mu.Lock()
	o.deltas = append(o.deltas, msg)
	o.mu.Unlock()

	switch value := msg.Value.(type) {
	case *messages.SessionOpenValue:
		o.trace.record("session_open_observed")
	case *messages.SessionCreatedValue:
		o.trace.record("session_created_observed")
	case *messages.MessageEndValue:
		o.mu.Lock()
		o.responseCompleted++
		firstResponseComplete := o.responseCompleted == 1
		o.mu.Unlock()
		if firstResponseComplete {
			o.trace.record("turn_one_completed")
			o.turnOneCompletedOnce.Do(func() { close(o.turnOneCompleted) })
		}
	case *messages.AudioDeltaValue:
		if value == nil {
			return
		}
		if bytes.Equal(value.Content, o.firstAudio) {
			o.trace.record("collision_audio_1_observed")
			o.mu.Lock()
			o.collisionAudioSeen++
			o.mu.Unlock()
			o.executor.releaseResult()
		}
		if bytes.Equal(value.Content, o.secondAudio) {
			o.trace.record("collision_audio_2_observed")
			o.mu.Lock()
			o.collisionAudioSeen++
			o.mu.Unlock()
		}
	case *messages.AudioEndValue:
		o.mu.Lock()
		complete := o.collisionAudioSeen >= asyncCollisionDeltaCount && !o.collisionResponseComplete
		if complete {
			o.collisionResponseComplete = true
		}
		o.mu.Unlock()
		if complete {
			o.trace.record("collision_response_completed")
			o.collisionCompletedOnce.Do(func() { close(o.collisionCompleted) })
		}
	case *messages.TextDeltaValue:
		if msg.Role == messages.RoleTool && value != nil && value.Content == asyncCollisionResult {
			o.trace.record("tool_result_observed")
		}
	}
}

func (o *asyncCollisionObserver) snapshot() []messages.StreamMessage {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]messages.StreamMessage(nil), o.deltas...)
}

// asyncCollisionOutbound is the provider-facing exchange observed by the
// gated replay connection. It intentionally records every outbound event even
// when the current provider adapter fails to serialize the expected result.
type asyncCollisionOutbound struct {
	Type    string
	Payload []byte
}

// asyncCollisionReplayDialer replays a synthetic OpenAI capture through the
// actual provider session adapter while independently gating server phases.
// Independent client/server scheduling is necessary here because the tool
// result and later speech are intentionally concurrent sources.
type asyncCollisionReplayDialer struct {
	groups  asyncCollisionServerGroups
	control asyncCollisionReplayControl

	mu   sync.Mutex
	conn *asyncCollisionReplayConn
}

type asyncCollisionReplayControl struct {
	signals                   *asyncCollisionSignals
	toolStarted               <-chan struct{}
	turnOneCompleted          <-chan struct{}
	toolResultReady           <-chan struct{}
	collisionResponseComplete <-chan struct{}
	expectedInputAudio        []byte
	trace                     *asyncCollisionTrace
	dropProviderResult        bool
	withholdTerminal          bool
}

// asyncCollisionRunOptions mutates exactly one observable outcome of the
// healthy replay. The command, fixture shape, causal gates, and verifiers stay
// shared by every control.
type asyncCollisionRunOptions struct {
	maxDuration        time.Duration
	dropProviderResult bool
	withholdTerminal   bool
}

func (o asyncCollisionRunOptions) normalized() asyncCollisionRunOptions {
	if o.maxDuration <= 0 {
		o.maxDuration = asyncCollisionMaxDuration
	}
	return o
}

func newAsyncCollisionReplayDialer(capture gwtesting.SessionCapture, control asyncCollisionReplayControl) (*asyncCollisionReplayDialer, error) {
	serverEvents := make([]gwtesting.CapturedSessionEvent, 0)
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionServerToClient {
			serverEvents = append(serverEvents, record)
		}
	}
	groups, err := splitAsyncCollisionServerEvents(serverEvents)
	if err != nil {
		return nil, err
	}
	return &asyncCollisionReplayDialer{
		groups:  groups,
		control: control,
	}, nil
}

func (d *asyncCollisionReplayDialer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	conn := &asyncCollisionReplayConn{
		groups:  d.groups,
		control: d.control,
		closed:  make(chan struct{}),
	}
	d.conn = conn
	return conn, nil
}

func (d *asyncCollisionReplayDialer) outboundSnapshot() []asyncCollisionOutbound {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.outboundSnapshot()
}

type asyncCollisionServerGroups struct {
	handshake     []gwtesting.CapturedSessionEvent
	turnOne       []gwtesting.CapturedSessionEvent
	collisionHead []gwtesting.CapturedSessionEvent
	collisionTail []gwtesting.CapturedSessionEvent
	continuation  []gwtesting.CapturedSessionEvent
	terminal      []gwtesting.CapturedSessionEvent
}

func splitAsyncCollisionServerEvents(events []gwtesting.CapturedSessionEvent) (asyncCollisionServerGroups, error) {
	var groups asyncCollisionServerGroups
	var current *[]gwtesting.CapturedSessionEvent
	for _, event := range events {
		switch event.Type {
		case "session.created":
			groups.handshake = append(groups.handshake, event)
		case "response.created":
			var payload struct {
				Response struct {
					ID string `json:"id"`
				} `json:"response"`
			}
			if err := json.Unmarshal(eventPayload(event), &payload); err != nil {
				return asyncCollisionServerGroups{}, fmt.Errorf("decode response.created fixture: %w", err)
			}
			switch payload.Response.ID {
			case asyncCollisionResponseOne:
				current = &groups.turnOne
			case asyncCollisionResponseTwo:
				current = &groups.collisionHead
			case asyncCollisionResponseThree:
				current = &groups.continuation
			default:
				return asyncCollisionServerGroups{}, fmt.Errorf("unexpected response.created ID %q in async collision fixture", payload.Response.ID)
			}
			*current = append(*current, event)
		case "session.closed":
			groups.terminal = append(groups.terminal, event)
		default:
			if current == nil {
				return asyncCollisionServerGroups{}, fmt.Errorf("server event %q precedes a response.created event", event.Type)
			}
			*current = append(*current, event)
		}
	}
	if len(groups.handshake) != 1 || len(groups.turnOne) == 0 || len(groups.collisionHead) == 0 || len(groups.continuation) == 0 || len(groups.terminal) != 1 {
		return asyncCollisionServerGroups{}, fmt.Errorf("async collision fixture phases are incomplete: handshake=%d turn_one=%d collision=%d continuation=%d terminal=%d", len(groups.handshake), len(groups.turnOne), len(groups.collisionHead), len(groups.continuation), len(groups.terminal))
	}

	firstAudio := -1
	for i, event := range groups.collisionHead {
		if event.Type == "response.output_audio.delta" {
			firstAudio = i
			break
		}
	}
	if firstAudio < 0 {
		return asyncCollisionServerGroups{}, errors.New("async collision response has no first audio delta")
	}
	groups.collisionHead, groups.collisionTail = groups.collisionHead[:firstAudio+1], groups.collisionHead[firstAudio+1:]
	return groups, nil
}

// asyncCollisionReplayConn is a deterministic, capture-backed websocket
// connection. Client writes are recorded independently of server reads so the
// test can hold an inbound speech delta while a result-driven provider write
// is concurrently attempted; all phase gates are channel-based.
type asyncCollisionReplayConn struct {
	groups  asyncCollisionServerGroups
	control asyncCollisionReplayControl

	mu        sync.Mutex
	stage     int
	pending   []gwtesting.CapturedSessionEvent
	closed    chan struct{}
	closeOnce sync.Once
	outbound  []asyncCollisionOutbound
}

var _ transport.Conn = (*asyncCollisionReplayConn)(nil)

func (c *asyncCollisionReplayConn) ReadMessage() (int, []byte, error) {
	for {
		c.mu.Lock()
		if len(c.pending) > 0 {
			event := c.pending[0]
			c.pending = c.pending[1:]
			c.mu.Unlock()
			return 1, eventPayload(event), nil
		}
		stage := c.stage
		if stage >= 6 {
			c.mu.Unlock()
			<-c.closed
			return 0, nil, io.EOF
		}
		c.mu.Unlock()

		var (
			waitFor <-chan struct{}
			phase   []gwtesting.CapturedSessionEvent
		)
		switch stage {
		case 0:
			// OpenAI ConnectSession writes its initial session.update before it
			// starts the provider read loop. The outbound write is therefore the
			// happens-before edge for this first server phase; waiting on a
			// second notification here can strand an otherwise connected replay
			// if the provider read loop starts after that notification.
			phase = c.groups.handshake
		case 1:
			waitFor = c.control.signals.initialResponseReady
			phase = c.groups.turnOne
		case 2:
			// This phase is released only by the second, independent
			// response.create. The connection never emits the collision
			// response merely because the tool started.
			waitFor = c.control.signals.laterResponseReady
			phase = c.groups.collisionHead
		case 3:
			// The executor's return is the result-availability boundary. The
			// local RoleTool delta is observed independently by the shared
			// verifier and must not be needed to release the response tail.
			waitFor = c.control.toolResultReady
			phase = c.groups.collisionTail
		case 4:
			waitFor = c.control.signals.continuationReady
			phase = c.groups.continuation
		case 5:
			if !c.control.withholdTerminal {
				phase = c.groups.terminal
			} else {
				waitFor = c.control.signals.terminalReady
			}
		}
		if err := c.waitForPhase(waitFor); err != nil {
			return 0, nil, err
		}

		c.mu.Lock()
		if c.stage == stage {
			c.pending = append(c.pending, phase...)
			c.stage++
		}
		c.mu.Unlock()
	}
}

func (c *asyncCollisionReplayConn) waitForPhase(waitFor <-chan struct{}) error {
	if waitFor == nil {
		return nil
	}
	select {
	case <-waitFor:
		return nil
	case <-c.closed:
		return io.EOF
	}
}

func (c *asyncCollisionReplayConn) WriteMessage(_ int, payload []byte) error {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Type == "" {
		if err == nil {
			err = errors.New("event has no type")
		}
		return fmt.Errorf("async collision replay rejected outbound event: %w", err)
	}
	switch envelope.Type {
	case "conversation.item.create":
		if err := validateAsyncOutboundConversationItem(payload); err != nil {
			return err
		}
		if isAsyncCollisionFunctionCallOutput(payload) {
			// Queue/sequence is the selected disposition. A provider-facing
			// result is not recorded until the current collision response has
			// crossed its final audio boundary, so a result cannot race ahead
			// of the response it is queued behind.
			if err := c.waitForPhase(c.control.collisionResponseComplete); err != nil {
				return fmt.Errorf("async collision replay could not reach the queue/sequence result boundary: %w", err)
			}
			// The loss control acts at the provider-facing transport boundary. It
			// accepts the write but removes only the correlated result from the
			// observed exchange, leaving the rest of the session healthy.
			if c.control.dropProviderResult {
				return nil
			}
			c.control.trace.record("provider_result_sent")
		} else {
			// The initial positional prompt starts the outstanding tool call and
			// is accepted immediately. The result-driven continuation is the
			// second user message and must follow the collision response boundary.
			if c.countOutboundUserMessages() > 0 {
				if err := c.waitForPhase(c.control.collisionResponseComplete); err != nil {
					return fmt.Errorf("async collision replay could not reach the continuation request boundary: %w", err)
				}
			}
		}
	case "input_audio_buffer.append":
		if err := validateAsyncOutboundInputAudio(payload, c.control.expectedInputAudio); err != nil {
			return err
		}
	case "input_audio_buffer.commit":
		// The exact audio frame is validated on append; the commit is the
		// distinct second-turn boundary that precedes response.create.
	case "session.update", "response.create":
	default:
		return fmt.Errorf("async collision replay rejected unexpected outbound event type %q", envelope.Type)
	}

	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return io.ErrClosedPipe
	default:
	}
	c.outbound = append(c.outbound, asyncCollisionOutbound{
		Type:    envelope.Type,
		Payload: append([]byte(nil), payload...),
	})
	c.mu.Unlock()

	switch envelope.Type {
	case "session.update":
		c.control.signals.markSessionUpdate()
	case "response.create":
		if count := c.countOutboundType("response.create"); count == 1 {
			c.control.signals.markInitialResponse()
		} else if count == 2 {
			if err := c.waitForPhase(c.control.toolStarted); err != nil {
				return fmt.Errorf("async collision replay could not reach the outstanding tool-call boundary: %w", err)
			}
			if err := c.waitForPhase(c.control.turnOneCompleted); err != nil {
				return fmt.Errorf("async collision replay could not reach the first response boundary: %w", err)
			}
			c.control.trace.record("later_turn_requested")
			c.control.signals.markLaterResponse()
		} else if count == 3 {
			c.control.trace.record("continuation_requested")
			c.control.signals.markContinuation()
		} else {
			return fmt.Errorf("async collision replay received %d response.create events, want exactly three", count)
		}
	case "conversation.item.create", "input_audio_buffer.append", "input_audio_buffer.commit":
		// The payload was validated before it was recorded.
	}
	return nil
}

func validateAsyncOutboundConversationItem(payload []byte) error {
	var envelope struct {
		Item struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			CallID  string `json:"call_id"`
			Output  string `json:"output"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("decode outbound conversation.item.create: %w", err)
	}
	switch envelope.Item.Type {
	case "message":
		if envelope.Item.Role != "user" || len(envelope.Item.Content) != 1 || envelope.Item.Content[0].Text != asyncCollisionPrompt {
			return fmt.Errorf("outbound user turn payload = %+v, want one user message carrying %q", envelope.Item, asyncCollisionPrompt)
		}
	case "function_call_output":
		if envelope.Item.CallID != asyncCollisionCallID || envelope.Item.Output != asyncCollisionResult {
			return fmt.Errorf("outbound function_call_output = {call_id:%q output:%q}, want original ID %q and sentinel %q", envelope.Item.CallID, envelope.Item.Output, asyncCollisionCallID, asyncCollisionResult)
		}
	default:
		return fmt.Errorf("outbound conversation item type %q is not a user message or function_call_output", envelope.Item.Type)
	}
	return nil
}

func isAsyncCollisionFunctionCallOutput(payload []byte) bool {
	var envelope struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	return json.Unmarshal(payload, &envelope) == nil && envelope.Item.Type == "function_call_output"
}

func (c *asyncCollisionReplayConn) countOutboundType(eventType string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, event := range c.outbound {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func (c *asyncCollisionReplayConn) countOutboundUserMessages() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, event := range c.outbound {
		if event.Type != "conversation.item.create" {
			continue
		}
		var payload struct {
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && payload.Item.Type == "message" {
			count++
		}
	}
	return count
}

func (c *asyncCollisionReplayConn) outboundSnapshot() []asyncCollisionOutbound {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]asyncCollisionOutbound, len(c.outbound))
	for i, event := range c.outbound {
		out[i] = asyncCollisionOutbound{Type: event.Type, Payload: append([]byte(nil), event.Payload...)}
	}
	return out
}

func (c *asyncCollisionReplayConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

// asyncCollisionSessionInferencer accepts the generic SESSION.UPDATE emitted
// by the CLI's instruction decorator. The OpenAI provider has already sent its
// provider-specific initial session.update during ConnectSession; this replay
// only needs to observe the later command-layer configuration without turning
// that unsupported outbound stream value into a test failure.
type asyncCollisionSessionInferencer struct {
	inner messages.SessionInferencer
	trace *asyncCollisionTrace
}

func (i *asyncCollisionSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return &asyncCollisionSession{inner: session, trace: i.trace}, nil
}

type asyncCollisionSession struct {
	inner messages.Session
	trace *asyncCollisionTrace
}

func (s *asyncCollisionSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionUpdate {
		s.trace.record("generic_session_update_suppressed")
		return true
	}
	s.trace.record("session_send_" + string(msg.Type))
	ok := s.inner.Send(ctx, msg)
	if !ok {
		s.trace.record("session_send_failed_" + string(msg.Type))
	}
	return ok
}

func (s *asyncCollisionSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.inner.Receive()
}

func (s *asyncCollisionSession) Done() <-chan struct{} { return s.inner.Done() }

func (s *asyncCollisionSession) Close() error { return s.inner.Close() }

func eventPayload(event gwtesting.CapturedSessionEvent) []byte {
	if len(event.Payload) > 0 {
		return event.Payload
	}
	return event.Data
}

// asyncCollisionSignals owns the close-once channels used by the replay
// connection. A separate struct avoids exposing writable channels through the
// control values shared with the provider adapter.
type asyncCollisionSignals struct {
	sessionUpdateReady   chan struct{}
	initialResponseReady chan struct{}
	laterResponseReady   chan struct{}
	continuationReady    chan struct{}
	terminalReady        chan struct{}
}

func newAsyncCollisionSignals() *asyncCollisionSignals {
	return &asyncCollisionSignals{
		sessionUpdateReady:   make(chan struct{}),
		initialResponseReady: make(chan struct{}),
		laterResponseReady:   make(chan struct{}),
		continuationReady:    make(chan struct{}),
		terminalReady:        make(chan struct{}),
	}
}

func (s *asyncCollisionSignals) control(toolStarted, turnOneCompleted, toolResultReady, collisionResponseComplete <-chan struct{}, expectedInputAudio []byte) asyncCollisionReplayControl {
	return asyncCollisionReplayControl{
		signals:                   s,
		toolStarted:               toolStarted,
		turnOneCompleted:          turnOneCompleted,
		toolResultReady:           toolResultReady,
		collisionResponseComplete: collisionResponseComplete,
		expectedInputAudio:        append([]byte(nil), expectedInputAudio...),
	}
}

func (s *asyncCollisionSignals) markSessionUpdate() {
	closeIfOpen(s.sessionUpdateReady)
}

func (s *asyncCollisionSignals) markInitialResponse() {
	closeIfOpen(s.initialResponseReady)
}

func (s *asyncCollisionSignals) markLaterResponse() {
	closeIfOpen(s.laterResponseReady)
}

func (s *asyncCollisionSignals) markContinuation() {
	closeIfOpen(s.continuationReady)
}

func closeIfOpen(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func validateAsyncOutboundInputAudio(payload []byte, want []byte) error {
	var envelope struct {
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("decode outbound input_audio_buffer.append: %w", err)
	}
	got, err := base64.StdEncoding.DecodeString(envelope.Audio)
	if err != nil {
		return fmt.Errorf("decode outbound input audio base64: %w", err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("later-turn input audio differs from the gated fixture: got %d bytes want %d", len(got), len(want))
	}
	return nil
}

func audioDeltaPayload(samples []int16) string {
	payload, _ := json.Marshal(map[string]string{
		"type":  "response.output_audio.delta",
		"delta": base64.StdEncoding.EncodeToString(pcm16LEBytes(samples)),
	})
	return string(payload)
}

func asyncCollisionAudio(t *testing.T) (collision, continuation [][]int16) {
	t.Helper()
	wavBytes, err := os.ReadFile(toolSingleCallWAVPath(t))
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	window := loudestWindowSamplesIntegration(t, samples, asyncCollisionDeltaSamples)
	all := make([][]int16, asyncCollisionDeltaCount*2)
	for i := range all {
		all[i] = make([]int16, len(window))
		shift := int16(i + 1)
		if i >= asyncCollisionDeltaCount {
			shift = int16(31 + i)
		}
		for j, sample := range window {
			all[i][j] = sample + shift
		}
	}
	return all[:asyncCollisionDeltaCount], all[asyncCollisionDeltaCount:]
}

func asyncCollisionInputAudio() []byte {
	samples := make([]int16, asyncCollisionInputSamples)
	for i := range samples {
		samples[i] = int16(700 + (i % 29))
	}
	return pcm16LEBytes(samples)
}

func inputAudioPayload(audioBytes []byte) string {
	payload, _ := json.Marshal(map[string]string{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(audioBytes),
	})
	return string(payload)
}

func functionCallOutputPayload() string {
	payload, _ := json.Marshal(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]string{
			"type":    "function_call_output",
			"call_id": asyncCollisionCallID,
			"output":  asyncCollisionResult,
		},
	})
	return string(payload)
}

func buildAsyncCollisionFixture(t *testing.T, collision, continuation [][]int16, inputAudio []byte) (string, gwtesting.SessionCapture) {
	t.Helper()
	base, err := gwtesting.LoadSessionCapture(filepath.Join("testdata", "openai_realtime_smoke.session.json"))
	if err != nil {
		t.Fatalf("load OpenAI replay baseline: %v", err)
	}
	records := []gwtesting.CapturedSessionEvent{base.Records[0], base.Records[1]}
	add := func(direction gwtesting.SessionEventDirection, eventType, payload string) {
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   direction,
			TimestampMs: int64(len(records)),
			Type:        eventType,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(payload),
		})
	}
	userPayload, _ := json.Marshal(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []map[string]string{{"type": "input_text", "text": asyncCollisionPrompt}},
		},
	})
	// The positional prompt is the first user turn and causes the outstanding
	// tool call. The CLI delays --audio-in-turn input until this response ends,
	// making the second response a real later turn rather than an unsolicited
	// provider frame.
	add(gwtesting.DirectionClientToServer, "conversation.item.create", string(userPayload))
	add(gwtesting.DirectionClientToServer, "response.create", `{"type":"response.create"}`)
	add(gwtesting.DirectionServerToClient, "response.created", `{"type":"response.created","response":{"id":"`+asyncCollisionResponseOne+`"}}`)
	add(gwtesting.DirectionServerToClient, "response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"`+asyncCollisionCallID+`","name":"`+asyncCollisionToolName+`"}}`)
	add(gwtesting.DirectionServerToClient, "response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","call_id":"`+asyncCollisionCallID+`","name":"`+asyncCollisionToolName+`","arguments":`+strconvQuote(asyncCollisionToolArgs)+`}`)
	add(gwtesting.DirectionServerToClient, "response.done", `{"type":"response.done","response":{"id":"`+asyncCollisionResponseOne+`","status":"completed"}}`)

	// The scheduled audio is the independent later response that overlaps the
	// still outstanding call's result. The gated transport requires this request
	// to follow the first response boundary before releasing collision audio.
	add(gwtesting.DirectionClientToServer, "input_audio_buffer.append", inputAudioPayload(inputAudio))
	add(gwtesting.DirectionClientToServer, "input_audio_buffer.commit", `{"type":"input_audio_buffer.commit"}`)
	add(gwtesting.DirectionClientToServer, "response.create", `{"type":"response.create"}`)
	add(gwtesting.DirectionServerToClient, "response.created", `{"type":"response.created","response":{"id":"`+asyncCollisionResponseTwo+`"}}`)
	for _, delta := range collision {
		add(gwtesting.DirectionServerToClient, "response.output_audio.delta", audioDeltaPayload(delta))
	}
	add(gwtesting.DirectionServerToClient, "response.output_audio.done", `{"type":"response.output_audio.done"}`)
	add(gwtesting.DirectionServerToClient, "response.done", `{"type":"response.done","response":{"id":"`+asyncCollisionResponseTwo+`","status":"completed"}}`)

	// This is the expected queue/sequence boundary: the provider-facing result
	// follows the current collision response's final audio boundary and
	// precedes the result-driven continuation request. The real runtime emits
	// this event through the tool-result forwarder and OpenAI session adapter.
	add(gwtesting.DirectionClientToServer, "conversation.item.create", functionCallOutputPayload())
	add(gwtesting.DirectionClientToServer, "conversation.item.create", string(userPayload))
	add(gwtesting.DirectionClientToServer, "response.create", `{"type":"response.create"}`)

	add(gwtesting.DirectionServerToClient, "response.created", `{"type":"response.created","response":{"id":"`+asyncCollisionResponseThree+`"}}`)
	for _, delta := range continuation {
		add(gwtesting.DirectionServerToClient, "response.output_audio.delta", audioDeltaPayload(delta))
	}
	add(gwtesting.DirectionServerToClient, "response.output_audio.done", `{"type":"response.output_audio.done"}`)
	add(gwtesting.DirectionServerToClient, "response.done", `{"type":"response.done","response":{"id":"`+asyncCollisionResponseThree+`","status":"completed"}}`)
	add(gwtesting.DirectionServerToClient, "session.closed", `{"type":"session.closed","session_id":"`+asyncCollisionSessionID+`","reason":"`+asyncCollisionCloseReason+`"}`)

	base.Session.ID = asyncCollisionSessionID
	base.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	base.Records = records
	data, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		t.Fatalf("marshal async collision fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "async-tool-result-interrupts-speech.session.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write async collision fixture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(path); err != nil {
		t.Fatalf("validate async collision fixture with shared replay validator: %v", err)
	}
	return path, base
}

type asyncCollisionRunResult struct {
	outputPath    string
	sessionOutput string
	outbound      []asyncCollisionOutbound
	executor      *asyncCollisionToolExecutor
	observer      *asyncCollisionObserver
	trace         *asyncCollisionTrace
	runErr        error
}

func runAsyncCollisionScenario(t *testing.T, fixtureCollision, expectedCollision, continuation [][]int16, options asyncCollisionRunOptions) asyncCollisionRunResult {
	t.Helper()
	options = options.normalized()
	trace := &asyncCollisionTrace{}
	executor := newAsyncCollisionToolExecutor(trace)
	observer := newAsyncCollisionObserver(trace, executor, pcm16LEBytes(fixtureCollision[0]), pcm16LEBytes(fixtureCollision[1]))
	signals := newAsyncCollisionSignals()
	inputAudio := asyncCollisionInputAudio()
	wirePath, capture := buildAsyncCollisionFixture(t, fixtureCollision, continuation, inputAudio)
	outputPath, sessionOutput, outbound, runErr := runAsyncCollisionCLI(t, wirePath, capture, inputAudio, executor, observer, signals, options)
	return asyncCollisionRunResult{
		outputPath:    outputPath,
		sessionOutput: sessionOutput,
		outbound:      outbound,
		executor:      executor,
		observer:      observer,
		trace:         trace,
		runErr:        runErr,
	}
}

func runAsyncCollisionCLI(t *testing.T, wirePath string, capture gwtesting.SessionCapture, inputAudio []byte, executor *asyncCollisionToolExecutor, observer *asyncCollisionObserver, signals *asyncCollisionSignals, options asyncCollisionRunOptions) (string, string, []asyncCollisionOutbound, error) {
	t.Helper()
	options = options.normalized()
	control := signals.control(
		executor.started,
		observer.turnOneCompleted,
		executor.resultReady,
		observer.collisionCompleted,
		inputAudio,
	)
	control.trace = executor.trace
	control.dropProviderResult = options.dropProviderResult
	control.withholdTerminal = options.withholdTerminal
	replayDialer, err := newAsyncCollisionReplayDialer(capture, control)
	if err != nil {
		t.Fatalf("build gated async collision replay dialer: %v", err)
	}
	sessionInferencer, err := services.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "replay", Model: "gpt-realtime"},
		oaiprovider.WithWebSocketDialer(replayDialer),
	)
	if err != nil {
		t.Fatalf("build OpenAI realtime session inferencer: %v", err)
	}
	sessionInferencer = &asyncCollisionSessionInferencer{inner: sessionInferencer, trace: executor.trace}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		executor,
		&mockInferencer{response: "unused"},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	agentCLI.SetSessionStreamObserver(observer.observe)
	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	outputPath := filepath.Join(t.TempDir(), "async-collision-response.wav")
	inputPath := filepath.Join(t.TempDir(), "async-collision-first-turn.raw")
	if err := os.WriteFile(inputPath, inputAudio, 0o600); err != nil {
		t.Fatalf("write async collision input fixture: %v", err)
	}
	recordingDir := filepath.Join(t.TempDir(), "async-collision-recording")
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--replay", wirePath,
		"--record-dir", recordingDir,
		"--audio-in-turn", inputPath,
		"--wait-for-close",
		"--audio-out", outputPath,
		"--max-duration", options.maxDuration.String(),
		asyncCollisionPrompt,
	})
	commandTimeout := 3 * options.maxDuration
	if commandTimeout < 2*time.Second {
		commandTimeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	runErr := rootCmd.ExecuteContext(ctx)
	return outputPath, writer.StdoutString(), replayDialer.outboundSnapshot(), runErr
}

func verifyAsyncCollisionAudio(outputPath string, collision, continuation [][]int16) error {
	wavBytes, err := os.ReadFile(outputPath)
	if err != nil {
		return fmt.Errorf("read recorded --audio-out WAV: %w", err)
	}
	rate, got, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		return fmt.Errorf("parse recorded --audio-out WAV: %w", err)
	}
	if rate != 16000 {
		return fmt.Errorf("recorded --audio-out WAV rate = %d, want 16000", rate)
	}
	want := make([]int16, 0, asyncCollisionDeltaSamples*asyncCollisionDeltaCount*2)
	for _, delta := range append(append([][]int16(nil), collision...), continuation...) {
		want = append(want, delta...)
	}
	if len(got) != len(want) {
		return fmt.Errorf("audio sample count = %d, want %d; collision/continuation delta loss or duplication", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("audio sample mismatch at index %d (collision delta span %d), got %d want %d", i, i/asyncCollisionDeltaSamples, got[i], want[i])
		}
	}
	return nil
}

func validateAsyncCollisionExecution(calls []messages.ToolCall, returned []messages.ToolCallResponse) error {
	if len(calls) != 1 {
		return fmt.Errorf("outstanding call %q executed %d times, want exactly once", asyncCollisionCallID, len(calls))
	}
	call := calls[0]
	if call.ID != asyncCollisionCallID || call.Name != asyncCollisionToolName || call.Arguments != asyncCollisionToolArgs {
		return fmt.Errorf("executed call = {id:%q name:%q args:%q}, want {id:%q name:%q args:%q}", call.ID, call.Name, call.Arguments, asyncCollisionCallID, asyncCollisionToolName, asyncCollisionToolArgs)
	}
	if len(returned) != 1 {
		return fmt.Errorf("call %q produced %d local results, want exactly one", asyncCollisionCallID, len(returned))
	}
	if returned[0].ToolCallID != asyncCollisionCallID || returned[0].Content != asyncCollisionResult {
		return fmt.Errorf("local result = {id:%q content:%q}, want original ID %q and sentinel %q", returned[0].ToolCallID, returned[0].Content, asyncCollisionCallID, asyncCollisionResult)
	}
	return nil
}

func validateAsyncCollisionToolDeltas(deltas []messages.StreamMessage) error {
	var toolDeltas []messages.StreamMessage
	for _, delta := range deltas {
		if delta.Role == messages.RoleTool {
			toolDeltas = append(toolDeltas, delta)
		}
	}
	if len(toolDeltas) == 0 {
		return fmt.Errorf("session loop emitted no local RoleTool result for outstanding call %q", asyncCollisionCallID)
	}
	for _, delta := range toolDeltas {
		// The enclosing MESSAGE.START/END delimiters belong to the whole
		// batch and intentionally carry no individual ToolCallID. Content
		// deltas are the per-call correlation evidence.
		switch delta.Type {
		case messages.StreamTypeTextStart, messages.StreamTypeTextDelta, messages.StreamTypeTextEnd:
		default:
			continue
		}
		if delta.ToolCallId != asyncCollisionCallID {
			return fmt.Errorf("local tool-result delta %s carries ToolCallID %q, want %q", delta.Type, delta.ToolCallId, asyncCollisionCallID)
		}
	}
	resultMessages := messages.ReconstructToolMessagesFromDeltas(toolDeltas)
	if len(resultMessages) != 1 || resultMessages[0].ToolCallID != asyncCollisionCallID || resultMessages[0].TextContent() != asyncCollisionResult {
		return fmt.Errorf("reconstructed local result = %v, want exactly one sentinel for %q", resultMessages, asyncCollisionCallID)
	}
	return nil
}

func validateAsyncCollisionTrace(events []string) error {
	required := []string{
		"tool_started",
		"turn_one_completed",
		"later_turn_requested",
		"collision_audio_1_observed",
		"tool_returned",
		"tool_result_observed",
		"collision_audio_2_observed",
		"collision_response_completed",
		"continuation_requested",
	}
	positions := make(map[string]int, len(events))
	counts := make(map[string]int, len(events))
	for i, event := range events {
		counts[event]++
		positions[event] = i
	}
	for _, event := range required {
		if counts[event] != 1 {
			if counts[event] == 0 {
				return fmt.Errorf("causal trace missing %q: %v", event, events)
			}
			return fmt.Errorf("causal trace contains %d %q events, want exactly one: %v", counts[event], event, events)
		}
	}
	// Tool start and completion of the first provider response are independent
	// observer milestones. Both must precede the later request, but their
	// relative scheduling is not part of the contract.
	constraints := [][2]string{
		{"tool_started", "later_turn_requested"},
		{"turn_one_completed", "later_turn_requested"},
		{"later_turn_requested", "collision_audio_1_observed"},
		{"collision_audio_1_observed", "tool_returned"},
		{"tool_returned", "collision_audio_2_observed"},
		{"collision_audio_2_observed", "collision_response_completed"},
		{"tool_returned", "tool_result_observed"},
		{"tool_result_observed", "continuation_requested"},
		{"collision_response_completed", "continuation_requested"},
	}
	for _, constraint := range constraints {
		before, after := constraint[0], constraint[1]
		if positions[before] >= positions[after] {
			return fmt.Errorf("causal trace order = %v, want %q before %q", events, before, after)
		}
	}
	return nil
}

func countAsyncProviderResults(outbound []asyncCollisionOutbound) (int, error) {
	count := 0
	for _, event := range outbound {
		if event.Type != "conversation.item.create" {
			continue
		}
		var payload struct {
			Item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
			} `json:"item"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return 0, fmt.Errorf("decode outbound conversation.item.create: %w", err)
		}
		if payload.Item.Type != "function_call_output" {
			continue
		}
		count++
		if payload.Item.CallID != asyncCollisionCallID || payload.Item.Output != asyncCollisionResult {
			return count, fmt.Errorf("outbound function_call_output = {call_id:%q output:%q}, want original ID %q and sentinel %q", payload.Item.CallID, payload.Item.Output, asyncCollisionCallID, asyncCollisionResult)
		}
	}
	return count, nil
}

func validateAsyncCollisionProviderResult(outbound []asyncCollisionOutbound) error {
	count, err := countAsyncProviderResults(outbound)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("provider-facing result loss for outstanding call %q: observed %d function_call_output events, want exactly one", asyncCollisionCallID, count)
	}
	return nil
}

func validateAsyncCollisionProviderBoundary(events []string, outbound []asyncCollisionOutbound, expectProviderResult bool) error {
	count, err := countAsyncProviderResults(outbound)
	if err != nil {
		return err
	}
	wantCount := 0
	if expectProviderResult {
		wantCount = 1
	}
	if count != wantCount {
		return fmt.Errorf("%s provider-facing result for outstanding call %q: observed %d function_call_output events, want %d", asyncCollisionDisposition, asyncCollisionCallID, count, wantCount)
	}
	positions := make(map[string]int, len(events))
	counts := make(map[string]int, len(events))
	for index, event := range events {
		positions[event] = index
		counts[event]++
	}
	if counts["provider_result_sent"] != wantCount {
		return fmt.Errorf("provider-facing result was observed %d times in the causal trace, want %d", counts["provider_result_sent"], wantCount)
	}
	if !expectProviderResult {
		return nil
	}
	if positions["collision_response_completed"] >= positions["provider_result_sent"] || positions["provider_result_sent"] >= positions["continuation_requested"] {
		return fmt.Errorf("%s provider result boundary is out of order: %v", asyncCollisionDisposition, events)
	}
	return nil
}

func validateAsyncCollisionContinuation(outbound []asyncCollisionOutbound, expectedInputAudio []byte, expectProviderResult bool) error {
	userTurnIndices := make([]int, 0, 2)
	responseCreateIndices := make([]int, 0, 3)
	providerResultIndices := make([]int, 0, 1)
	inputAppendIndex, inputCommitIndex := -1, -1
	providerResultCount, err := countAsyncProviderResults(outbound)
	if err != nil {
		return err
	}
	wantProviderResultCount := 0
	if expectProviderResult {
		wantProviderResultCount = 1
	}
	if providerResultCount != wantProviderResultCount {
		return fmt.Errorf("%s provider result for outstanding call %q: observed %d function_call_output events, want %d", asyncCollisionDisposition, asyncCollisionCallID, providerResultCount, wantProviderResultCount)
	}
	for index, event := range outbound {
		switch event.Type {
		case "conversation.item.create":
			var payload struct {
				Item struct {
					Type   string `json:"type"`
					CallID string `json:"call_id"`
				} `json:"item"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return fmt.Errorf("decode outbound continuation item: %w", err)
			}
			if payload.Item.Type == "message" {
				userTurnIndices = append(userTurnIndices, index)
			} else if payload.Item.Type == "function_call_output" && payload.Item.CallID == asyncCollisionCallID {
				providerResultIndices = append(providerResultIndices, index)
			}
		case "response.create":
			responseCreateIndices = append(responseCreateIndices, index)
		case "input_audio_buffer.append":
			if inputAppendIndex >= 0 {
				return fmt.Errorf("provider exchange contains multiple input_audio_buffer.append events")
			}
			if err := validateAsyncOutboundInputAudio(event.Payload, expectedInputAudio); err != nil {
				return err
			}
			inputAppendIndex = index
		case "input_audio_buffer.commit":
			if inputCommitIndex >= 0 {
				return fmt.Errorf("provider exchange contains multiple input_audio_buffer.commit events")
			}
			inputCommitIndex = index
		}
	}
	if len(userTurnIndices) != 2 {
		return fmt.Errorf("provider exchange contains %d text user turns, want one later turn and one result-driven continuation", len(userTurnIndices))
	}
	if len(responseCreateIndices) != 3 {
		return fmt.Errorf("provider exchange contains %d response.create events, want the initial audio turn, later collision turn, and result-driven continuation", len(responseCreateIndices))
	}
	if inputAppendIndex < 0 || inputCommitIndex < 0 {
		return fmt.Errorf("provider exchange is missing the later-turn input audio append/commit boundary")
	}
	if userTurnIndices[0] >= responseCreateIndices[0] || responseCreateIndices[0] >= inputAppendIndex || inputAppendIndex >= inputCommitIndex || inputCommitIndex >= responseCreateIndices[1] {
		return fmt.Errorf("initial text/later-turn audio boundary is out of order: user=%d initial response.create=%d append=%d commit=%d later response.create=%d", userTurnIndices[0], responseCreateIndices[0], inputAppendIndex, inputCommitIndex, responseCreateIndices[1])
	}
	if expectProviderResult {
		if len(providerResultIndices) != 1 {
			return fmt.Errorf("%s provider result for %q was correlated %d times, want exactly one", asyncCollisionDisposition, asyncCollisionCallID, len(providerResultIndices))
		}
		if providerResultIndices[0] <= responseCreateIndices[1] || providerResultIndices[0] >= userTurnIndices[1] {
			return fmt.Errorf("%s provider result for %q was sent after the result-driven continuation user turn", asyncCollisionDisposition, asyncCollisionCallID)
		}
	} else if len(providerResultIndices) != 0 {
		return fmt.Errorf("result-loss control still carried %d provider results for %q", len(providerResultIndices), asyncCollisionCallID)
	}
	if responseCreateIndices[1] >= userTurnIndices[1] || userTurnIndices[1] >= responseCreateIndices[2] {
		return fmt.Errorf("result-driven continuation is out of order: collision response.create=%d continuation user=%d continuation response.create=%d", responseCreateIndices[1], userTurnIndices[1], responseCreateIndices[2])
	}
	return nil
}

func validateAsyncCollisionTerminal(sessionOutput string, runErr error) error {
	want := "[session closed: " + asyncCollisionCloseReason + "]"
	if strings.Contains(sessionOutput, want) {
		return nil
	}
	if runErr != nil {
		return fmt.Errorf("required terminal event %q was not observed before bounded CLI run ended (session output %q): %w", want, sessionOutput, runErr)
	}
	return fmt.Errorf("required terminal event %q was not observed (session output %q)", want, sessionOutput)
}

// validateAsyncCollisionRun is the shared verifier for the positive path and
// every control. A control can disable only the assertion it intentionally
// damages; the other runtime, correlation, continuation, and terminal checks
// remain identical.
func validateAsyncCollisionRun(run asyncCollisionRunResult, collision, continuation [][]int16, verifyAudio, expectProviderResult bool) error {
	if err := validateAsyncCollisionTerminal(run.sessionOutput, run.runErr); err != nil {
		return err
	}
	if run.runErr != nil {
		return fmt.Errorf("agent session async collision replay failed: %w", run.runErr)
	}
	if verifyAudio {
		if err := verifyAsyncCollisionAudio(run.outputPath, collision, continuation); err != nil {
			return err
		}
	}
	calls, returned := run.executor.snapshot()
	if err := validateAsyncCollisionExecution(calls, returned); err != nil {
		return err
	}
	if err := validateAsyncCollisionToolDeltas(run.observer.snapshot()); err != nil {
		return err
	}
	if err := validateAsyncCollisionTrace(run.trace.snapshot()); err != nil {
		return err
	}
	if err := validateAsyncCollisionProviderBoundary(run.trace.snapshot(), run.outbound, expectProviderResult); err != nil {
		return err
	}
	if err := validateAsyncCollisionContinuation(run.outbound, asyncCollisionInputAudio(), expectProviderResult); err != nil {
		return err
	}
	return nil
}

func cloneAsyncCollisionDeltas(deltas [][]int16) [][]int16 {
	clone := make([][]int16, len(deltas))
	for i, delta := range deltas {
		clone[i] = append([]int16(nil), delta...)
	}
	return clone
}

// TestSessionAsyncToolResultInterruptsSpeechThroughCLI is the first-story
// positive collision proof. It validates every observable, including the
// exact-one provider-facing result and its original call ID.
func TestSessionAsyncToolResultInterruptsSpeechThroughCLI(t *testing.T) {
	collision, continuation := asyncCollisionAudio(t)
	run := runAsyncCollisionScenario(t, collision, collision, continuation, asyncCollisionRunOptions{})
	if err := validateAsyncCollisionRun(run, collision, continuation, true, true); err != nil {
		calls, returned := run.executor.snapshot()
		t.Logf("async collision run: err=%v trace=%v outbound=%v calls=%+v returned=%+v deltas=%v", run.runErr, run.trace.snapshot(), summarizeAsyncCollisionOutbound(run.outbound), calls, returned, summarizeAsyncCollisionDeltas(run.observer.snapshot()))
		t.Fatal(err)
	}
	if err := validateAsyncCollisionProviderResult(run.outbound); err != nil {
		t.Fatal(err)
	}
	t.Logf("provider-facing result delivered exactly once for %q", asyncCollisionCallID)
}

func summarizeAsyncCollisionOutbound(outbound []asyncCollisionOutbound) []string {
	types := make([]string, len(outbound))
	for i, event := range outbound {
		types[i] = event.Type
	}
	return types
}

func summarizeAsyncCollisionDeltas(deltas []messages.StreamMessage) []string {
	types := make([]string, len(deltas))
	for i, delta := range deltas {
		types[i] = string(delta.Type)
	}
	return types
}

// TestSessionAsyncToolResultProviderResultLossFailsVerifier is the result-loss
// control. It keeps the collision, audio, continuation, and terminal path
// healthy, while suppressing only a provider-facing function_call_output at
// the replay transport boundary. The shared verifier still checks every
// unrelated outcome before the targeted result-loss assertion names the call.
func TestSessionAsyncToolResultProviderResultLossFailsVerifier(t *testing.T) {
	collision, continuation := asyncCollisionAudio(t)
	run := runAsyncCollisionScenario(t, collision, collision, continuation, asyncCollisionRunOptions{
		dropProviderResult: true,
	})
	if err := validateAsyncCollisionRun(run, collision, continuation, true, false); err != nil {
		t.Fatalf("result-loss control changed an unrelated collision outcome: %v\ntrace=%v outbound=%v", err, run.trace.snapshot(), summarizeAsyncCollisionOutbound(run.outbound))
	}

	assertionErr := validateAsyncCollisionProviderResult(run.outbound)
	if assertionErr == nil {
		t.Fatal("result-loss control was not detected by the provider-result verifier")
	}
	if !strings.Contains(assertionErr.Error(), asyncCollisionCallID) {
		t.Fatalf("result-loss diagnostic %q does not name outstanding call %q", assertionErr, asyncCollisionCallID)
	}
}

// TestSessionAsyncToolResultAudioDamageFailsVerifier mutates one collision
// delta while leaving transport completion and the continuation untouched.
// The shared PCM verifier must identify the affected collision span.
func TestSessionAsyncToolResultAudioDamageFailsVerifier(t *testing.T) {
	collision, continuation := asyncCollisionAudio(t)
	damaged := cloneAsyncCollisionDeltas(collision)
	damaged[1][0] ^= 1
	run := runAsyncCollisionScenario(t, damaged, collision, continuation, asyncCollisionRunOptions{})
	if err := validateAsyncCollisionRun(run, collision, continuation, false, true); err != nil {
		t.Fatalf("audio-damage control changed an unrelated runtime outcome: %v\ntrace=%v outbound=%v", err, run.trace.snapshot(), summarizeAsyncCollisionOutbound(run.outbound))
	}

	assertionErr := verifyAsyncCollisionAudio(run.outputPath, collision, continuation)
	if assertionErr == nil {
		t.Fatal("audio-damage control was not detected by the byte-exact PCM verifier")
	}
	if !strings.Contains(assertionErr.Error(), "collision delta span 1") {
		t.Fatalf("audio-damage diagnostic %q does not identify collision delta 1", assertionErr)
	}
}

// TestSessionAsyncToolResultMissingTerminalFailsBounded is the wedge control.
// It withholds only the fixture's terminal event; the bounded CLI must return
// and the shared verifier must report the missing exact terminal boundary.
func TestSessionAsyncToolResultMissingTerminalFailsBounded(t *testing.T) {
	collision, continuation := asyncCollisionAudio(t)
	run := runAsyncCollisionScenario(t, collision, collision, continuation, asyncCollisionRunOptions{
		maxDuration:      asyncCollisionControlMaxDuration,
		withholdTerminal: true,
	})
	assertionErr := validateAsyncCollisionRun(run, collision, continuation, true, true)
	if assertionErr == nil {
		t.Fatal("missing-terminal control unexpectedly passed the terminal verifier")
	}
	if !strings.Contains(assertionErr.Error(), "required terminal event") && !strings.Contains(assertionErr.Error(), "bounded CLI run") {
		t.Fatalf("missing-terminal diagnostic %q does not identify terminal/timeout failure", assertionErr)
	}
}
