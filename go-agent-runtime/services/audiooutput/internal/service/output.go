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

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiooutput"
	goaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	wavMaxDataSize      = uint64(^uint32(0)) - 36
	audioOutputFileMode = 0o644
)

type output struct {
	mu          sync.Mutex
	path        string
	writer      io.Writer
	sampleRate  int
	deviceBound bool
	loudness    audiooutput.LoudnessProcessor
	observe     func([]byte, messages.StreamMessage)
	sink        goaudio.AudioSink
	closed      bool
	closeOnce   sync.Once
	closeErr    error
}

func newOutput(config audiooutput.Config) (audiooutput.Output, error) {
	if config.Path == "" {
		return nil, fmt.Errorf("%w: output path is required", audiooutput.ErrInvalidConfig)
	}
	if !config.DeviceBound && config.SampleRate <= 0 {
		return nil, fmt.Errorf("%w: sample rate must be positive; got %d Hz", audiooutput.ErrInvalidConfig, config.SampleRate)
	}
	result := &output{
		path:        config.Path,
		writer:      config.Writer,
		sampleRate:  config.SampleRate,
		deviceBound: config.DeviceBound,
		loudness:    config.Loudness,
		observe:     config.ObserveAudioOutput,
	}
	if config.DeviceBound {
		return result, nil
	}
	sink, err := newSinkAtRate(config.Path, config.Writer, config.SampleRate)
	if err != nil {
		return nil, err
	}
	result.sink = sink
	return result, nil
}

func (o *output) WriteDelta(ctx context.Context, content []byte, msg messages.StreamMessage) error {
	if len(content) == 0 {
		return nil
	}
	if o.deviceBound {
		o.observeAudio(content, msg)
		return nil
	}
	if err := codec.ValidatePCM16(content, codec.MaxPCM16Bytes); err != nil {
		return pcm16DeltaError(len(content), err)
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if o.loudness != nil {
		content = o.loudness.ProcessBytes(content)
	}
	samples, err := codec.DecodePCM16(content)
	if err != nil {
		return pcm16DeltaError(len(content), err)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return contract.ErrClosed
	}
	o.observeAudio(content, msg)
	writer, ok := o.sink.(interface {
		WriteSamples(context.Context, []int16) error
	})
	if !ok {
		return fmt.Errorf("PCM16 audio output cannot stream a %d-sample delta", len(samples))
	}
	return writer.WriteSamples(ctx, samples)
}

func (o *output) ObserveDeviceSamples(ctx context.Context, sampleRate int, samples []int16) error {
	if !o.deviceBound {
		return audiooutput.ErrDeviceObserverUnavailable
	}
	if len(samples) == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return contract.ErrClosed
	}
	if o.sink == nil {
		sink, err := newSinkAtRate(o.path, o.writer, sampleRate)
		if err != nil {
			return err
		}
		o.sink = sink
	}
	writer, ok := o.sink.(interface {
		WriteSamples(context.Context, []int16) error
	})
	if !ok {
		return fmt.Errorf("PCM16 device audio output cannot stream a %d-sample chunk", len(samples))
	}
	return writer.WriteSamples(ctx, samples)
}

func (o *output) observeAudio(content []byte, msg messages.StreamMessage) {
	if o.observe != nil {
		o.observe(content, msg)
	}
}

func (o *output) Close() error {
	o.closeOnce.Do(func() {
		o.mu.Lock()
		o.closed = true
		sink := o.sink
		o.mu.Unlock()
		var err error
		if sink != nil {
			err = sink.Close()
		}
		o.mu.Lock()
		o.closeErr = err
		o.mu.Unlock()
	})
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closeErr
}

type sink struct {
	mu         sync.Mutex
	path       string
	raw        goaudio.AudioSink
	writer     io.Writer
	file       *os.File
	wav        bool
	sampleRate int
	samples    uint64
	closed     bool
	closeErr   error
}

var _ goaudio.AudioSink = (*sink)(nil)

