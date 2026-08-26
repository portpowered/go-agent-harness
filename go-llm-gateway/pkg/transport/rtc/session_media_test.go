package rtc_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestSessionMediaFramesInboundPCMAndFlushesPartialFrame(t *testing.T) {
	media := rtc.NewSessionMedia(func(context.Context, rtc.PCMFrame) error { return nil })
	endpoints := media.Endpoints()

	first := make([]int16, rtc.DefaultSessionMediaFrameSamples-1)
	for index := range first {
		first[index] = int16(index + 1) //nolint:gosec // bounded test sample
	}
	if err := media.PushInbound(first); err != nil {
		t.Fatalf("push first inbound samples: %v", err)
	}
	readCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := endpoints.Inbound.ReadFrame(readCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read before complete frame = %v, want context deadline", err)
	}

	if err := media.PushInbound([]int16{99}); err != nil {
		t.Fatalf("push final sample: %v", err)
	}
	frame, err := endpoints.Inbound.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("read complete frame: %v", err)
	}
	want := append(append([]int16(nil), first...), 99)
	if !reflect.DeepEqual(frame.Samples, want) {
		t.Fatalf("framed samples differ: got %d samples, want %d", len(frame.Samples), len(want))
	}

	partial := []int16{7, 8, 9}
	if err := media.PushInbound(partial); err != nil {
		t.Fatalf("push partial samples: %v", err)
	}
	if err := media.FlushInbound(); err != nil {
		t.Fatalf("flush partial samples: %v", err)
	}
	flushed, err := endpoints.Inbound.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("read flushed frame: %v", err)
	}
	if len(flushed.Samples) != rtc.DefaultSessionMediaFrameSamples || !reflect.DeepEqual(flushed.Samples[:len(partial)], partial) {
		t.Fatalf("flushed frame prefix = %v, want %v", flushed.Samples[:len(partial)], partial)
	}
	for _, sample := range flushed.Samples[len(partial):] {
		if sample != 0 {
			t.Fatalf("flushed frame padding sample = %d, want zero", sample)
		}
	}

	if err := media.Close(); err != nil {
		t.Fatalf("close session media: %v", err)
	}
	if _, err := endpoints.Inbound.ReadFrame(context.Background()); !errors.Is(err, rtc.ErrSessionMediaClosed) {
		t.Fatalf("read after close = %v, want ErrSessionMediaClosed", err)
	}
}

func TestSessionMediaOutboundCopiesSamplesAndStopsOnClose(t *testing.T) {
	var got []int16
	media := rtc.NewSessionMedia(func(_ context.Context, frame rtc.PCMFrame) error {
		got = frame.Samples
		return nil
	})
	endpoints := media.Endpoints()
	want := []int16{-4, 0, 12}
	if err := endpoints.Outbound.WriteFrame(context.Background(), rtc.PCMFrame{Samples: want}); err != nil {
		t.Fatalf("write outbound frame: %v", err)
	}
	want[0] = 100
	if !reflect.DeepEqual(got, []int16{-4, 0, 12}) {
		t.Fatalf("writer received samples = %v, want copied PCM", got)
	}
	if err := media.Close(); err != nil {
		t.Fatalf("close session media: %v", err)
	}
	if err := endpoints.Outbound.WriteFrame(context.Background(), rtc.PCMFrame{Samples: []int16{1}}); !errors.Is(err, rtc.ErrSessionMediaClosed) {
		t.Fatalf("write after close = %v, want ErrSessionMediaClosed", err)
	}
}

