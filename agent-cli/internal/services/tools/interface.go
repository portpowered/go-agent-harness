// Package tools defines the session capability service boundary.
package tools

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// CapabilityStatus is the lifecycle snapshot for an optional browser
// capability. It intentionally contains no concrete browser implementation.
type CapabilityStatus struct {
	State                  CapabilityState
	Err                    error
	BrowserCapabilityState webmcp.BrowserCapabilityState
}

type CapabilityState string

const (
	CapabilityInitializing CapabilityState = "initializing"
	CapabilityReady        CapabilityState = "ready"
	CapabilityFailed       CapabilityState = "failed"
)

// BrowserCapability is the request-scoped browser seam consumed by the
// service. Composition owns the concrete broker and supplies lifecycle hooks.
type BrowserCapability struct {
	Broker            webmcp.Broker
	Initialize        func(context.Context) error
	Status            func() CapabilityStatus
	BrowserWatch      func(context.Context) <-chan webmcp.BrokerEvent
	BrowserEventWatch func(context.Context) <-chan webmcp.BrowserEvent
	Close             func() error
}

// BrowserFactory constructs one request-scoped browser capability lazily.
type BrowserFactory func(config.BrowserConfig, string) (BrowserCapability, error)

// Capabilities is the session-facing tool surface produced from one config
// snapshot. All callbacks retain ownership of their request-scoped resources.
type Capabilities struct {
	Executor                    messages.ToolExecutor
	Definitions                 []messages.ToolDefinition
	BrowserCapabilityState      webmcp.BrowserCapabilityState
	DisplayCapability           cliTools.DisplayCapability
	RefreshDefinitions          func(context.Context) []messages.ToolDefinition
	RefreshDefinitionsWithError func(context.Context) ([]messages.ToolDefinition, error)
	Initialize                  func(context.Context) error
	Status                      func() CapabilityStatus
	BrowserWatch                func(context.Context) <-chan webmcp.BrokerEvent
	BrowserEventWatch           func(context.Context) <-chan webmcp.BrowserEvent
	Close                       func() error
}

// Factory resolves a config-scoped capability surface.
type Factory func(*config.Config) (Capabilities, error)

// Service resolves capability surfaces while keeping composition in the
// private services implementation.
type Service interface {
	Resolve(*config.Config) (Capabilities, error)
}

// Resolve lets an injected factory satisfy Service without another adapter.
func (factory Factory) Resolve(cfg *config.Config) (Capabilities, error) {
	if factory == nil {
		return Capabilities{}, errors.New("tool capability factory is required")
	}
	return factory(cfg)
}
