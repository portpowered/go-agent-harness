package service

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type output struct {
	sink       sharedaudio.AudioSink
	processor  *sharedaudio.Processor
	continuous bool
	normalizer *sharedaudio.LoudnessNormalizer
	ended      bool
	lineage    sharedaudio.PCMFrame
	closeOnce  sync.Once
	closeErr   error
	used       bool
	useMu      sync.Mutex
}

func newOutput(ctx context.Context, request audioio.OutputRequest) (audioio.Output, error) {
	if request.Sink == nil {
		return nil, errors.New("audio output sink is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	providerRate, sinkRate := request.ProviderRate, request.SinkRate
	if providerRate <= 0 {
		providerRate = audioio.DefaultSampleRate
	}
	if sinkRate <= 0 {
		sinkRate = sharedaudio.SampleRate
	}
	processor, err := sharedaudio.NewProcessor(sharedaudio.PCM16DeviceFormat(providerRate), sharedaudio.PCM16DeviceFormat(sinkRate), sharedaudio.FrameSize)
	if err != nil {
		return nil, err
	}
	gainDB := request.GainDB
	if gainDB == 0 && request.Voice != "" {
		gainDB = sharedaudio.VoiceLoudnessGainDB(request.Voice)
	}
	var normalizer *sharedaudio.LoudnessNormalizer
	if gainDB != 0 {
		normalizer = sharedaudio.NewLoudnessNormalizer(sharedaudio.LoudnessNormalizerConfig{GainDB: gainDB})
	}
	return &output{sink: request.Sink, processor: processor, continuous: request.Continuous, normalizer: normalizer}, nil
}

func (o *output) Write(ctx context.Context, frame sharedaudio.PCMFrame) error {
	if o == nil || o.sink == nil || o.processor == nil {
		return errors.New("audio output is unavailable")
	}
	if ctx == nil {
		return errors.New("audio output context is required")
	}
	return o.consumeFrame(ctx, frame)
}

func (o *output) Pump(ctx context.Context, inbound sharedaudio.InboundMedia) error {
	if o == nil || o.sink == nil || o.processor == nil {
		return errors.New("audio output is unavailable")
	}
	if inbound == nil {
		return errors.New("audio output inbound media is nil")
	}
	if ctx == nil {
		return errors.New("audio output context is required")
	}
	o.useMu.Lock()
	if o.used {
		o.useMu.Unlock()
		return errors.New("audio output pump has already been started")
	}
	o.used = true
	o.useMu.Unlock()
	for {
		frame, err := inbound.ReadFrame(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
				return o.flush(ctx)
			}
			return err
		}
		if err := o.consumeFrame(ctx, frame); err != nil {
			return err
		}
	}
}

func (o *output) consumeFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
	if err := o.prepareFrame(frame); err != nil {
		return err
	}
	frames, err := o.processFrame(frame)
	if err != nil {
		return err
	}
	if err := o.writeFrames(ctx, frames, true); err != nil {
		return err
	}
	o.ended = frame.EndOfResponse
	o.lineage = frame
	o.lineage.Samples = nil
	return nil
}

func (o *output) prepareFrame(frame sharedaudio.PCMFrame) error {
	if frame.Epoch != o.lineage.Epoch {
		o.ended = true
	}
	if o.ended {
		if _, err := o.processor.Reset(); err != nil {
			return err
		}
		o.ended = false
	}
	return nil
}

func (o *output) processFrame(frame sharedaudio.PCMFrame) ([]sharedaudio.PCMFrame, error) {
	process := o.processor.Process
	if o.continuous {
		process = o.processor.ProcessAvailable
	}
	return process(frame)
}

func (o *output) writeFrames(ctx context.Context, frames []sharedaudio.PCMFrame, applyGain bool) error {
	for _, item := range frames {
		if len(item.Samples) == 0 {
			continue
		}
		if applyGain && o.normalizer != nil {
			item.Samples = o.normalizer.Process(item.Samples)
		}
		if err := o.writeFrame(ctx, item.Samples); err != nil {
			return err
		}
	}
	return nil
}

func (o *output) writeFrame(ctx context.Context, samples []int16) error {
	if sink, ok := o.sink.(sharedaudio.SampleSink); ok {
		return sink.WriteSamples(ctx, samples)
	}
	if len(samples) != sharedaudio.FrameSize {
		return errors.New("audio output sink does not support partial frame")
	}
	return o.sink.WriteFrame(ctx, samples)
}

func (o *output) flush(ctx context.Context) error {
	if o == nil || o.ended {
		return nil
	}
	frame := o.lineage
	frame.EndOfResponse = true
	frames, err := o.processor.Process(frame)
	if err != nil {
		return err
	}
	if err := o.writeFrames(ctx, frames, false); err != nil {
		return err
	}
	o.ended = true
	return nil
}

func (o *output) Close() error {
	if o == nil {
		return nil
	}
	o.closeOnce.Do(func() {
		if o.sink != nil {
			o.closeErr = o.sink.Close()
		}
	})
	return o.closeErr
}

var _ audioio.Output = (*output)(nil)
