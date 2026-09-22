//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire contains the device service's application providers. Device
// admission policy stays in the private implementation package; this edge
// exposes only the public device service contract.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/internal/composite"
	filemedia "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/internal/file"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/internal/media"
	deviceprobe "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/internal/probe"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// NewService creates the process-scoped device service. It is inert until a
// caller invokes Open with a normalized request.
func NewService(registry devicegw.DeviceRegistry, audioService audioio.Service) devices.Service {
	wire.Build(newFactory, wire.Bind(new(devices.Service), new(*composite.Factory)))
	return nil
}

// NewFileService assembles the finite file-backed device role. The returned
// service is stateless; each Open call takes ownership of its caller-opened
// source and sink only after successful admission.
func NewFileService(audioService audioio.Service) devices.Service {
	wire.Build(newFileFactory, wire.Bind(new(devices.Service), new(*filemedia.Factory)))
	return nil
}

// NewProbeService assembles the reusable physical-device probe runner. The
// application graph supplies provider session construction; this package owns
// all negotiated media and device worker lifetimes.
func NewProbeService(registry devicegw.DeviceRegistry, sessionFactory devices.ProbeSessionFactory) devices.ProbeService {
	return deviceprobe.New(registry, sessionFactory)
}

func newFactory(registry devicegw.DeviceRegistry, audioService audioio.Service) *composite.Factory {
	return composite.NewFactory(media.NewFactory(registry, mixer.DefaultFormat()), filemedia.NewFactory(audioService))
}

func newFileFactory(audioService audioio.Service) *filemedia.Factory {
	return filemedia.NewFactory(audioService)
}
