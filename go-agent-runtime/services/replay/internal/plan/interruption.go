package plan

import (
	"encoding/json"
	"strings"

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
