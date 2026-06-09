package logger

import (
	"go.uber.org/zap"

	gatewaylogging "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
)

// ZapGatewayAdapter wraps a zap.Logger to implement the go-llm-gateway logging seam.
type ZapGatewayAdapter struct {
	z *zap.Logger
}

// NewZapGatewayAdapter returns an adapter that satisfies the gateway logger interface.
func NewZapGatewayAdapter(z *zap.Logger) *ZapGatewayAdapter {
	if z == nil {
		z = zap.NewNop()
	}
	return &ZapGatewayAdapter{z: z}
}

func gatewayFieldsToZap(fields ...gatewaylogging.Field) []zap.Field {
	zfs := make([]zap.Field, 0, len(fields))
	for _, f := range fields {
		zfs = append(zfs, zap.Any(f.Key, f.Value))
	}
	return zfs
}

func (a *ZapGatewayAdapter) Debug(msg string, fields ...gatewaylogging.Field) {
	a.z.Debug(msg, gatewayFieldsToZap(fields...)...)
}

func (a *ZapGatewayAdapter) Info(msg string, fields ...gatewaylogging.Field) {
	a.z.Info(msg, gatewayFieldsToZap(fields...)...)
}

func (a *ZapGatewayAdapter) Warn(msg string, fields ...gatewaylogging.Field) {
	a.z.Warn(msg, gatewayFieldsToZap(fields...)...)
}

func (a *ZapGatewayAdapter) Error(msg string, fields ...gatewaylogging.Field) {
	a.z.Error(msg, gatewayFieldsToZap(fields...)...)
}

func (a *ZapGatewayAdapter) Fatal(msg string, fields ...gatewaylogging.Field) {
	a.z.Fatal(msg, gatewayFieldsToZap(fields...)...)
}

func (a *ZapGatewayAdapter) Panic(msg string, fields ...gatewaylogging.Field) {
	a.z.Panic(msg, gatewayFieldsToZap(fields...)...)
}

var _ gatewaylogging.Logger = (*ZapGatewayAdapter)(nil)
