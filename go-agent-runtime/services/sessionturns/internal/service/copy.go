package service

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturns"
)

func cloneInput(input sessionturns.TurnInput) sessionturns.TurnInput {
	input.Audio = append([]byte(nil), input.Audio...)
	return input
}

func cloneTurn(turn *sessionturns.SessionTurn) sessionturns.SessionTurn {
	if turn == nil {
		return sessionturns.SessionTurn{}
	}
	clone := *turn
	clone.Input = cloneInput(turn.Input)
	clone.Response = cloneMessage(turn.Response)
	return clone
}

func cloneMessage(message messages.Message) messages.Message {
	clone := message
	if message.Index != nil {
		index := *message.Index
		clone.Index = &index
	}
	clone.ToolCalls = append([]messages.ToolCall(nil), message.ToolCalls...)
	if message.ContentParts == nil {
		return clone
	}
	clone.ContentParts = make([]messages.ContentPart, len(message.ContentParts))
	for i, part := range message.ContentParts {
		clone.ContentParts[i] = cloneContentPart(part)
	}
	return clone
}

func cloneContentPart(part messages.ContentPart) messages.ContentPart {
	switch value := part.(type) {
	case messages.ControlPlanePart:
		return value
	case *messages.ControlPlanePart:
		return clonePointer(value)
	case messages.TextPart:
		return value
	case *messages.TextPart:
		return clonePointer(value)
	case messages.ImagePart:
		value.Bytes = append([]byte(nil), value.Bytes...)
		return value
	case *messages.ImagePart:
		if value == nil {
			return (*messages.ImagePart)(nil)
		}
		clone := *value
		clone.Bytes = append([]byte(nil), value.Bytes...)
		return &clone
	case messages.AudioPart:
		value.Bytes = append([]byte(nil), value.Bytes...)
		return value
	case *messages.AudioPart:
		if value == nil {
			return (*messages.AudioPart)(nil)
		}
		clone := *value
		clone.Bytes = append([]byte(nil), value.Bytes...)
		return &clone
	case messages.TranscriptPart:
		return value
	case *messages.TranscriptPart:
		return clonePointer(value)
	case messages.VideoPart:
		value.Bytes = append([]byte(nil), value.Bytes...)
		return value
	case *messages.VideoPart:
		if value == nil {
			return (*messages.VideoPart)(nil)
		}
		clone := *value
		clone.Bytes = append([]byte(nil), value.Bytes...)
		return &clone
	case messages.FilePart:
		value.Bytes = append([]byte(nil), value.Bytes...)
		return value
	case *messages.FilePart:
		if value == nil {
			return (*messages.FilePart)(nil)
		}
		clone := *value
		clone.Bytes = append([]byte(nil), value.Bytes...)
		return &clone
	case messages.EmbeddingPart:
		value.Bytes = append([]byte(nil), value.Bytes...)
		return value
	case *messages.EmbeddingPart:
		if value == nil {
			return (*messages.EmbeddingPart)(nil)
		}
		clone := *value
		clone.Bytes = append([]byte(nil), value.Bytes...)
		return &clone
	case messages.UsageInfoPart:
		return value
	case *messages.UsageInfoPart:
		return clonePointer(value)
	case messages.ReasoningPart:
		return value
	case *messages.ReasoningPart:
		return clonePointer(value)
	default:
		return part
	}
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
