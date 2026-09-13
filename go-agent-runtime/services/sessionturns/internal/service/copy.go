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
	case messages.ImagePart:
		value.Bytes = append([]byte(nil), value.Bytes...)
		return value
	case *messages.ImagePart:
		return cloneImagePart(value)
	case messages.AudioPart:
		value.Bytes = append([]byte(nil), value.Bytes...)
		return value
	case *messages.AudioPart:
		return cloneAudioPart(value)
	case messages.VideoPart:
		value.Bytes = append([]byte(nil), value.Bytes...)
		return value
	case *messages.VideoPart:
		return cloneVideoPart(value)
	case messages.FilePart:
		value.Bytes = append([]byte(nil), value.Bytes...)
		return value
	case *messages.FilePart:
		return cloneFilePart(value)
	case messages.EmbeddingPart:
		value.Bytes = append([]byte(nil), value.Bytes...)
		return value
	case *messages.EmbeddingPart:
		return cloneEmbeddingPart(value)
	default:
		return cloneScalarContentPart(part)
	}
}

func cloneScalarContentPart(part messages.ContentPart) messages.ContentPart {
	switch value := part.(type) {
	case messages.ControlPlanePart, messages.TextPart, messages.TranscriptPart, messages.UsageInfoPart, messages.ReasoningPart:
		return value
	case *messages.ControlPlanePart:
		return clonePointer(value)
	case *messages.TextPart:
		return clonePointer(value)
	case *messages.TranscriptPart:
		return clonePointer(value)
	case *messages.UsageInfoPart:
		return clonePointer(value)
	case *messages.ReasoningPart:
		return clonePointer(value)
	default:
		return part
	}
}

func cloneImagePart(value *messages.ImagePart) messages.ContentPart {
	if value == nil {
		return (*messages.ImagePart)(nil)
	}
	clone := *value
	clone.Bytes = append([]byte(nil), value.Bytes...)
	return &clone
}

func cloneAudioPart(value *messages.AudioPart) messages.ContentPart {
	if value == nil {
		return (*messages.AudioPart)(nil)
	}
	clone := *value
	clone.Bytes = append([]byte(nil), value.Bytes...)
	return &clone
}

func cloneVideoPart(value *messages.VideoPart) messages.ContentPart {
	if value == nil {
		return (*messages.VideoPart)(nil)
	}
	clone := *value
	clone.Bytes = append([]byte(nil), value.Bytes...)
	return &clone
}

func cloneFilePart(value *messages.FilePart) messages.ContentPart {
	if value == nil {
		return (*messages.FilePart)(nil)
	}
	clone := *value
	clone.Bytes = append([]byte(nil), value.Bytes...)
	return &clone
}

func cloneEmbeddingPart(value *messages.EmbeddingPart) messages.ContentPart {
	if value == nil {
		return (*messages.EmbeddingPart)(nil)
	}
	clone := *value
	clone.Bytes = append([]byte(nil), value.Bytes...)
	return &clone
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
