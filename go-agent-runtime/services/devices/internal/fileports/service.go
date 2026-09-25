// Package fileports admits the finite file and stdin media a live invocation
// attaches to the device service. It owns source/sink opening, legacy framing,
// observation wrapping, pacing, and joined cleanup.
package fileports

import (
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const stdinPath = "-"

// Service is the stateless finite media admission service.
type Service struct{}

// New returns the finite media admission service.
func New() *Service { return &Service{} }

var _ devices.FileMediaService = (*Service)(nil)

// OpenFileMedia opens every requested source and sink. It returns a nil
// handle when the request names no media.
func (s *Service) OpenFileMedia(request devices.FileMediaRequest) (devices.FileMediaHandle, error) {
	if request.Input == nil && len(request.InputTurns) == 0 && len(request.Interruptions) == 0 && request.OutputPath == "" {
		return nil, nil
	}
	if err := request.Pacing.Validate(); err != nil {
		return nil, err
	}
	labels := resolvedLabels(request.Labels)
	handle := &handle{}
	if err := openInputs(handle, request, labels); err != nil {
		return nil, err
	}
	if request.OutputPath != "" {
		sink, err := openOutput(request)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("%s %q: %w", labels.Output, request.OutputPath, err), handle.Close())
		}
		handle.media.Output = &devices.FileOutput{Sink: sink, SampleRate: request.OutputSampleRate, Continuous: request.OutputPath == stdinPath}
	}
	observeSources(&handle.media, request)
	applyPacing(&handle.media, request)
	return handle, nil
}

func openInputs(handle *handle, request devices.FileMediaRequest, labels devices.FileMediaLabels) error {
	if request.Input != nil {
		source, rate, err := openAudioInput(*request.Input, labels.Input)
		if err != nil {
			return err
		}
		if request.FrameInput {
			source = &frameAudioSource{source: source}
		}
		path := request.Input.Path
		handle.media.Input = &devices.FileInput{Source: source, SampleRate: rate, Pace: path != stdinPath, Continuous: path == stdinPath}
	}
	for index, path := range request.InputTurns {
		source, rate, err := openAudioInput(devices.FileMediaSource{Path: path}, labels.Input)
		if err != nil {
			return errors.Join(fmt.Errorf("%s %d %q: %w", labels.InputTurn, index+1, path, err), handle.Close())
		}
		handle.media.InputTurns = append(handle.media.InputTurns, devices.FileInput{Source: source, SampleRate: rate, Pace: path != stdinPath, Continuous: path == stdinPath})
	}
	for index, path := range request.Interruptions {
		source, rate, err := openAudioInput(devices.FileMediaSource{Path: path}, labels.Input)
		if err != nil {
			return errors.Join(fmt.Errorf("%s %d %q: %w", labels.Interruption, index+1, path, err), handle.Close())
		}
		handle.media.Interruptions = append(handle.media.Interruptions, devices.FileInput{Source: source, SampleRate: rate, Pace: true})
	}
	return nil
}

func openOutput(request devices.FileMediaRequest) (audio.AudioSink, error) {
	rate := request.OutputSampleRate
	if rate <= 0 {
		rate = audio.SampleRate
	}
	if request.NegotiateOutputRate || request.Input != nil || len(request.InputTurns) > 0 || len(request.Interruptions) > 0 {
		return newNegotiatedFileSink(request.OutputPath, request.Stdout, rate)
	}
	return audio.NewFileSinkAtSampleRate(request.OutputPath, request.Stdout, rate)
}

// observeSources wraps the primary input and every input turn after framing,
// so an observer sees exactly the samples the capture worker reads.
func observeSources(media *devices.FileMedia, request devices.FileMediaRequest) {
	if request.ObserveSource == nil {
		return
	}
	if media.Input != nil {
		media.Input.Source = request.ObserveSource(media.Input.Source, media.Input.SampleRate)
	}
	for index := range media.InputTurns {
		media.InputTurns[index].Source = request.ObserveSource(media.InputTurns[index].Source, media.InputTurns[index].SampleRate)
	}
}

// applyPacing gives every file input the request's pacing policy. Real time
// uses the host scheduler unchanged; a speed multiplier runs the same pacing
// on an accelerated view of that scheduler; Unpaced releases frames without
// waiting. Stdin inputs were admitted unpaced and stay that way.
func applyPacing(media *devices.FileMedia, request devices.FileMediaRequest) {
	scheduler := pacingScheduler(request.Scheduler, request.Pacing)
	apply := func(input *devices.FileInput) {
		input.Scheduler = scheduler
		if request.Pacing.Unpaced {
			input.Pace = false
		}
	}
	if media.Input != nil {
		apply(media.Input)
	}
	for index := range media.InputTurns {
		apply(&media.InputTurns[index])
	}
	for index := range media.Interruptions {
		apply(&media.Interruptions[index])
	}
}

func resolvedLabels(labels devices.FileMediaLabels) devices.FileMediaLabels {
	if labels.Input == "" {
		labels.Input = "audio input"
	}
	if labels.InputTurn == "" {
		labels.InputTurn = "audio input turn"
	}
	if labels.Interruption == "" {
		labels.Interruption = "audio interruption"
	}
	if labels.Output == "" {
		labels.Output = "audio output"
	}
	return labels
}

// handle owns the admitted media and releases it exactly once.
type handle struct {
	media    devices.FileMedia
	once     sync.Once
	closeErr error
}

func (h *handle) Media() devices.FileMedia {
	media := h.media
	media.InputTurns = append([]devices.FileInput(nil), h.media.InputTurns...)
	media.Interruptions = append([]devices.FileInput(nil), h.media.Interruptions...)
	return media
}

func (h *handle) Close() error {
	h.once.Do(func() { h.closeErr = closeMedia(h.media) })
	return h.closeErr
}

func closeMedia(media devices.FileMedia) error {
	var errs []error
	if media.Output != nil {
		errs = appendClose(errs, media.Output.Sink)
	}
	if media.Input != nil {
		errs = appendClose(errs, media.Input.Source)
	}
	for index := range media.InputTurns {
		errs = appendClose(errs, media.InputTurns[index].Source)
	}
	for index := range media.Interruptions {
		errs = appendClose(errs, media.Interruptions[index].Source)
	}
	return errors.Join(errs...)
}

func appendClose(errs []error, closer interface{ Close() error }) []error {
	if closer == nil {
		return errs
	}
	return append(errs, closer.Close())
}
