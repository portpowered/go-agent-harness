package hermetic

import (
	"encoding/json"
	"sort"
)

func pendingIDs(pending map[string]struct{}) []string {
	result := make([]string, 0, len(pending))
	for id := range pending {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func cloneOperationRequest(request OperationRequest) OperationRequest {
	if request.Type == OperationInvokeTool && len(request.Input) == 0 {
		request.Input = json.RawMessage(`{}`)
	}
	request.Input = cloneRaw(request.Input)
	return request
}

func cloneRuntimeExecution(execution RuntimeExecution) RuntimeExecution {
	execution.Request = cloneOperationRequest(execution.Request)
	execution.Result = cloneRaw(execution.Result)
	events := execution.Events
	execution.Events = make([]FixtureEvent, len(events))
	for index, event := range events {
		execution.Events[index] = cloneFixtureEvent(event)
	}
	return execution
}

func cloneFixtureEvent(event FixtureEvent) FixtureEvent {
	event.Tools = cloneToolDescriptors(event.Tools)
	event.Output = cloneRaw(event.Output)
	event.Error = cloneRaw(event.Error)
	return event
}

func cloneToolDescriptors(tools []ToolDescriptor) []ToolDescriptor {
	if tools == nil {
		return nil
	}
	result := make([]ToolDescriptor, len(tools))
	for index, tool := range tools {
		result[index] = tool
		result[index].InputSchema = cloneRaw(tool.InputSchema)
		result[index].Annotations = cloneRaw(tool.Annotations)
	}
	return result
}
