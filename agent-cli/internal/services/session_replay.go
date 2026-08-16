// This file contains session capture replay execution and inspection helpers used to select and validate replay behavior.
package services

import (
	"context"
	"fmt"
	"io"
	"strings"

	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const sessionClosedEventType = "session.closed"

func replaySessionCapture(ctx context.Context, out io.Writer, path string) error {
	replayer, err := gwtesting.NewSessionReplayer(path, gwtesting.WithReplayOutboundValidation(false), gwtesting.WithReplayContext(ctx))
	if err != nil {
		return fmt.Errorf("replay session capture %s: %w", path, err)
	}
	for {
		select {
		case <-ctx.Done():
			_ = replayer.Close()
			return ctx.Err()
		case <-replayer.Done():
			return drainSessionReplayMessages(out, replayer)
		case msg, ok := <-replayer.Receive().Chan():
			if !ok {
				continue
			}
			if err := writeSessionReplayMessage(out, msg); err != nil {
				return err
			}
		}
	}
}

func drainSessionReplayMessages(out io.Writer, replayer *gwtesting.SessionReplayer) error {
	for {
		msg, ok := replayer.Receive().Read()
		if !ok {
			return nil
		}
		if err := writeSessionReplayMessage(out, msg); err != nil {
			return err
		}
	}
}

func grokReplayCaptureHasSessionClose(path string) bool {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return false
	}
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionServerToClient && record.Type == "session.closed" {
			return true
		}
	}
	return false
}

func usesWebSocketCapture(path string) bool {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return false
	}
	for _, record := range capture.Records {
		if record.PayloadType == gwtesting.SessionPayloadTypeWebSocketMessage {
			return true
		}
	}
	return false
}

func usesOpenAIWebSocketCapture(path string) bool {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return false
	}
	return strings.EqualFold(capture.Provider.Name, sessionProviderOpenAI)
}

func captureHasEvent(path string, eventType string) bool {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return false
	}
	for _, record := range capture.Records {
		if record.Type == eventType {
			return true
		}
	}
	return false
}