func newSinkAtRate(path string, out io.Writer, sampleRate int) (goaudio.AudioSink, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("audio output sample rate must be positive; got %d Hz", sampleRate)
	}
	if uint64(sampleRate)*2 > uint64(^uint32(0)) {
		return nil, fmt.Errorf("audio output sample rate %d Hz exceeds WAV header limits", sampleRate)
	}
	if path == "-" {
		raw, err := goaudio.NewFileSink(path, out)
		if err != nil {
			return nil, err
		}
		return &sink{path: path, raw: raw, writer: out, sampleRate: sampleRate}, nil
	}
	probe, err := goaudio.NewFileSink(path, out)
	if err != nil {
		return nil, err
	}
	if probeErr := probe.Close(); probeErr != nil {
		// The preflight sink is intentionally empty; WAV close reports that
		// expected condition before the real stream is opened below.
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, audioOutputFileMode)
	if err != nil {
		return nil, err
	}
	raw, err := goaudio.NewFileSink("-", file)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	result := &sink{
		path:       path,
		raw:        raw,
		writer:     file,
		file:       file,
		wav:        strings.EqualFold(filepath.Ext(path), ".wav"),
		sampleRate: sampleRate,
	}
	if result.wav {
		if err := result.updateHeaderLocked(); err != nil {
			return nil, errors.Join(err, file.Close())
		}
	}
	return result, nil
}

func (s *sink) WriteFrame(ctx context.Context, frame []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return contract.ErrClosed
	}
	if s.wav && !wavSizeFits(s.samples+uint64(len(frame))) {
		return fmt.Errorf("WAV audio output %q exceeds the 32-bit data chunk limit", s.path)
	}
	if err := s.raw.WriteFrame(ctx, frame); err != nil {
		return sinkError(s.path, "write", err)
	}
	s.samples += uint64(len(frame))
	return s.updateHeaderLocked()
}

func (s *sink) WriteSamples(ctx context.Context, samples []int16) error {
	if err := contextError(ctx); err != nil {
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
	if s.wav && !wavSizeFits(s.samples+uint64(len(samples))) {
		return fmt.Errorf("WAV audio output %q exceeds the 32-bit data chunk limit", s.path)
	}
	encoded := make([]byte, len(samples)*2)
	if err := codec.EncodePCM16Into(encoded, samples); err != nil {
		return sinkError(s.path, "encode", err)
	}
	if err := writeAll(s.writer, encoded); err != nil {
		return sinkError(s.path, "write", err)
	}
	s.samples += uint64(len(samples))
	return s.updateHeaderLocked()
}

func (s *sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	var closeErr error
	if err := s.raw.Close(); err != nil {
		closeErr = errors.Join(closeErr, sinkError(s.path, "close", err))
	}
	if s.wav && s.samples > 0 {
		closeErr = errors.Join(closeErr, sinkError(s.path, "write", s.updateHeaderLocked()))
	}
	if s.file != nil {
		closeErr = errors.Join(closeErr, sinkError(s.path, "close", s.file.Close()))
		if s.wav && s.samples == 0 {
			if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				closeErr = errors.Join(closeErr, sinkError(s.path, "remove", err))
			}
		}
	}
	s.closeErr = closeErr
	return closeErr
}

func sinkError(path, operation string, err error) error {
	if err == nil {
		return nil
	}
	var streamErr *goaudio.StreamError
	if errors.As(err, &streamErr) {
		copyErr := *streamErr
		copyErr.Operation = operation
		copyErr.Path = path
		copyErr.Format = audioFormat(path)
		return &copyErr
	}
	return &goaudio.StreamError{Operation: operation, Path: path, Format: audioFormat(path), Err: err}
}

func audioFormat(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".wav") {
		return "wav"
	}
	return "raw PCM16"
}

func (s *sink) updateHeaderLocked() error {
	if !s.wav {
		return nil
	}
	if !wavSizeFits(s.samples) {
		return fmt.Errorf("WAV audio output %q exceeds the 32-bit data chunk limit", s.path)
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	header, err := wavio.PCM16Header(s.sampleRate, s.samples*2)
	if err != nil {
		return err
	}
	if err := writeAll(s.file, header[:]); err != nil {
		return err
	}
	_, err = s.file.Seek(0, io.SeekEnd)
	return err
}

func wavSizeFits(samples uint64) bool {
	return samples <= wavMaxDataSize/2
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if written < 0 || written > len(data) {
			return fmt.Errorf("%w: writer returned invalid byte count %d", io.ErrShortWrite, written)
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func pcm16DeltaError(byteCount int, err error) error {
	if errors.Is(err, codec.ErrPCM16OddLength) {
		return fmt.Errorf("PCM16 audio delta has odd byte length %d: %w", byteCount, err)
	}
	return fmt.Errorf("PCM16 audio delta: %w", err)
}
