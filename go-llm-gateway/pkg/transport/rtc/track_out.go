package rtc

import "github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"

// OutboundRTPClockRate is the Pion-facing negotiated WebRTC Opus clock. Track
// packetization and lifecycle policy lives in runtime rtctransport.
const OutboundRTPClockRate = wavio.Rate48kHz
