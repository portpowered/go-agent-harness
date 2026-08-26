package rtc_test

import (
	"context"
	"errors"
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
