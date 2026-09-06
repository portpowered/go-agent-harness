package input

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// CloneLiveRequest detaches caller-owned request data before workers start.
func CloneLiveRequest(request session.LiveRequest) session.LiveRequest {
	request.ToolNames = append([]string(nil), request.ToolNames...)
	request.OpeningContentParts = CloneContentParts(request.OpeningContentParts)
	if request.TurnDetection != nil {
		policy := *request.TurnDetection
		if request.TurnDetection.CreateResponse != nil {
			createResponse := *request.TurnDetection.CreateResponse
			policy.CreateResponse = &createResponse
		}
		if request.TurnDetection.InterruptResponse != nil {
			interruptResponse := *request.TurnDetection.InterruptResponse
			policy.InterruptResponse = &interruptResponse
		}
		request.TurnDetection = &policy
	}
	if request.Capabilities != nil {
		binding := *request.Capabilities
		binding.Definitions = CloneToolDefinitions(binding.Definitions)
		request.Capabilities = &binding
	}
	if request.ReplayPlan != nil {
		plan := *request.ReplayPlan
		plan.AudioTurns = make([]session.LiveReplayAudioTurn, len(request.ReplayPlan.AudioTurns))
		for index, turn := range request.ReplayPlan.AudioTurns {
			plan.AudioTurns[index].Chunks = make([][]int16, len(turn.Chunks))
			for chunkIndex, chunk := range turn.Chunks {
				plan.AudioTurns[index].Chunks[chunkIndex] = append([]int16(nil), chunk...)
			}
		}
		request.ReplayPlan = &plan
	}
	return request
}

// CloneContentParts preserves part order and owns all mutable binary content.
func CloneContentParts(parts []messages.ContentPart) []messages.ContentPart {
	if len(parts) == 0 {
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

// CloneToolDefinitions detaches mutable schemas without changing advertised order.
func CloneToolDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	cloned := append([]messages.ToolDefinition(nil), definitions...)
	for index := range cloned {
		cloned[index].Parameters = append([]messages.ToolParameter(nil), definitions[index].Parameters...)
		cloned[index].ParameterSchema = append([]byte(nil), definitions[index].ParameterSchema...)
	}
	return cloned
}
