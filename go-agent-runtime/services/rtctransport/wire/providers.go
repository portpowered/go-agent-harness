//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire composes the reusable RTC transport policy without exposing
// its private queue, RTP validation, or lifecycle implementation.
package wire

import (
	"github.com/google/wire"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport/internal/service"
)

func NewService() rtctransport.Service {
	wire.Build(service.New, wire.Bind(new(rtctransport.Service), new(*service.Service)))
	return nil
}