func TestSessionMediaInvalidCallsAndWriterFailures(t *testing.T) {
	var nilMedia *rtc.SessionMedia
	if endpoints := nilMedia.Endpoints(); endpoints.Inbound != nil || endpoints.Outbound != nil {
		t.Fatalf("nil media endpoints = %#v, want both nil", endpoints)
	}
	if err := nilMedia.PushInbound([]int16{1}); !errors.Is(err, rtc.ErrSessionMediaClosed) {
		t.Fatalf("nil media PushInbound = %v, want ErrSessionMediaClosed", err)
	}
	if err := nilMedia.FlushInbound(); !errors.Is(err, rtc.ErrSessionMediaClosed) {
		t.Fatalf("nil media FlushInbound = %v, want ErrSessionMediaClosed", err)
	}
	nilMedia.FailInbound(nil)
	if err := nilMedia.Close(); err != nil {
		t.Fatalf("nil media Close = %v", err)
	}

	media := rtc.NewSessionMedia(nil)
	endpoints := media.Endpoints()
	var nilContext context.Context
	if err := endpoints.Outbound.WriteFrame(nilContext, rtc.PCMFrame{}); !errors.Is(err, rtc.ErrSessionMediaEmptyFrame) {
		t.Fatalf("empty outbound frame = %v, want ErrSessionMediaEmptyFrame", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := endpoints.Outbound.WriteFrame(ctx, rtc.PCMFrame{Samples: []int16{1}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled outbound frame = %v, want context.Canceled", err)
	}
	if err := endpoints.Outbound.WriteFrame(nilContext, rtc.PCMFrame{Samples: []int16{1}}); !errors.Is(err, rtc.ErrSessionMediaNoWriter) {
		t.Fatalf("outbound without writer = %v, want ErrSessionMediaNoWriter", err)
	}
	if err := endpoints.Outbound.Close(); err != nil {
		t.Fatalf("outbound endpoint Close = %v", err)
	}
	if err := endpoints.Outbound.Close(); err != nil {
		t.Fatalf("repeated outbound endpoint Close = %v", err)
	}
	if err := endpoints.Outbound.WriteFrame(context.Background(), rtc.PCMFrame{Samples: []int16{1}}); !errors.Is(err, rtc.ErrSessionMediaClosed) {
		t.Fatalf("outbound after endpoint close = %v, want ErrSessionMediaClosed", err)
	}

	wantErr := errors.New("provider writer failed")
	media = rtc.NewSessionMedia(func(context.Context, rtc.PCMFrame) error { return wantErr })
	if err := media.Endpoints().Outbound.WriteFrame(context.Background(), rtc.PCMFrame{Samples: []int16{1}}); !errors.Is(err, wantErr) {
		t.Fatalf("writer failure = %v, want %v", err, wantErr)
	}
}

func TestSessionMediaInboundFailuresAndEndpointLifecycle(t *testing.T) {
	media := rtc.NewSessionMedia(func(context.Context, rtc.PCMFrame) error { return nil })
	endpoints := media.Endpoints()
	if err := media.PushInbound(nil); err != nil {
		t.Fatalf("empty inbound push = %v", err)
	}
	if err := media.FlushInbound(); err != nil {
		t.Fatalf("empty inbound flush = %v", err)
	}

	frame := make([]int16, rtc.DefaultSessionMediaFrameSamples)
	frame[0] = 42
	if err := media.PushInbound(frame); err != nil {
		t.Fatalf("inbound push = %v", err)
	}
	var nilContext context.Context
	got, err := endpoints.Inbound.ReadFrame(nilContext)
	if err != nil {
		t.Fatalf("nil-context inbound read = %v", err)
	}
	if len(got.Samples) == 0 || got.Samples[0] != 42 {
		t.Fatalf("nil-context inbound frame = %v, want first sample 42", got.Samples)
	}

	wantErr := errors.New("provider inbound failed")
	media.FailInbound(wantErr)
	if _, err := endpoints.Inbound.ReadFrame(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("inbound failure = %v, want %v", err, wantErr)
	}
	if err := media.PushInbound([]int16{1}); !errors.Is(err, wantErr) {
		t.Fatalf("push after inbound failure = %v, want %v", err, wantErr)
	}
	if err := media.FlushInbound(); !errors.Is(err, wantErr) {
		t.Fatalf("flush after inbound failure = %v, want %v", err, wantErr)
	}

	if err := endpoints.Inbound.Close(); err != nil {
		t.Fatalf("inbound endpoint Close = %v", err)
	}
	if err := endpoints.Inbound.Close(); err != nil {
		t.Fatalf("repeated inbound endpoint Close = %v", err)
	}
	if _, err := endpoints.Inbound.ReadFrame(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("inbound read after terminal close = %v, want terminal error %v", err, wantErr)
	}
	if err := media.Close(); err != nil {
		t.Fatalf("media close after inbound endpoint close = %v", err)
	}

	eofMedia := rtc.NewSessionMedia(func(context.Context, rtc.PCMFrame) error { return nil })
	eofEndpoints := eofMedia.Endpoints()
	eofMedia.FailInbound(nil)
	if _, err := eofEndpoints.Inbound.ReadFrame(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("nil inbound failure = %v, want io.EOF", err)
	}
	if err := eofMedia.Close(); err != nil {
		t.Fatalf("media Close = %v", err)
	}
	if err := eofMedia.Close(); err != nil {
		t.Fatalf("repeated media Close = %v", err)
	}
}

func TestSessionMediaInboundQueueDropsOldestFrames(t *testing.T) {
	media := rtc.NewSessionMedia(func(context.Context, rtc.PCMFrame) error { return nil })
	const queuedFrames = 257
	samples := make([]int16, queuedFrames*rtc.DefaultSessionMediaFrameSamples)
	for frameIndex := 0; frameIndex < queuedFrames; frameIndex++ {
		samples[frameIndex*rtc.DefaultSessionMediaFrameSamples] = int16(frameIndex + 1) //nolint:gosec // bounded test marker
	}
	if err := media.PushInbound(samples); err != nil {
		t.Fatalf("push queued inbound frames = %v", err)
	}
	frame, err := media.Endpoints().Inbound.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("read retained inbound frame = %v", err)
	}
	if got, want := frame.Samples[0], int16(2); got != want {
		t.Fatalf("first retained frame marker = %d, want %d after dropping oldest", got, want)
	}
	if err := media.Close(); err != nil {
		t.Fatalf("close queued media = %v", err)
	}
}
