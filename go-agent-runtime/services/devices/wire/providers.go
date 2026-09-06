//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire contains the device service's application providers. Device
// admission policy stays in the private implementation package; this edge
// exposes only the public device service contract.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	filemedia "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/internal/file"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/internal/media"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// NewService creates the process-scoped device service. It is inert until a
// caller invokes Open with a normalized request.
func NewService(registry devicegw.DeviceRegistry) devices.Service {
	wire.Build(newFactory, wire.Bind(new(devices.Service), new(*media.Factory)))
	return nil
}

// NewFileService assembles the finite file-backed device role. The returned
// service is stateless; each Open call takes ownership of its caller-opened
// source and sink only after successful admission.
func NewFileService() devices.Service {
	wire.Build(newFileFactory, wire.Bind(new(devices.Service), new(*filemedia.Factory)))
	return nil
}

func newFactory(registry devicegw.DeviceRegistry) *media.Factory {
	return media.NewFactory(registry, mixer.DefaultFormat())
}

func newFileFactory() *filemedia.Factory { return filemedia.NewFactory() }
