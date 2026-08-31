package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

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
	continuationRequested     <-chan struct{}
	continuationCompleted     <-chan struct{}
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
			// The result-driven response is the only response eligible after
			// the first tool-call response. A scheduled audio request is not
			// allowed to release a provider phase before this continuation.
			waitFor = c.control.continuationRequested
			phase = c.groups.continuation
		case 3:
			// The scheduled audio is admitted only after the grounded
			// continuation reaches MESSAGE.END. Once admitted, wait for its
			// own response.create write before releasing the server response;
			// otherwise the independent provider read/write loops can publish
			// collision audio before the client has requested that response.
			waitFor = c.control.signals.laterResponseReady
			phase = c.groups.collisionHead
		case 4:
			phase = c.groups.collisionTail
		case 5:
			if !c.control.withholdTerminal {
				waitFor = c.control.collisionResponseComplete
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
			// The loss control acts at the provider-facing transport boundary. It
			// accepts the write but removes only the correlated result from the
			// observed exchange, leaving the continuation request observable.
			if c.control.dropProviderResult {
				return nil
			}
			c.control.trace.record("provider_result_sent")
		}
	case "input_audio_buffer.append":
		select {
		case <-c.control.continuationCompleted:
		default:
			return errors.New("scheduled audio reached the provider before the grounded continuation completed")
		}
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
			c.control.trace.record("continuation_requested")
			c.control.signals.markContinuation()
		} else if count == 3 {
			c.control.trace.record("later_turn_requested")
			c.control.signals.markLaterResponse()
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
