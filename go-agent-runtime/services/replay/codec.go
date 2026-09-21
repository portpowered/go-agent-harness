package replay

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/capture"
)

// EncodeStreamMessage exposes the canonical capture codec to a host that
// needs to inspect an already-admitted transcript record. It does not admit a
// file or expose replay cursors.
func EncodeStreamMessage(message messages.StreamMessage) ([]byte, error) {
	payload, err := capture.MarshalStreamMessage(message)
	return append([]byte(nil), payload...), err
}

// DecodeStreamMessage decodes one canonical captured stream message. Capture
// admission, ordering, and replay lifecycle remain owned by Service.
func DecodeStreamMessage(data []byte) (messages.StreamMessage, error) {
	return capture.UnmarshalStreamMessage(json.RawMessage(data))
}
