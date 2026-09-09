package strict

import (
	"context"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

type replayState struct {
	mu           sync.Mutex
	consumed     int
	dialed       bool
	messageTypes []int
}

func (s *replayState) expectedTypeLocked() int {
	if s.consumed >= len(s.messageTypes) {
		return 0
	}
	return s.messageTypes[s.consumed]
}

type trackingDialer struct {
	inner transport.Dialer
	state *replayState
}

func (d *trackingDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	d.state.mu.Lock()
	if d.state.dialed {
		err := fmt.Errorf("%w: replay bundle permits one session connection", replay.ErrBundleMismatch)
		d.state.mu.Unlock()
		return nil, err
	}
	d.state.dialed = true
	d.state.mu.Unlock()
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		return nil, err
	}
	return &trackingConn{inner: conn, state: d.state}, nil
}

type trackingConn struct {
	inner transport.Conn
	state *replayState
}

func (c *trackingConn) ReadMessage() (int, []byte, error) {
	messageType, payload, err := c.inner.ReadMessage()
	if err != nil {
		return messageType, payload, err
	}
	c.state.mu.Lock()
	if expected := c.state.expectedTypeLocked(); expected != 0 && messageType != expected {
		if expected == 2 && messageType == 1 {
			messageType = expected
		} else {
			err := fmt.Errorf("%w: received message type %d, expected %d", replay.ErrBundleMismatch, messageType, expected)
			c.state.mu.Unlock()
			return messageType, payload, err
		}
	}
	c.state.consumed++
	c.state.mu.Unlock()
	return messageType, payload, nil
}

func (c *trackingConn) WriteMessage(messageType int, payload []byte) error {
	c.state.mu.Lock()
	expected := c.state.expectedTypeLocked()
	c.state.mu.Unlock()
	if expected != 0 && messageType != expected {
		err := fmt.Errorf("%w: sent message type %d, expected %d", replay.ErrBundleMismatch, messageType, expected)
		return err
	}
	if err := c.inner.WriteMessage(messageType, payload); err != nil {
		return fmt.Errorf("%w: %w", replay.ErrBundleMismatch, err)
	}
	c.state.mu.Lock()
	c.state.consumed++
	c.state.mu.Unlock()
	return nil
}

func (c *trackingConn) Close() error {
	return c.inner.Close()
}

// prepared is the private implementation of the public StrictPrepared view.
// Keeping the completion witness and its accounting here means a caller can
// observe prepared dependencies but cannot construct a value that validates
// arbitrary caller-owned evidence.
type prepared struct {
	capture      gwtesting.SessionCapture
	dialer       transport.Dialer
	toolExecutor messages.ToolExecutor
	audio        *recording.Replay
	clock        clock.Scheduler
	scope        replay.StrictEvidenceScope
	wireEvents   int
	toolCalls    int
	completion   *strictCompletion
}

type strictCompletion struct {
	mu            sync.Mutex
	expectedWire  int
	consumedWire  int
	expectedTools int
	consumedTools int
	dialed        bool
	invalid       bool
	err           error
}

type strictToolCallCounter interface {
	ExpectedToolCalls() int
}

func newPrepared(
	capture gwtesting.SessionCapture,
	dialer transport.Dialer,
	toolExecutor messages.ToolExecutor,
	audio *recording.Replay,
	scheduler clock.Scheduler,
	scope replay.StrictEvidenceScope,
) replay.StrictPrepared {
	expectedTools, hasToolCount := 0, false
	if counter, ok := toolExecutor.(strictToolCallCounter); ok {
		expectedTools = counter.ExpectedToolCalls()
		hasToolCount = expectedTools >= 0
	}
	completion := &strictCompletion{
		expectedWire:  len(capture.Records),
		expectedTools: expectedTools,
		invalid:       len(capture.Records) == 0 || toolExecutor == nil || !hasToolCount,
	}
	return &prepared{
		capture:      capture,
		dialer:       &completionDialer{inner: dialer, state: completion},
		toolExecutor: &completionToolExecutor{inner: toolExecutor, state: completion},
		audio:        audio,
		clock:        scheduler,
		scope:        scope,
		wireEvents:   len(capture.Records),
		toolCalls:    expectedTools,
		completion:   completion,
	}
}

func (p *prepared) Capture() gwtesting.SessionCapture {
	if p == nil {
		return gwtesting.SessionCapture{}
	}
	return p.capture
}

func (p *prepared) Dialer() transport.Dialer {
	if p == nil {
		return nil
	}
	return p.dialer
}

func (p *prepared) ToolExecutor() messages.ToolExecutor {
	if p == nil {
		return nil
	}
	return p.toolExecutor
}

func (p *prepared) Audio() *recording.Replay {
	if p == nil {
		return nil
	}
	return p.audio
}

