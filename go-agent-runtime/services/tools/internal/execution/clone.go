package execution

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

func cloneToolCallResponse(response messages.ToolCallResponse) messages.ToolCallResponse {
	response.ContentParts = cloneContentParts(response.ContentParts)
	return response
}

func cloneContentParts(parts []messages.ContentPart) []messages.ContentPart {
	if parts == nil {
		return nil
	}
	cloned := make([]messages.ContentPart, len(parts))
	for index, part := range parts {
		switch value := part.(type) {
		case messages.ImagePart:
			value.Bytes = append([]byte(nil), value.Bytes...)
			cloned[index] = value
		case messages.AudioPart:
			value.Bytes = append([]byte(nil), value.Bytes...)
			cloned[index] = value
		case messages.VideoPart:
			value.Bytes = append([]byte(nil), value.Bytes...)
			cloned[index] = value
		case messages.FilePart:
			value.Bytes = append([]byte(nil), value.Bytes...)
			cloned[index] = value
		case messages.EmbeddingPart:
			value.Bytes = append([]byte(nil), value.Bytes...)
			cloned[index] = value
		default:
			cloned[index] = part
		}
	}
	return cloned
}

func (c *controller) observeCall(call messages.ToolCall) {
	if c.observer == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			return
		}
	}()
	c.observer.ObserveToolCall(call)
}

func (c *controller) observeResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
	if c.observer == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			return
		}
	}()
	c.observer.ObserveToolResult(call, response, failed)
}
