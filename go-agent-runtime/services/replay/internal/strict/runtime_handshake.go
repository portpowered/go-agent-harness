package strict

import (
	"encoding/json"
	"fmt"

	publicreplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func initialSessionUpdate(capture gwtesting.SessionCapture) ([]byte, error) {
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionClientToServer && record.Type == "session.update" {
			return append([]byte(nil), record.Payload...), nil
		}
	}
	return nil, fmt.Errorf("%w: initial session.update is missing", publicreplay.ErrBundleIncomplete)
}

type initialUpdateDialer struct {
	inner   transport.Dialer
	payload []byte
}

func (d *initialUpdateDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		return nil, err
	}
	return &initialUpdateConn{inner: conn, payload: append([]byte(nil), d.payload...)}, nil
}

type initialUpdateConn struct {
	inner   transport.Conn
	payload []byte
	written bool
}

func (c *initialUpdateConn) ReadMessage() (int, []byte, error) { return c.inner.ReadMessage() }

func (c *initialUpdateConn) WriteMessage(messageType int, payload []byte) error {
	if !c.written {
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(payload, &envelope) == nil && envelope.Type == "session.update" {
			payload = c.payload
		}
		c.written = true
	}
	return c.inner.WriteMessage(messageType, payload)
}

func (c *initialUpdateConn) Close() error { return c.inner.Close() }
