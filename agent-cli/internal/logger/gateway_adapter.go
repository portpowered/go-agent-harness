package logger

import (
	"go.uber.org/zap"

	gatewaylogging "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
)

// ZapGatewayAdapter wraps a zap.Logger to implement the go-llm-gateway logging seam.
type ZapGatewayAdapter struct {
	zapFieldAdapter[gatewaylogging.Field]
}

// NewZapGatewayAdapter returns an adapter that satisfies the gateway logger interface.
func NewZapGatewayAdapter(z *zap.Logger) *ZapGatewayAdapter {
	return &ZapGatewayAdapter{newZapFieldAdapter(z, func(f gatewaylogging.Field) zap.Field { return zap.Any(f.Key, f.Value) })}
}

var _ gatewaylogging.Logger = (*ZapGatewayAdapter)(nil)
