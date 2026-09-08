// Package composite combines independently admitted device roles behind one
// invocation handle. A live provider has one inbound media endpoint, so
// playback fan-out belongs here instead of being recreated by each host.
package composite

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
)

// Factory composes a registry-backed service with the finite file role. Each
// role remains responsible for its own workers; this package only joins their
// admission and playback lifetimes.
type Factory struct {
	physical devices.Service
	file     devices.Service
}

// NewFactory creates a composite service. The services are retained as
// interfaces so hosts can supply a different physical or finite backend in
// tests without exposing implementation types through the public contract.
func NewFactory(physical, file devices.Service) *Factory {
	return &Factory{physical: physical, file: file}
}

type openPlan struct {
	physicalCapture  bool
	physicalPlayback bool
	fileCapture      bool
	filePlayback     bool
}

func (p openPlan) hasPhysical() bool { return p.physicalCapture || p.physicalPlayback }

func (p openPlan) hasFile() bool { return p.fileCapture || p.filePlayback }

func planRequest(request devices.Request) (openPlan, error) {
	plan := openPlan{
		physicalCapture:  request.CaptureEnabled && request.FileInput == nil,
		physicalPlayback: request.PlaybackEnabled,
		fileCapture:      request.FileInput != nil,
		filePlayback:     request.FileOutput != nil,
	}
	if !plan.hasPhysical() && !plan.hasFile() {
		return openPlan{}, fmt.Errorf("%w: at least one media direction must be enabled", devices.ErrInvalidRequest)
	}
	return plan, nil
}

