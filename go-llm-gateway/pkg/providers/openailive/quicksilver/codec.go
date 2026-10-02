package quicksilver

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
)

// ErrMalformedEvent reports a frame that is not a JSON object with a string
// "type" field, or whose fields do not match its type.
var ErrMalformedEvent = errors.New("quicksilver: malformed event")

// EncodeEvent encodes one event as a flat JSON object whose first field is
// "type". An UnknownEvent re-encodes its raw frame unchanged.
func EncodeEvent(event Event) ([]byte, error) {
	return openailive.EncodeEvent(event)
}

// DecodeServerEvent decodes one server frame. A frame whose type this package
// does not model decodes to UnknownEvent, not an error. Unknown extra fields
// are ignored.
func DecodeServerEvent(frame []byte) (Event, error) {
	return decode(frame, serverDecoder)
}

// DecodeClientEvent decodes one client frame, as a server or a recorder sees
// it. A frame whose type is not a client event decodes to UnknownEvent.
func DecodeClientEvent(frame []byte) (Event, error) {
	return decode(frame, clientDecoder)
}

type decoder func([]byte) (Event, error)

func decode(frame []byte, lookup func(string) decoder) (Event, error) {
	var head struct {
		Type *string `json:"type"`
	}
	if err := json.Unmarshal(frame, &head); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedEvent, err)
	}
	if head.Type == nil || *head.Type == "" {
		return nil, fmt.Errorf("%w: missing type", ErrMalformedEvent)
	}
	if decodeFrame := lookup(*head.Type); decodeFrame != nil {
		return decodeFrame(frame)
	}
	return UnknownEvent{Type: *head.Type, Raw: append(json.RawMessage(nil), frame...)}, nil
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
	case TypeInputAudioAppend:
		return decodeAs[InputAudioAppend]
	case TypeSessionUpdate:
		return decodeAs[SessionUpdate]
	case TypeSessionContextAppend:
		return decodeAs[SessionContextAppend]
	case TypeDelegationContextAppend:
		return decodeAs[DelegationContextAppend]
	case TypeSessionClose:
		return decodeAs[SessionClose]
	}
	return nil
}

func serverDecoder(eventType string) decoder {
	switch eventType {
	case TypeSessionStarted:
		return decodeAs[SessionStarted]
	case TypeSessionUpdated:
		return decodeAs[SessionUpdated]
	case TypeOutputAudioDelta:
		return decodeAs[OutputAudioDelta]
	case TypeInputTranscriptAdded:
		return decodeAs[InputTranscriptAdded]
	case TypeOutputTranscriptAdded:
		return decodeAs[OutputTranscriptAdded]
	case TypeTurnDone:
		return decodeAs[TurnDone]
	case TypeDelegationCreated:
		return decodeAs[DelegationCreated]
	case TypeOutputAudioBufferCleared:
		return decodeAs[OutputAudioBufferCleared]
	case TypeError:
		return decodeAs[ErrorEvent]
	}
	return nil
}
