package plan

import (
	"encoding/json"
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
