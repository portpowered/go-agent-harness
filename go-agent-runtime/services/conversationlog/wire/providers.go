//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire constructs independent conversation-log reducers.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/conversationlog"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/conversationlog/internal/service"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// NewService constructs a reducer with the supplied wall-clock source. A nil
// source is valid and keeps timing out of deterministic snapshots.
func NewService(source clock.Source) conversationlog.Service {
	wire.Build(service.New, wire.Bind(new(conversationlog.Service), new(*service.Service)))
	return nil
}
