package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func replayHasInterruptionReplacement(records []gatewaytesting.CapturedSessionEvent) bool {
	cancelledResponseID := ""
	for _, record := range records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		if responseID, ok := replayCancelledResponseID(record); ok {
			cancelledResponseID = responseID
			continue
		}
		if cancelledResponseID == "" || record.Type != "response.created" {
			continue
		}
		if replayCreatedResponseIsReplacement(record, cancelledResponseID) {
			return true
		}
	}
	return false
}

func replayCancelledResponseID(record gatewaytesting.CapturedSessionEvent) (string, bool) {
	if record.Type != "response.done" {
		return "", false
	}
	var event struct {
		Response struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			StatusDetails struct {
				Type string `json:"type"`
			} `json:"status_details"`
		} `json:"response"`
	}
	if err := json.Unmarshal(replayRecordPayload(record), &event); err != nil {
		return "", false
	}
	if !replayResponseWasCancelled(event.Response.Status, event.Response.StatusDetails.Type) {
		return "", false
	}
	return event.Response.ID, true
}

func replayCreatedResponseIsReplacement(record gatewaytesting.CapturedSessionEvent, cancelledResponseID string) bool {
	var event struct {
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if err := json.Unmarshal(replayRecordPayload(record), &event); err != nil {
		return false
	}
	// A missing ID cannot be correlated safely, so the later provider
	// response is treated as the replacement boundary. When both IDs
	// are present, require a distinct response to avoid mistaking a
	// duplicate event for a replacement.
	return event.Response.ID == "" || cancelledResponseID == "" || event.Response.ID != cancelledResponseID
}

func replayResponseWasCancelled(status, detailType string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	detailType = strings.ToLower(strings.TrimSpace(detailType))
	return status == "cancelled" || status == "canceled" || detailType == "cancelled" || detailType == "canceled"
}

// ReplayCapture renders the ordered server stream from a capture through the
// supplied sink. The replay service owns admission, cursor lifecycle, and
// bounded draining; callers only adapt messages to their presentation layer.
func (s *Service) ReplayCapture(ctx context.Context, path string, sink func(messages.StreamMessage) error) error {
	if err := replayContextError(ctx); err != nil {
		return err
	}
	if sink == nil {
		return replay.ErrReplaySinkRequired
	}
	capturePath, err := s.ResolveCapturePath(ctx, path)
	if err != nil {
		return err
	}
	replayer, err := gatewaytesting.NewSessionReplayer(
		capturePath,
		gatewaytesting.WithReplayOutboundValidation(false),
		gatewaytesting.WithReplayContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("replay session capture %s: %w", path, err)
	}
	return replayCaptureMessages(ctx, replayer, sink)
}

func replayCaptureMessages(ctx context.Context, replayer *gatewaytesting.SessionReplayer, sink func(messages.StreamMessage) error) error {
	for {
		select {
		case <-ctx.Done():
			return errors.Join(context.Cause(ctx), replayer.Close())
		case <-replayer.Done():
			if err := drainReplayMessages(ctx, replayer, sink); err != nil {
				return err
			}
			return replayer.Err()
		case message, ok := <-replayer.Receive().Chan():
			if !ok {
				continue
			}
			if err := sink(message); err != nil {
				return errors.Join(err, replayer.Close())
			}
		}
	}
}

func drainReplayMessages(ctx context.Context, replayer *gatewaytesting.SessionReplayer, sink func(messages.StreamMessage) error) error {
	for {
		select {
		case <-ctx.Done():
			return errors.Join(context.Cause(ctx), replayer.Close())
		default:
		}
		message, ok := replayer.Receive().Read()
		if !ok {
			return nil
		}
		if err := sink(message); err != nil {
			return errors.Join(err, replayer.Close())
		}
	}
}
