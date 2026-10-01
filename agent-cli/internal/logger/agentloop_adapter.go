package logger

import (
	"go.uber.org/zap"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
)

// ZapAgentLoopAdapter wraps a zap.Logger to implement the go-agent-loop logging.Logger interface.
type ZapAgentLoopAdapter struct {
	zapFieldAdapter[logging.Field]
}

// NewZapAgentLoopAdapter returns an adapter that satisfies logging.Logger using the given zap logger.
func NewZapAgentLoopAdapter(z *zap.Logger) *ZapAgentLoopAdapter {
	return &ZapAgentLoopAdapter{newZapFieldAdapter(z, func(f logging.Field) zap.Field { return zap.Any(f.Key, f.Value) })}
}

// Ensure ZapAgentLoopAdapter implements logging.Logger.
var _ logging.Logger = (*ZapAgentLoopAdapter)(nil)
