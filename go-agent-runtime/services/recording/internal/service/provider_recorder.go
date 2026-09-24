package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// providerRecordingDialer owns the mutable WebSocket recording state for one
// provider capture. The bounded sink owns persistence and admission budgets.
type providerRecordingDialer struct {
	inner   transport.Dialer
	clock   clock.Source
	startAt time.Time
	capture gatewaytesting.SessionCapture
	sink    recording.ProviderCaptureSink

	mu        sync.Mutex
	captureMu sync.RWMutex
	sequence  int
	sinkErr   error
}

var _ transport.Dialer = (*providerRecordingDialer)(nil)
var _ recording.Writer = (*providerRecordingDialer)(nil)

func newProviderRecordingDialer(inner transport.Dialer, provider, model string, sink recording.ProviderCaptureSink, source clock.Source) (*providerRecordingDialer, error) {
	if inner == nil {
		return nil, errors.New("provider recording requires an inner dialer")
	}
	if sink == nil {
		return nil, errors.New("provider recording requires a capture sink")
	}
	if source == nil {
		source = clock.Real{}
	}
	startAt := source.Now()
	return &providerRecordingDialer{
		inner:   inner,
		clock:   source,
		startAt: startAt,
		capture: gatewaytesting.SessionCapture{
			Version:  gatewaytesting.SessionCaptureVersion,
			Provider: gatewaytesting.SessionProviderMetadata{Name: provider, Model: model},
			Session:  gatewaytesting.SessionMetadata{StartedAtUTC: startAt.UTC().Format(time.RFC3339Nano)},
			Records:  make([]gatewaytesting.CapturedSessionEvent, 0),
		},
		sink: sink,
	}, nil
}

func (d *providerRecordingDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.inner == nil {
		return nil, errors.New("provider recording dialer is unavailable")
	}
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, errors.New("provider dialer returned a nil connection")
	}
	return &providerRecordingConn{inner: conn, recorder: d}, nil
}

func (d *providerRecordingDialer) FlushToFile(path string) error {
	if d == nil || d.sink == nil {
		return errors.New("provider recording dialer is unavailable")
	}
	d.captureMu.Lock()
	defer d.captureMu.Unlock()

	d.mu.Lock()
	sinkErr := d.sinkErr
	capture := d.capture
	d.mu.Unlock()
	if sinkErr != nil {
		return errors.Join(sinkErr, d.sink.Abort())
	}
	if err := d.sink.FlushToFile(path, capture); err != nil {
		return errors.Join(fmt.Errorf("flush provider capture sink: %w", err), d.sink.Abort())
	}
	return nil
}

func (d *providerRecordingDialer) record(direction gatewaytesting.SessionEventDirection, payload []byte) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.sequence++
	sequence := d.sequence
	if d.sinkErr != nil {
		return sequence
	}
	event := gatewaytesting.CapturedSessionEvent{
		Sequence:    sequence,
		Direction:   direction,
		TimestampMs: d.clock.Now().Sub(d.startAt).Milliseconds(),
		Type:        providerPayloadType(payload),
		PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
		Payload:     payload,
	}
	if err := d.sink.Append(event); err != nil && d.sinkErr == nil {
		d.sinkErr = err
	}
	return sequence
}

func (d *providerRecordingDialer) commit(sequence int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sinkErr == nil {
		if err := d.sink.Commit(sequence); err != nil {
			d.sinkErr = err
		}
	}
}

func (d *providerRecordingDialer) discard(sequence int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sinkErr == nil {
		if err := d.sink.Discard(sequence); err != nil {
			d.sinkErr = err
		}
	}
}

func providerPayloadType(payload []byte) string {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Type == "" {
		return "websocket.message"
	}
	return envelope.Type
}

type providerRecordingConn struct {
	inner    transport.Conn
	recorder *providerRecordingDialer
}

var _ transport.Conn = (*providerRecordingConn)(nil)

func (c *providerRecordingConn) ReadMessage() (int, []byte, error) {
	c.recorder.captureMu.RLock()
	defer c.recorder.captureMu.RUnlock()

	messageType, payload, err := c.inner.ReadMessage()
	if err == nil {
		sequence := c.recorder.record(gatewaytesting.DirectionServerToClient, payload)
		c.recorder.commit(sequence)
	}
	return messageType, payload, err
}

func (c *providerRecordingConn) WriteMessage(messageType int, payload []byte) error {
	c.recorder.captureMu.RLock()
	defer c.recorder.captureMu.RUnlock()

	// Reserve before the transport call so a synchronous response cannot be
	// ordered ahead of the outbound event that caused it.
	sequence := c.recorder.record(gatewaytesting.DirectionClientToServer, payload)
	if err := c.inner.WriteMessage(messageType, payload); err != nil {
		c.recorder.discard(sequence)
		return err
	}
	c.recorder.commit(sequence)
	return nil
}

func (c *providerRecordingConn) Close() error { return c.inner.Close() }