func (p *prepared) Clock() clock.Scheduler {
	if p == nil {
		return nil
	}
	return p.clock
}

func (p *prepared) Scope() replay.StrictEvidenceScope {
	if p == nil {
		return replay.StrictEvidenceScope{}
	}
	return p.scope
}

func (p *prepared) WireEvents() int {
	if p == nil {
		return 0
	}
	return p.wireEvents
}

func (p *prepared) ToolCalls() int {
	if p == nil {
		return 0
	}
	return p.toolCalls
}

func (p *prepared) ValidateComplete() error {
	if p == nil {
		return replay.ErrBundleIncomplete
	}
	return p.completion.validate()
}

func (p *prepared) Close() error { return p.ValidateComplete() }

func (c *strictCompletion) beginDial() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dialed {
		c.err = fmt.Errorf("%w: replay bundle permits one session connection", replay.ErrBundleMismatch)
		return c.err
	}
	c.dialed = true
	return nil
}

func (c *strictCompletion) fail(err error) {
	if c == nil || err == nil {
		return
	}
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
}

func (c *strictCompletion) failWire(err error) {
	if c == nil || err == nil {
		return
	}
	c.mu.Lock()
	if c.err == nil && c.consumedWire < c.expectedWire {
		c.err = fmt.Errorf("%w: %w", replay.ErrBundleIncomplete, err)
	}
	c.mu.Unlock()
}

func (c *strictCompletion) markWire() {
	c.mu.Lock()
	c.consumedWire++
	c.mu.Unlock()
}

func (c *strictCompletion) markTool() {
	c.mu.Lock()
	c.consumedTools++
	c.mu.Unlock()
}

func (c *strictCompletion) validate() error {
	if c == nil {
		return replay.ErrBundleIncomplete
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if c.invalid || !c.dialed || c.consumedWire != c.expectedWire || c.consumedTools != c.expectedTools {
		return fmt.Errorf("%w: provider wire consumed %d/%d and tools consumed %d/%d", replay.ErrBundleIncomplete, c.consumedWire, c.expectedWire, c.consumedTools, c.expectedTools)
	}
	return nil
}

type completionDialer struct {
	inner transport.Dialer
	state *strictCompletion
}

func (d *completionDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.state == nil || d.inner == nil {
		return nil, fmt.Errorf("%w: replay dialer is unavailable", replay.ErrBundleIncomplete)
	}
	if err := d.state.beginDial(); err != nil {
		return nil, err
	}
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		d.state.fail(err)
		return nil, err
	}
	if conn == nil {
		err := fmt.Errorf("%w: replay dialer returned a nil connection", replay.ErrBundleIncomplete)
		d.state.fail(err)
		return nil, err
	}
	return &completionConn{inner: conn, state: d.state}, nil
}

type completionConn struct {
	inner transport.Conn
	state *strictCompletion
}

func (c *completionConn) ReadMessage() (int, []byte, error) {
	if c == nil || c.inner == nil {
		err := fmt.Errorf("%w: replay connection is unavailable", replay.ErrBundleIncomplete)
		if c != nil {
			c.state.fail(err)
		}
		return 0, nil, err
	}
	messageType, payload, err := c.inner.ReadMessage()
	if err == nil {
		c.state.markWire()
	} else {
		c.state.failWire(err)
	}
	return messageType, payload, err
}

func (c *completionConn) WriteMessage(messageType int, payload []byte) error {
	if c == nil || c.inner == nil {
		err := fmt.Errorf("%w: replay connection is unavailable", replay.ErrBundleIncomplete)
		if c != nil {
			c.state.fail(err)
		}
		return err
	}
	err := c.inner.WriteMessage(messageType, payload)
	if err == nil {
		c.state.markWire()
	} else {
		c.state.fail(err)
	}
	return err
}

func (c *completionConn) Close() error {
	if c == nil || c.inner == nil {
		return nil
	}
	err := c.inner.Close()
	c.state.fail(err)
	return err
}

type completionToolExecutor struct {
	inner messages.ToolExecutor
	state *strictCompletion
}

func (e *completionToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if e == nil || e.inner == nil {
		err := fmt.Errorf("%w: replay tool executor is unavailable", replay.ErrToolFailure)
		if e != nil {
			e.state.fail(err)
		}
		return messages.ToolCallResponse{}, err
	}
	response, err := e.inner.Execute(ctx, call)
	if err == nil {
		e.state.markTool()
	}
	return response, err
}

var _ replay.StrictPrepared = (*prepared)(nil)
var _ transport.Dialer = (*completionDialer)(nil)
var _ transport.Conn = (*completionConn)(nil)
var _ messages.ToolExecutor = (*completionToolExecutor)(nil)
