// Package probe owns the reusable physical-device probe orchestration. It is
// kept behind the runtime device contract so hosts only provide values and a
// provider-session factory.
package deviceprobe

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// Service runs a probe against one injected device registry. The registry and
// every opened source/sink remain implementation details of this package.
type Service struct {
	registry       devicegw.DeviceRegistry
	sessionFactory runtimeDevices.ProbeSessionFactory
	transport      rtctransport.Service
}

// New creates an inert probe service. Provider sessions are constructed only
// when Run needs them, through the injected session factory.
func New(registry devicegw.DeviceRegistry, sessionFactory runtimeDevices.ProbeSessionFactory, transports ...rtctransport.Service) *Service {
	var transport rtctransport.Service
	if len(transports) > 0 {
		transport = transports[0]
	}
	return &Service{registry: registry, sessionFactory: sessionFactory, transport: transport}
}

var _ runtimeDevices.ProbeService = (*Service)(nil)

func (s *Service) Run(ctx context.Context, request runtimeDevices.ProbeRequest) (observation probe.ObservationSnapshot, err error) {
	if err := contextError(ctx); err != nil {
		return observation, err
	}
	if s == nil || s.registry == nil {
		return observation, errors.New("audio device registry is required")
	}
	availability, err := devicegw.ProbeDeviceAvailability(s.registry)
	if err != nil {
		return observation, err
	}
	if availability.Status != devicegw.DeviceProbeStatusReady {
		return observation, fmt.Errorf("device probe cannot run with availability status %q", availability.Status)
	}
	return runDeviceProbeScenario(ctx, request.Scenario, availability, s.registry, request, s.sessionFactory, s.transport)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
