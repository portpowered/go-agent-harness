package livehost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestNegotiatedFileSinkUsesPhysicalPlaybackRate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device.wav")
	sink, err := newNegotiatedFileSink(path, nil, 24_000)
	if err != nil {
		t.Fatalf("newNegotiatedFileSink: %v", err)
	}
	want := []int16{2, -3, 5}
	writer, ok := sink.(interface {
		WriteSamplesAtRate(context.Context, int, []int16) error
	})
	if !ok {
		t.Fatalf("sink type = %T, want negotiated rate writer", sink)
	}
	if err := writer.WriteSamplesAtRate(context.Background(), 16_000, want); err != nil {
		t.Fatalf("WriteSamplesAtRate: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read WAV: %v", err)
	}
	rate, got, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read WAV: %v", err)
	}
	if rate != 16_000 || !reflect.DeepEqual(got, want) {
		t.Fatalf("WAV = rate %d samples %v, want rate 16000 samples %v", rate, got, want)
	}
}

func TestNegotiatedFileSinkRemovesEmptyWAV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.wav")
	sink, err := newNegotiatedFileSink(path, nil, 24_000)
	if err != nil {
		t.Fatalf("newNegotiatedFileSink: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty WAV stat error = %v, want removed artifact", err)
	}
}

func TestNegotiatedFileSinkRejectsRateChangesAndClosedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-change.wav")
	sink, err := newNegotiatedFileSink(path, nil, 24_000)
	if err != nil {
		t.Fatalf("newNegotiatedFileSink: %v", err)
	}
	writer, ok := sink.(interface {
		WriteSamplesAtRate(context.Context, int, []int16) error
	})
	if !ok {
		t.Fatalf("sink type = %T, want negotiated rate writer", sink)
	}
	if err := writer.WriteSamplesAtRate(context.Background(), 16_000, []int16{7, -8}); err != nil {
		t.Fatalf("first WriteSamplesAtRate: %v", err)
	}
	if err := writer.WriteSamplesAtRate(context.Background(), 24_000, []int16{9}); err == nil {
		t.Fatal("second WriteSamplesAtRate succeeded after negotiated rate changed")
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	samples, ok := sink.(audio.SampleSink)
	if !ok {
		t.Fatalf("sink type = %T, want audio.SampleSink", sink)
	}
	if err := samples.WriteSamples(context.Background(), []int16{10}); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("WriteSamples after Close = %v, want audio.ErrClosed", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read WAV: %v", err)
	}
	rate, got, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read WAV: %v", err)
	}
	if rate != 16_000 || !reflect.DeepEqual(got, []int16{7, -8}) {
		t.Fatalf("WAV = rate %d samples %v, want rate 16000 samples [7 -8]", rate, got)
	}
}
