package logger

import (
	"go.uber.org/zap"

	"github.com/portpowered/go-agent-loop/pkg/logging"
)

// ZapAgentLoopAdapter wraps a zap.Logger to implement the go-agent-loop logging.Logger interface.
type ZapAgentLoopAdapter struct {
	z *zap.Logger
}

// NewZapAgentLoopAdapter returns an adapter that satisfies logging.Logger using the given zap logger.
func NewZapAgentLoopAdapter(z *zap.Logger) *ZapAgentLoopAdapter {
	if z == nil {
		z = zap.NewNop()
	}
	return &ZapAgentLoopAdapter{z: z}
}

func toZapFields(fields ...logging.Field) []zap.Field {
	zfs := make([]zap.Field, 0, len(fields))
	for _, f := range fields {
		zfs = append(zfs, zap.Any(f.Key, f.Value))
	}
	return zfs
}

func (a *ZapAgentLoopAdapter) Debug(msg string, fields ...logging.Field) {
	a.z.Debug(msg, toZapFields(fields...)...)
}

func (a *ZapAgentLoopAdapter) Info(msg string, fields ...logging.Field) {
	a.z.Info(msg, toZapFields(fields...)...)
}

func (a *ZapAgentLoopAdapter) Warn(msg string, fields ...logging.Field) {
	a.z.Warn(msg, toZapFields(fields...)...)
}

func (a *ZapAgentLoopAdapter) Error(msg string, fields ...logging.Field) {
	a.z.Error(msg, toZapFields(fields...)...)
}

func (a *ZapAgentLoopAdapter) Fatal(msg string, fields ...logging.Field) {
	a.z.Fatal(msg, toZapFields(fields...)...)
}

func (a *ZapAgentLoopAdapter) Panic(msg string, fields ...logging.Field) {
	a.z.Panic(msg, toZapFields(fields...)...)
}

// Ensure ZapAgentLoopAdapter implements logging.Logger.
var _ logging.Logger = (*ZapAgentLoopAdapter)(nil)
