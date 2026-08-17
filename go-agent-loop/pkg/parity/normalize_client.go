package parity

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

// NormalizeClient reduces a client-owned transcript to the shared parity
// projection. The shared reducer is deliberately responsible for decoding the
// presence-aware event payloads and copying retained evidence; this adapter
// supplies the client-side semantic boundary.
//
// Client-only recorder mechanics are excluded at the shared boundary: the
// record timestamp is wall-clock arrival data, peer/direction/stream are
// recorder metadata after they have been validated, and the explicitly named
// transport.id, transport.identifier, transport.packet, transport.frame, and
// transport.segment events contain only transport identity or framing. Logical
// ticks and all semantic event payloads remain in the projection.
func NormalizeClient(records []transcript.Record) (Projection, error) {
	for index, record := range records {
		if record.Peer != transcript.PeerClient {
			return Projection{}, newNormalizationError(
				"client",
				fmt.Sprintf("records[%d].peer", index),
				fmt.Sprintf("must be %q", transcript.PeerClient),
			)
		}
	}

	return Normalize("client", records)
}
