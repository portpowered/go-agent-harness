package openaichatgpt

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func streamError(stream <-chan messages.StreamMessage) error {
	var found error
	for msg := range stream {
		if value, ok := msg.Value.(*messages.ErrorValue); ok && found == nil {
			found = streamValueError(value)
		}
	}
	return found
}

func TestStreamIdleTimeoutFailsTheTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		ch := make(chan messages.StreamMessage, providers.StreamMessageBuffer)
		go func() {
			defer close(ch)
			translateStream(reader, reader.Close, ch, DefaultStreamIdleTimeout, replayTarget{})
		}()
		// One event arrives; then the backend goes silent and never closes.
		if _, err := writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"par\"}\n\n")); err != nil {
			t.Fatalf("write event: %v", err)
		}
		start := time.Now()
		err := streamError(ch)
		if !errors.Is(err, ErrStreamIdle) || !errors.Is(err, providers.ErrTransport) {
			t.Fatalf("error = %v, want ErrStreamIdle as a transport error", err)
		}
		if waited := time.Since(start); waited != DefaultStreamIdleTimeout {
			t.Fatalf("failed after %v, want exactly the idle timeout %v", waited, DefaultStreamIdleTimeout)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close writer: %v", err)
		}
	})
}

func TestStreamActivityKeepsTheIdleWatchQuiet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		ch := make(chan messages.StreamMessage, providers.StreamMessageBuffer)
		go func() {
			defer close(ch)
			translateStream(reader, reader.Close, ch, time.Minute, replayTarget{})
		}()
		go func() {
			for range 3 {
				<-time.After(50 * time.Second) // virtual time inside the synctest bubble
				if _, err := writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n")); err != nil {
					return
				}
			}
			if _, err := writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{}}\n\n")); err != nil {
				return
			}
			if err := writer.Close(); err != nil {
				t.Errorf("close writer: %v", err)
			}
		}()
		if err := streamError(ch); err != nil {
			t.Fatalf("stream with steady activity failed: %v", err)
		}
	})
}

func TestStreamAcceptsLinesBeyondOneMebibyte(t *testing.T) {
	big := strings.Repeat("a", 3<<20)
	body := "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_big\",\"summary\":[],\"encrypted_content\":\"" + big + "\"}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_big\",\"name\":\"f\",\"arguments\":\"{}\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{}}\n\n"
	replay := newReasoningReplay()
	if err := streamError(translateFixture(bytes.NewReader([]byte(body)), replayTarget{store: replay})); err != nil {
		t.Fatalf("3 MiB line failed: %v", err)
	}
	kept := replay.lookup("", models.Message{Role: models.RoleAssistant, ToolCalls: []models.ToolCall{{ID: "call_big"}}})
	if items := kept.reasoningBefore("call_big"); len(items) != 1 || !strings.Contains(string(items[0]), big) {
		t.Fatal("the large encrypted reasoning item was not kept for replay")
	}

	tooBig := "data: \"" + strings.Repeat("a", sseLineBytes) + "\"\n\n"
	if err := streamError(translateFixture(strings.NewReader(tooBig), replayTarget{})); !errors.Is(err, providers.ErrTransport) {
		t.Fatalf("line over the bound = %v, want a transport error", err)
	}
}
