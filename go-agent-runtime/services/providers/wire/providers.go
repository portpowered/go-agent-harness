//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the built-in providers service without exposing its
// implementation package to embedders.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/internal/admission"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/internal/catalog"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/internal/service"
)

// NewService assembles the provider service from explicit host ports.
func NewService(deps Dependencies) providers.FullService {
	wire.Build(NewModelCatalog, wire.FieldsOf(new(Dependencies), "HTTPClient", "Logger", "Clock", "Recording", "ProviderCapture"), service.New, wire.Bind(new(providers.FullService), new(*service.Service)))
	return nil
}

// NewModelCatalog installs the provider-owned immutable model catalog at the
// application composition boundary.
func NewModelCatalog() providers.ModelCatalog { return catalog.New() }

// NewModelAdmission constructs the provider-owned admission role around an
// explicitly supplied catalog. This keeps custom catalogs usable by an
// independent consumer without exposing providers/internal packages.
func NewModelAdmission(modelCatalog providers.ModelCatalog) providers.ModelAdmission {
	return admission.NewService(modelCatalog)
}
