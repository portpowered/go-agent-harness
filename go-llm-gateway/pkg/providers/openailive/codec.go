package openailive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrMalformedEvent reports a frame that is not a JSON object with a string
// "type" field, or whose fields do not match its type.
var ErrMalformedEvent = errors.New("openailive: malformed event")

// EncodeEvent encodes one event as a flat JSON object whose first field is
// "type". An UnknownEvent re-encodes its raw frame unchanged.
func EncodeEvent(event Event) ([]byte, error) {
	if event == nil {
		return nil, fmt.Errorf("%w: nil event", ErrMalformedEvent)
	}
	if unknown, ok := event.(UnknownEvent); ok {
		return append([]byte(nil), unknown.Raw...), nil
	}
	body, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("openailive: encode %s: %w", event.EventType(), err)
	}
	typeField, err := json.Marshal(event.EventType())
	if err != nil {
		return nil, fmt.Errorf("openailive: encode %s: %w", event.EventType(), err)
	}
	var out bytes.Buffer
	out.Grow(len(body) + len(typeField) + len(`{"type":,`))
	out.WriteString(`{"type":`)
	out.Write(typeField)
	if rest := bytes.TrimSpace(body[1:]); len(rest) > 1 {
		out.WriteByte(',')
		out.Write(rest)
	} else {
		out.WriteByte('}')
	}
	return out.Bytes(), nil
}

// DecodeServerEvent decodes one server frame. A frame whose type this package
// does not model decodes to UnknownEvent, not an error. Fields the spec marks
// optional, or that OpenAI and Azure disagree on (output-audio timing,
// client_event_id, status), may be absent; unknown extra fields are ignored.
func DecodeServerEvent(frame []byte) (Event, error) {
	eventType, err := frameType(frame)
	if err != nil {
		return nil, err
	}
	decode := serverDecoder(eventType)
	if decode == nil {
		return UnknownEvent{Type: eventType, Raw: append(json.RawMessage(nil), frame...)}, nil
	}
	return decode(frame)
}

// DecodeClientEvent decodes one client frame, as a server or a recorder sees
// it. A frame whose type is not a client event decodes to UnknownEvent.
func DecodeClientEvent(frame []byte) (Event, error) {
	eventType, err := frameType(frame)
	if err != nil {
		return nil, err
	}
	decode := clientDecoder(eventType)
	if decode == nil {
		return UnknownEvent{Type: eventType, Raw: append(json.RawMessage(nil), frame...)}, nil
	}
	return decode(frame)
}

type decoder func([]byte) (Event, error)

func frameType(frame []byte) (string, error) {
	var head struct {
		Type *string `json:"type"`
	}
	if err := json.Unmarshal(frame, &head); err != nil {
		return "", fmt.Errorf("%w: %w", ErrMalformedEvent, err)
	}
	if head.Type == nil || *head.Type == "" {
		return "", fmt.Errorf("%w: missing type", ErrMalformedEvent)
	}
	return *head.Type, nil
}

func decodeAs[T Event](frame []byte) (Event, error) {
	var event T
	if err := json.Unmarshal(frame, &event); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrMalformedEvent, event.EventType(), err)
	}
	return event, nil
}

func clientDecoder(eventType string) decoder {
	switch eventType {
	case TypeSessionStart:
		return decodeAs[SessionStart]
	case TypeSessionUpdate:
		return decodeAs[SessionUpdate]
	case TypeInputAudioAppend:
		return decodeAs[InputAudioAppend]
	case TypeInputAudioMute:
		return decodeAs[InputAudioMute]
	case TypeInputAudioUnmute:
		return decodeAs[InputAudioUnmute]
	case TypeInstructionsAppend:
		return decodeAs[InstructionsAppend]
	case TypeThinkingAppend:
		return decodeAs[ThinkingAppend]
	case TypeCommentaryAppend:
		return decodeAs[CommentaryAppend]
	case TypeResponseItemCreate:
		return decodeAs[ResponseItemCreate]
	case TypeResponseCreate:
		return decodeAs[ResponseCreate]
	case TypeSessionClose:
		return decodeAs[SessionClose]
	}
	return nil
}

func serverDecoder(eventType string) decoder {
	if decode := sessionServerDecoder(eventType); decode != nil {
		return decode
	}
	return transportServerDecoder(eventType)
}

func sessionServerDecoder(eventType string) decoder {
	switch eventType {
	case TypeSessionStarted:
		return decodeAs[SessionStarted]
	case TypeSessionUpdated:
		return decodeAs[SessionUpdated]
	case TypeInputAudioMuted:
		return decodeAs[InputAudioMuted]
	case TypeInputAudioUnmuted:
		return decodeAs[InputAudioUnmuted]
	case TypeInstructionsAppended:
		return decodeAs[InstructionsAppended]
	case TypeThinkingAppended:
		return decodeAs[ThinkingAppended]
	case TypeCommentaryAppended:
		return decodeAs[CommentaryAppended]
	case TypeOutputAudioDelta:
		return decodeAs[OutputAudioDelta]
	case TypeInputTranscriptDelta:
		return decodeAs[InputTranscriptDelta]
	case TypeOutputTranscriptDelta:
		return decodeAs[OutputTranscriptDelta]
	case TypeDelegationCreated:
		return decodeAs[DelegationCreated]
	case TypeResponseEvent:
		return decodeAs[ResponseEvent]
	case TypeUsageUpdated:
		return decodeAs[UsageUpdated]
	case TypeSessionClosed:
		return decodeAs[SessionClosed]
	case TypeError:
		return decodeAs[ErrorEvent]
	case TypeInfo:
		return decodeAs[Info]
	}
	return nil
}

func transportServerDecoder(eventType string) decoder {
	switch eventType {
	case TypeInputAudioAppend:
		return decodeAs[ReflectedInputAudio]
	case TypeTransportRinging:
		return decodeAs[TransportRinging]
	case TypeTransportAnswered:
		return decodeAs[TransportAnswered]
	case TypeTransportFailed:
		return decodeAs[TransportFailed]
	case TypeDTMFReceived:
		return decodeAs[DTMFReceived]
	case TypeDTMFSend:
		return decodeAs[DTMFSend]
	}
	return nil
}