// Open admits the selected roles as one lifecycle unit. File input takes the
// capture direction when present; a physical capture worker cannot be merged
// with it because the public handle exposes one outbound port. File output
// may coexist with physical playback and is fed by the bounded fan-out.
func (f *Factory) Open(ctx context.Context, request devices.Request) (devices.Handle, error) {
	if f == nil {
		return nil, devices.ErrUnavailable
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	plan, err := planRequest(request)
	if err != nil {
		return nil, err
	}
	physical, err := f.openPhysical(ctx, request, plan)
	if err != nil {
		return nil, err
	}
	tap, tapped, err := attachPlaybackTap(ctx, physical, request.FileOutput, plan)
	if err != nil {
		return nil, errors.Join(err, closeHandle(physical))
	}
	effectivePlan := plan
	if tapped {
		effectivePlan.filePlayback = false
	}
	finite, err := f.openFile(ctx, request, effectivePlan)
	if err != nil {
		return nil, errors.Join(err, closeHandle(physical), closeTap(tap))
	}
	handle, err := newHandle(physical, finite, tap, effectivePlan)
	if err != nil {
		return nil, errors.Join(err, closeHandle(physical), closeHandle(finite), closeTap(tap))
	}
	return handle, nil
}

func (f *Factory) openPhysical(ctx context.Context, request devices.Request, plan openPlan) (devices.Handle, error) {
	if !plan.hasPhysical() {
		return nil, nil
	}
	if f.physical == nil {
		return nil, devices.ErrUnavailable
	}
	physicalRequest := request
	physicalRequest.FileInput = nil
	physicalRequest.FileOutput = nil
	physicalRequest.CaptureEnabled = plan.physicalCapture
	physicalRequest.PlaybackEnabled = plan.physicalPlayback
	handle, err := f.physical.Open(ctx, physicalRequest)
	if err != nil {
		return nil, fmt.Errorf("open physical media: %w", err)
	}
	if handle == nil {
		return nil, fmt.Errorf("%w: physical service returned a nil handle", devices.ErrUnavailable)
	}
	return handle, nil
}

func (f *Factory) openFile(ctx context.Context, request devices.Request, plan openPlan) (devices.Handle, error) {
	if !plan.hasFile() {
		return nil, nil
	}
	if f.file == nil {
		return nil, devices.ErrUnavailable
	}
	fileRequest := devices.Request{
		SampleRate:      request.SampleRate,
		Channels:        request.Channels,
		PlaybackProfile: request.PlaybackProfile,
		CaptureEnabled:  plan.fileCapture,
		PlaybackEnabled: plan.filePlayback,
		FileInput:       request.FileInput,
		FileOutput:      request.FileOutput,
	}
	if !plan.filePlayback {
		fileRequest.FileOutput = nil
	}
	handle, err := f.file.Open(ctx, fileRequest)
	if err != nil {
		return nil, fmt.Errorf("open finite media: %w", err)
	}
	if handle == nil {
		return nil, fmt.Errorf("%w: finite service returned a nil handle", devices.ErrUnavailable)
	}
	return handle, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func closeHandle(handle devices.Handle) error {
	if handle == nil {
		return nil
	}
	return handle.Close()
}

type handle struct {
	ports    devices.MediaPorts
	physical devices.Handle
	file     devices.Handle
	fanout   *fanoutPlayback
	tap      *outputTap

	closeOnce sync.Once
	closeErr  error
}

func newHandle(physical, finite devices.Handle, tap *outputTap, plan openPlan) (devices.Handle, error) {
	physicalPorts := mediaOf(physical)
	filePorts := mediaOf(finite)
	if physicalPorts.Capture != nil && filePorts.Capture != nil {
		return nil, fmt.Errorf("%w: physical and finite capture cannot share one media port", devices.ErrInvalidRequest)
	}
	if plan.physicalCapture && physicalPorts.Capture == nil {
		return nil, fmt.Errorf("%w: physical service omitted admitted capture", devices.ErrUnavailable)
	}
	if plan.physicalPlayback && physicalPorts.Playback == nil {
		return nil, fmt.Errorf("%w: physical service omitted admitted playback", devices.ErrUnavailable)
	}
	if plan.fileCapture && filePorts.Capture == nil {
		return nil, fmt.Errorf("%w: finite service omitted admitted capture", devices.ErrUnavailable)
	}
	if plan.filePlayback && filePorts.Playback == nil {
		return nil, fmt.Errorf("%w: finite service omitted admitted playback", devices.ErrUnavailable)
	}
	playback, fanout, err := combinePlayback(physicalPorts.Playback, filePorts.Playback)
	if err != nil {
		return nil, err
	}
	if playback == nil && (plan.physicalPlayback || plan.filePlayback) {
		return nil, fmt.Errorf("%w: admitted playback is unavailable", devices.ErrUnavailable)
	}
	ports := devices.MediaPorts{Capture: firstCapture(physicalPorts.Capture, filePorts.Capture), Playback: playback}
	return &handle{ports: ports, physical: physical, file: finite, fanout: fanout, tap: tap}, nil
}

func mediaOf(handle devices.Handle) devices.MediaPorts {
	if handle == nil {
		return devices.MediaPorts{}
	}
	return handle.Media()
}

func firstCapture(primary, secondary devices.Capture) devices.Capture {
	if primary != nil {
		return primary
	}
	return secondary
}

func combinePlayback(primary, secondary devices.Playback) (devices.Playback, *fanoutPlayback, error) {
	children := make([]devices.Playback, 0, 2)
	if primary != nil {
		children = append(children, primary)
	}
	if secondary != nil {
		children = append(children, secondary)
	}
	switch len(children) {
	case 0:
		return nil, nil, nil
	case 1:
		return children[0], nil, nil
	default:
		fanout, err := newFanoutPlayback(children)
		if err != nil {
			return nil, nil, err
		}
		return fanout, fanout, nil
	}
}

func (h *handle) Media() devices.MediaPorts {
	if h == nil {
		return devices.MediaPorts{}
	}
	return h.ports
}

func (h *handle) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		h.closeErr = errors.Join(closeFanout(h.fanout), closeHandle(h.physical), closeHandle(h.file), closeTap(h.tap))
	})
	return h.closeErr
}

func attachPlaybackTap(ctx context.Context, handle devices.Handle, output *devices.FileOutput, plan openPlan) (*outputTap, bool, error) {
	if !plan.physicalPlayback || !plan.filePlayback || handle == nil || output == nil || output.Sink == nil {
		return nil, false, nil
	}
	playback := handle.Media().Playback
	provider, ok := playback.(devices.PlaybackSamplesObserverProvider)
	if !ok {
		return nil, false, nil
	}
	tap, err := newOutputTap(ctx, output.Sink)
	if err != nil {
		return nil, false, err
	}
	provider.SetPlaybackSamplesObserver(tap.Observe)
	return tap, true, nil
}

func closeTap(tap *outputTap) error {
	if tap == nil {
		return nil
	}
	return tap.Close()
}

func closeFanout(fanout *fanoutPlayback) error {
	if fanout == nil {
		return nil
	}
	return fanout.Close()
}

var _ devices.Service = (*Factory)(nil)
var _ devices.Handle = (*handle)(nil)
