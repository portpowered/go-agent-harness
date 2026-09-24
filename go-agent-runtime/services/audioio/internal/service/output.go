package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const fileOutputWAVMaxDataSize = uint64(^uint32(0)) - 36

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
		return nil, errors.New("audio output context is required")
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

type pcm16FileOutput struct {
	mu         sync.Mutex
	path       string
	raw        sharedaudio.AudioSink
	writer     io.Writer
	file       *os.File
	wav        bool
	sampleRate int
	samples    uint64
	closed     bool
	closeErr   error
}

var _ audioio.PCM16FileOutput = (*pcm16FileOutput)(nil)

func (s *Service) OpenPCM16FileOutput(ctx context.Context, request audioio.PCM16FileOutputRequest) (audioio.PCM16FileOutput, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if request.SampleRate <= 0 {
		return nil, fmt.Errorf("audio output sample rate must be positive; got %d Hz", request.SampleRate)
	}
	if uint64(request.SampleRate)*2 > uint64(^uint32(0)) {
		return nil, fmt.Errorf("audio output sample rate %d Hz exceeds WAV header limits", request.SampleRate)
	}
	if request.Path == "-" {
		raw, err := sharedaudio.NewFileSink(request.Path, request.Writer)
		if err != nil {
			return nil, err
		}
		return &pcm16FileOutput{path: request.Path, raw: raw, writer: request.Writer, sampleRate: request.SampleRate}, nil
	}
	probe, err := sharedaudio.NewFileSink(request.Path, request.Writer)
	if err != nil {
		return nil, err
	}
	_ = probe.Close()
	file, err := os.OpenFile(request.Path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	raw, err := sharedaudio.NewFileSink("-", file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	sink := &pcm16FileOutput{
		path: request.Path, raw: raw, writer: file, file: file,
		wav: strings.EqualFold(filepath.Ext(request.Path), ".wav"), sampleRate: request.SampleRate,
	}
	if sink.wav {
		if err := sink.updateWAVHeaderLocked(); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return sink, nil
}

func (s *pcm16FileOutput) WriteFrame(ctx context.Context, frame []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return contract.ErrClosed
	}
	if s.wav && !fileOutputWAVSizeFits(s.samples+uint64(len(frame))) {
		return fmt.Errorf("WAV audio output %q exceeds the 32-bit data chunk limit", s.path)
	}
	if err := s.raw.WriteFrame(ctx, frame); err != nil {
		return fileOutputError(s.path, "write", err)
	}
	s.samples += uint64(len(frame))
	return s.updateWAVHeaderLocked()
}

func (s *pcm16FileOutput) WriteSamples(ctx context.Context, samples []int16) error {
	if err := fileOutputContextError(ctx); err != nil {
		return err
	}
	if len(samples) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return contract.ErrClosed
	}
	if s.wav && !fileOutputWAVSizeFits(s.samples+uint64(len(samples))) {
		return fmt.Errorf("WAV audio output %q exceeds the 32-bit data chunk limit", s.path)
	}
	encoded := make([]byte, len(samples)*2)
	if err := codec.EncodePCM16Into(encoded, samples); err != nil {
		return fileOutputError(s.path, "encode", err)
	}
	if err := writeFileOutputAll(s.writer, encoded); err != nil {
		return fileOutputError(s.path, "write", err)
	}
	s.samples += uint64(len(samples))
	return s.updateWAVHeaderLocked()
}

func (s *pcm16FileOutput) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	var closeErr error
	if err := s.raw.Close(); err != nil {
		closeErr = errors.Join(closeErr, fileOutputError(s.path, "close", err))
	}
	if s.wav && s.samples > 0 {
		closeErr = errors.Join(closeErr, fileOutputError(s.path, "write", s.updateWAVHeaderLocked()))
	}
	if s.file != nil {
		closeErr = errors.Join(closeErr, fileOutputError(s.path, "close", s.file.Close()))
		if s.wav && s.samples == 0 {
			if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				closeErr = errors.Join(closeErr, fileOutputError(s.path, "remove", err))
			}
		}
	}
	s.closeErr = closeErr
	return closeErr
}

func (s *pcm16FileOutput) updateWAVHeaderLocked() error {
	if !s.wav {
		return nil
	}
	if !fileOutputWAVSizeFits(s.samples) {
		return fmt.Errorf("WAV audio output %q exceeds the 32-bit data chunk limit", s.path)
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	header, err := wavio.PCM16Header(s.sampleRate, s.samples*2)
	if err != nil {
		return err
	}
	if err := writeFileOutputAll(s.file, header[:]); err != nil {
		return err
	}
	_, err = s.file.Seek(0, io.SeekEnd)
	return err
}

func fileOutputWAVSizeFits(samples uint64) bool {
	return samples <= fileOutputWAVMaxDataSize/2
}

var _ audioio.Output = (*output)(nil)
