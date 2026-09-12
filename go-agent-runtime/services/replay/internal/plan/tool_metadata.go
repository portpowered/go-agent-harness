package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"strings"
)

// initialToolNames observes the first client advertisement. It never grants
// permission to execute tools and does not replace the caller's capability set.
func initialToolNames(records []gatewaytesting.CapturedSessionEvent) ([]string, bool) {
	for _, record := range records {
		if record.Direction != gatewaytesting.DirectionClientToServer || record.Type != replaySessionUpdate {
			continue
		}
		var envelope struct {
			Session struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"session"`
		}
		if err := json.Unmarshal(replayRecordPayload(record), &envelope); err != nil {
			return nil, false
		}
		names := make([]string, 0, len(envelope.Session.Tools))
		for _, tool := range envelope.Session.Tools {
			if name := strings.TrimSpace(tool.Name); name != "" {
				names = append(names, name)
			}
		}
		return names, true
	}
	return nil, false
}

const (
	replayCompletionGrace = 3 * time.Second
	replayTimingRecorded  = "recorded"
)

// ReplayDuration returns a bounded completion window derived from capture
// timestamps. Only recorded timing changes the default grace period; no wall
// clock or sleep is used while admitting the value.
func (s *Service) ReplayDuration(ctx context.Context, path, timing string) (time.Duration, error) {
	if err := replayContextError(ctx); err != nil {
		return replayCompletionGrace, err
	}
	if strings.ToLower(strings.TrimSpace(timing)) != replayTimingRecorded {
		return replayCompletionGrace, nil
	}
	capture, err := s.admitCapture(ctx, path)
	if err != nil {
		return replayCompletionGrace, err
	}
	if len(capture.Records) < 2 {
		return replayCompletionGrace, nil
	}
	first := capture.Records[0].TimestampMs
	last := capture.Records[len(capture.Records)-1].TimestampMs
	if last <= first {
		return replayCompletionGrace, nil
	}
	return time.Duration(last-first)*time.Millisecond + replayCompletionGrace, nil
}

// HasEvent answers a capture-derived lifecycle probe without exposing capture
// records to the host package.
func (s *Service) HasEvent(ctx context.Context, path, eventType string) (bool, error) {
	if err := replayContextError(ctx); err != nil {
		return false, err
	}
	if eventType == "" {
		return false, fmt.Errorf("replay event type is empty")
	}
	capture, err := s.admitCapture(ctx, path)
	if err != nil {
		return false, err
	}
	for _, record := range capture.Records {
		if record.Type == eventType {
			return true, nil
		}
	}
	return false, nil
}
