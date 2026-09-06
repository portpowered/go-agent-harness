package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	public "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
)

type traceRun struct {
	trace *recording.Trace
	path  string
}

// Stage audio evidence alongside the destination, so the session recorder
// can validate and atomically publish its initially empty bundle directory.
func prepareTrace(request *public.Request, options *SessionRunOptions, source clock.Source) (*traceRun, error) {
	if !request.TraceAudio && request.RecordDirectory == "" {
		return nil, nil
	}
	if source == nil {
		return nil, errors.New("session trace clock is required")
	}
	path := "audio-trace"
	if request.RecordDirectory != "" {
		parent := filepath.Dir(filepath.Clean(request.RecordDirectory))
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return nil, err
		}
		var err error
		path, err = os.MkdirTemp(parent, ".session-audio-trace-")
		if err != nil {
			return nil, err
		}
	}
	trace, err := recording.NewTrace(path, source)
	if err != nil {
		return nil, err
	}
	binding := &options.RTCDeviceBinding
	priorPreGate := binding.PreGateSamplesObserver
	binding.PreGateSamplesObserver = func(rate int, samples []int16) {
		trace.CaptureMicrophonePreGate(rate, samples)
		if priorPreGate != nil {
			priorPreGate(rate, samples)
		}
	}
	priorUploaded := binding.UploadedSamplesObserver
	binding.UploadedSamplesObserver = func(rate int, samples []int16) {
		trace.CaptureMicrophoneUploaded(rate, samples)
		if priorUploaded != nil {
			priorUploaded(rate, samples)
		}
	}
	priorPlayback := binding.PlaybackSamplesObserver
	binding.PlaybackSamplesObserver = func(ctx context.Context, rate int, samples []int16) error {
		traceErr := trace.CaptureSpeakerEnqueued(ctx, rate, samples)
		if priorPlayback != nil {
			return errors.Join(traceErr, priorPlayback(ctx, rate, samples))
		}
		return traceErr
	}
	priorRendered := binding.RenderedSamplesObserver
	binding.RenderedSamplesObserver = func(rate int, samples []int16) {
		trace.CaptureSpeakerRendered(rate, samples)
		if priorRendered != nil {
			priorRendered(rate, samples)
		}
	}
	options.RuntimeObserver = CombineSessionRuntimeObservers(options.RuntimeObserver, TraceRuntimeObserver{Trace: trace, Redactions: traceCredentials(request)})
	binding.RenderedSamplesUnavailable = func() {
		if options.RuntimeObserver != nil {
			options.RuntimeObserver.ObserveSessionRuntime(SessionRuntimeObservation{
				Kind:  SessionRuntimeObservationAudioRenderTapUnavailable,
				Clean: false,
				Error: "selected audio backend does not expose physical render callbacks",
			})
		}
	}
	return &traceRun{trace: trace, path: path}, nil
}

func (r *traceRun) finish(bundle string, published bool) error {
	closeErr := r.trace.Close()
	if bundle == "" {
		return closeErr
	}
	if !published {
		return errors.Join(closeErr, fmt.Errorf("session failed; audio evidence retained at %s", r.path))
	}
	destination := filepath.Join(bundle, "audio-trace")
	if _, err := os.Lstat(destination); err == nil {
		return errors.Join(closeErr, fmt.Errorf("audio trace destination exists; evidence retained at %s", r.path))
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(closeErr, err)
	}
	if err := os.Rename(r.path, destination); err != nil {
		return errors.Join(closeErr, fmt.Errorf("attach audio trace to bundle: %w; evidence retained at %s", err, r.path))
	}
	return closeErr
}

func traceCredentials(request *public.Request) []string {
	values := []string{request.APIKey}
	if cfg := request.LoadedConfig; cfg != nil {
		if cfg.Model.OpenAI != nil {
			values = append(values, cfg.Model.OpenAI.APIKey)
		}
		if cfg.Model.Grok != nil {
			values = append(values, cfg.Model.Grok.APIKey)
		}
	}
	return values
}
