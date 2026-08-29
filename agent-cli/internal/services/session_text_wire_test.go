package services_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// scriptedRealtimeDialer is a hermetic OpenAI Realtime server: it answers
// session.update with session.created and response.create with a completed
// response, so the full session loop runs without any network access.
type scriptedRealtimeDialer struct {
	mu      sync.Mutex
	pending [][]byte
	closed  bool
}

type scriptedRealtimeConn struct {
	dialer *scriptedRealtimeDialer
}

func (d *scriptedRealtimeDialer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	return &scriptedRealtimeConn{dialer: d}, nil
}

func (d *scriptedRealtimeDialer) enqueue(payload []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending = append(d.pending, payload)
}

func (c *scriptedRealtimeConn) WriteMessage(_ int, payload []byte) error {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	switch envelope.Type {
	case "session.update":
		c.dialer.enqueue([]byte(`{"type":"session.created","session":{"id":"sess_wire","model":"gpt-realtime-2.1-mini"}}`))
	case "response.create":
		c.dialer.enqueue([]byte(`{"type":"response.created"}`))
		c.dialer.enqueue([]byte(`{"type":"response.output_audio_transcript.delta","delta":"ok"}`))
		c.dialer.enqueue([]byte(`{"type":"response.done"}`))
	case "response.cancel":
		c.dialer.enqueue([]byte(`{"type":"response.done"}`))
	}
	return nil
}

func (c *scriptedRealtimeConn) ReadMessage() (int, []byte, error) {
	for {
		c.dialer.mu.Lock()
		if c.dialer.closed && len(c.dialer.pending) == 0 {
			c.dialer.mu.Unlock()
			return 0, nil, io.EOF
		}
		if len(c.dialer.pending) > 0 {
			payload := c.dialer.pending[0]
			c.dialer.pending = c.dialer.pending[1:]
			c.dialer.mu.Unlock()
			return 1, payload, nil
		}
		c.dialer.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
}

func (c *scriptedRealtimeConn) Close() error {
	c.dialer.mu.Lock()
	defer c.dialer.mu.Unlock()
	c.dialer.closed = true
	return nil
}

func TestWireCapturePromptReachesConversationItemCreate(t *testing.T) {
	const prompt = "Say hello in one short sentence."
	recorder := gwtesting.NewRecordingWebSocketDialer(&scriptedRealtimeDialer{}, "openai", "gpt-realtime-2.1-mini")

	opts := services.SessionRunOptions{
		Provider:        "openai",
		Model:           "gpt-realtime-2.1-mini",
		APIKey:          "test-key",
		RecordPath:      t.TempDir() + "/capture.json",
		ConfigDir:       t.TempDir(),
		WebSocketDialer: recorder,
	}
	out := &bytes.Buffer{}
	err := services.RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(
		context.Background(), out, opts, "", 0,
		services.SessionTextSeed{Value: prompt, Present: true}, "",
	)
	if err != nil {
		t.Fatalf("session run: %v", err)
	}

	capture := recorder.Capture()
	var itemCreates []string
	for _, record := range capture.Records {
		payload := string(record.Payload)
		if strings.Contains(payload, "agent-cli-session-text-seed") {
			t.Fatalf("sentinel leaked to the wire in frame %q: %s", record.Type, payload)
		}
		if record.Direction == gwtesting.DirectionClientToServer && record.Type == "conversation.item.create" {
			itemCreates = append(itemCreates, payload)
		}
	}
	if len(itemCreates) == 0 {
		t.Fatalf("no conversation.item.create captured; frames: %+v", capture.Records)
	}
	found := false
	for _, payload := range itemCreates {
		var decoded struct {
			Item struct {
				Content []struct {
					Text string `json:"text"`
					Type string `json:"type"`
				} `json:"content"`
			} `json:"item"`
		}
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			t.Fatalf("decode conversation.item.create %s: %v", payload, err)
		}
		for _, content := range decoded.Item.Content {
			if content.Text == prompt {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("prompt %q not found in conversation.item.create payloads: %v", prompt, itemCreates)
	}
}

func TestWireCapturePromptReachesWireWithDurationBound(t *testing.T) {
	const prompt = "Say hello in one short sentence."
	recorder := gwtesting.NewRecordingWebSocketDialer(&scriptedRealtimeDialer{}, "openai", "gpt-realtime-2.1-mini")

	opts := services.SessionRunOptions{
		Provider:        "openai",
		Model:           "gpt-realtime-2.1-mini",
		APIKey:          "test-key",
		RecordPath:      t.TempDir() + "/capture.json",
		ConfigDir:       t.TempDir(),
		WebSocketDialer: recorder,
	}
	out := &bytes.Buffer{}
	err := services.RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(
		context.Background(), out, opts, "", 2*time.Second,
		services.SessionTextSeed{Value: prompt, Present: true}, "",
	)
	if err != nil {
		t.Fatalf("session run: %v", err)
	}

	capture := recorder.Capture()
	var itemCreates []string
	promptOnWire := false
	for _, record := range capture.Records {
		payload := string(record.Payload)
		if strings.Contains(payload, "agent-cli-session-text-seed") {
			t.Fatalf("sentinel leaked to the wire in frame %q: %s", record.Type, payload)
		}
		if record.Direction == gwtesting.DirectionClientToServer && record.Type == "conversation.item.create" {
			itemCreates = append(itemCreates, payload)
			var decoded struct {
				Item struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"item"`
			}
			if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
				t.Fatalf("decode conversation.item.create %s: %v", payload, err)
			}
			for _, content := range decoded.Item.Content {
				if content.Text == prompt {
					promptOnWire = true
				}
			}
		}
	}
	if !promptOnWire {
		t.Fatalf("prompt %q not found in conversation.item.create payloads: %v", prompt, itemCreates)
	}
}
