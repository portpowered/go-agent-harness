package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

type livePrepared struct {
	inspection replay.CaptureInspection
	dialer     *testing.ReplayWebSocketDialer
	config     []byte
	active     transport.Dialer
	mu         sync.Mutex
	closed     bool
}

func (s *Service) PrepareLive(ctx context.Context, request replay.LiveRequest) (replay.LivePrepared, error) {
	if err := replayContextError(ctx); err != nil {
		return nil, err
	}
	inspection, err := s.InspectCapture(ctx, request.SourcePath)
	if err != nil {
		return nil, err
	}
	if !inspection.IsRealtime() {
		return nil, fmt.Errorf("replay capture %s is not a realtime session", request.SourcePath)
	}
	loaded, err := loadReplayCapture(ctx, inspection.CapturePath)
	if err != nil {
		return nil, fmt.Errorf("prepare live replay %s: %w", request.SourcePath, err)
	}
	if inspection.LivePlan != nil {
		plan := *inspection.LivePlan
		plan.MaxDuration = replayMaxDuration(loaded.Capture.Records, request.Timing)
		inspection.LivePlan = &plan
	}
	config, err := initialSessionUpdatePayload(request.SourcePath, loaded.Capture.Records)
	if err != nil {
		return nil, err
	}
	options := []testing.ReplayWebSocketDialerOption(nil)
	if request.Timing == "realtime" {
		options = append(options, testing.WithRecordedSessionTiming())
	}
	dialer, err := testing.NewReplayWebSocketDialerFromCapture(loaded.Capture, options...)
	if err != nil {
		return nil, fmt.Errorf("prepare live replay %s: %w", request.SourcePath, err)
	}
	return &livePrepared{inspection: inspection, dialer: dialer, config: config, active: dialer}, nil
}

func replayMaxDuration(records []testing.CapturedSessionEvent, timing session.LiveReplayTiming) time.Duration {
	const completionGrace = 3 * time.Second
	if timing != session.LiveReplayTimingRealtime || len(records) < 2 {
		return completionGrace
	}
	first, last := records[0].TimestampMs, records[len(records)-1].TimestampMs
	if last <= first {
		return completionGrace
	}
	return time.Duration(last-first)*time.Millisecond + completionGrace
}

func initialSessionUpdatePayload(path string, records []testing.CapturedSessionEvent) ([]byte, error) {
	for _, record := range records {
		if record.Direction != testing.DirectionClientToServer || record.Type != replaySessionUpdate {
			continue
		}
		payload := replayRecordPayload(record)
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Type != replaySessionUpdate {
			if err == nil {
				err = fmt.Errorf("payload type %q", envelope.Type)
			}
			return nil, fmt.Errorf("replay capture %s: invalid initial %s at sequence %d: %w", path, replaySessionUpdate, record.Sequence, err)
		}
		return append([]byte(nil), payload...), nil
	}
	return nil, fmt.Errorf("replay capture %s: missing initial %s", path, replaySessionUpdate)
}

func (p *livePrepared) Inspection() replay.CaptureInspection {
	if p == nil {
		return replay.CaptureInspection{}
	}
	return p.inspection
}

func (p *livePrepared) WrapDialer(inner transport.Dialer) transport.Dialer {
	if p == nil || p.dialer == nil {
		return nil
	}
	if inner == nil {
		inner = p.dialer
	}
	p.mu.Lock()
	p.active = inner
	p.mu.Unlock()
	var pacer testing.ReplayOutboundPacer
	if candidate, ok := inner.(testing.ReplayOutboundPacer); ok {
		pacer = candidate
	}
	return &initialSessionUpdateDialer{inner: inner, payload: append([]byte(nil), p.config...), waitForNextOutbound: pacer}
}

func (*livePrepared) WrapInferencer(inner messages.SessionInferencer) messages.SessionInferencer {
	if inner == nil {
		return nil
	}
	return replayInferencer{inner: inner}
}

func (p *livePrepared) Done() <-chan struct{} {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	active := p.active
	p.mu.Unlock()
	if lifecycle, ok := active.(interface{ Done() <-chan struct{} }); ok {
		return lifecycle.Done()
	}
	return nil
}

func (p *livePrepared) Err() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	active := p.active
	p.mu.Unlock()
	if lifecycle, ok := active.(interface{ Err() error }); ok {
		return lifecycle.Err()
	}
	return nil
}

func (p *livePrepared) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	active := p.active
	p.closed = true
	p.mu.Unlock()
	if closer, ok := active.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

type initialSessionUpdateDialer struct {
	inner               transport.Dialer
	payload             []byte
	waitForNextOutbound testing.ReplayOutboundPacer
}

func (d *initialSessionUpdateDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.inner == nil {
		return nil, fmt.Errorf("replay dialer is unavailable")
	}
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		return nil, err
	}
	return &initialSessionUpdateConn{inner: conn, payload: append([]byte(nil), d.payload...), waitForNextOutbound: d.waitForNextOutbound}, nil
}

type initialSessionUpdateConn struct {
	inner               transport.Conn
	payload             []byte
	mu                  sync.Mutex
	handshake           bool
	waitForNextOutbound testing.ReplayOutboundPacer
}

func (c *initialSessionUpdateConn) ReadMessage() (int, []byte, error) { return c.inner.ReadMessage() }

func (c *initialSessionUpdateConn) WriteMessage(messageType int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waitForNextOutbound != nil {
		if err := c.waitForNextOutbound.WaitForNextOutbound(); err != nil {
			return err
		}
	}
	if !c.handshake {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return fmt.Errorf("replay initial event is not valid JSON: %w", err)
		}
		if envelope.Type != replaySessionUpdate {
			return fmt.Errorf("replay provider expected initial %s, got %q", replaySessionUpdate, envelope.Type)
		}
		payload = c.payload
		c.handshake = true
	}
	return c.inner.WriteMessage(messageType, payload)
}

func (c *initialSessionUpdateConn) Close() error { return c.inner.Close() }

type replayInferencer struct{ inner messages.SessionInferencer }

func (i replayInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	s, err := i.inner.ConnectSession(ctx)
	if err != nil || s == nil {
		return s, err
	}
	return replaySession{Session: s}, nil
}

type replaySession struct{ messages.Session }

func (s replaySession) Send(ctx context.Context, message messages.StreamMessage) bool {
	return messages.SendSessionWithOutcome(ctx, s.Session, message).OK()
}
