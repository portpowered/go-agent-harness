package browserscenario

import (
	"encoding/json"
	"io"
	"strings"
)

func computeBrowserConversationInputJSONValidity(calls []BrowserConversationBrokerCall) BrowserConversationInputJSONValidity {
	measurement := BrowserConversationInputJSONValidity{}
	for _, call := range calls {
		if call.Operation != BrowserConversationInvoke {
			continue
		}
		valid := browserConversationJSONStringObject(call.InputJSON)
		measurement.Attempts = append(measurement.Attempts, BrowserConversationInputJSONAttempt{
			Sequence: call.Sequence, StepID: call.StepID, InvocationID: cloneBrowserConversationOpaque(call.InvocationID),
			ToolRef: cloneBrowserConversationOpaque(call.ToolRef), ToolName: call.ToolName,
			State: cloneBrowserConversationOpaque(call.State), Terminal: call.Terminal,
			InputJSON: call.InputJSON, ValidObject: valid,
		})
		measurement.TotalAttempts++
		if valid {
			measurement.ValidObjectStrings++
		}
	}
	if measurement.TotalAttempts > 0 {
		measurement.Percentage = float64(measurement.ValidObjectStrings) * 100 / float64(measurement.TotalAttempts)
	}
	return measurement
}

func browserConversationJSONStringObject(value string) bool {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return false
	}
	if _, ok := decoded.(map[string]any); !ok {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}
