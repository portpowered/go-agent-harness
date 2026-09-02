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

func TestSessionMediaAt24kFramesThirtyMillisecondsAndPreservesPartialResponse(t *testing.T) {
	media := rtc.NewSessionMediaAtRate(func(context.Context, rtc.PCMFrame) error { return nil }, 24000)
	endpoints := media.Endpoints()
	defer func() { _ = media.Close() }()

	complete := make([]int16, 720)
	sample := int16(1)
	for index := range complete {
		complete[index] = sample
		sample++
	}
	if err := media.PushInbound(complete); err != nil {
		t.Fatalf("push complete 24 kHz frame: %v", err)
	}
	frame, err := endpoints.Inbound.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("read complete 24 kHz frame: %v", err)
	}
	if frame.EndOfResponse || !reflect.DeepEqual(frame.Samples, complete) {
		t.Fatalf("24 kHz frame = %d samples, boundary=%t; want exact 720-sample frame", len(frame.Samples), frame.EndOfResponse)
	}

	partial := append([]int16(nil), complete[:480]...)
	if err := media.PushInbound(partial); err != nil {
		t.Fatalf("push partial 24 kHz response: %v", err)
	}
	if err := media.FlushInbound(); err != nil {
		t.Fatalf("flush partial 24 kHz response: %v", err)
	}
	frame, err = endpoints.Inbound.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("read partial 24 kHz response: %v", err)
	}
	if !frame.EndOfResponse || !reflect.DeepEqual(frame.Samples, partial) {
		t.Fatalf("partial 24 kHz frame = %d samples, boundary=%t; want exact 480-sample response boundary", len(frame.Samples), frame.EndOfResponse)
	}
}

func TestSessionMediaServerVADDiscardsBacklogAndReturnsDeviceCursor(t *testing.T) {
	media := rtc.NewSessionMediaAtRate(func(context.Context, rtc.PCMFrame) error { return nil }, 24000)
	defer func() { _ = media.Close() }()
	controlled, ok := media.Endpoints().Inbound.(rtc.PlaybackControlledInbound)
	if !ok {
		t.Fatal("SessionMedia inbound does not expose playback control")
	}
	controller := &sessionMediaPlaybackController{audioEndMS: 1234}
	controlled.SetPlaybackController(controller)

	response := rtc.PlaybackResponse{ResponseID: "resp-old", ItemID: "item-old", ContentIndex: 2}
	media.StartInboundResponse(response)
	if controller.started != response {
		t.Fatalf("started response = %+v, want %+v", controller.started, response)
	}
	if err := media.PushInbound(make([]int16, 720*3)); err != nil {
		t.Fatalf("push old response backlog: %v", err)
	}
	first, err := controlled.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("read first old response frame: %v", err)
	}
	if first.PlaybackResponse != response {
		t.Fatalf("first frame response = %+v, want %+v", first.PlaybackResponse, response)
	}

	interruption, ok := media.InterruptInbound()
	if !ok {
		t.Fatal("server-VAD interruption did not return a device cursor")
	}
	if interruption.PlaybackResponse != response || interruption.AudioEndMS != 1234 {
		t.Fatalf("interruption = %+v, want response %+v at 1234 ms", interruption, response)
	}
	readCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := controlled.ReadFrame(readCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read discarded old backlog = %v, want deadline", err)
	}

	// OpenAI may deliver an already-in-flight delta and audio.done after the
	// speech_started event. They belong to the interrupted response and must
	// not reopen playback or publish a response boundary.
	media.StartInboundResponse(response)
	if err := media.PushInbound(make([]int16, 720)); err != nil {
		t.Fatalf("push late interrupted response delta: %v", err)
	}
	if err := media.FlushInbound(); err != nil {
		t.Fatalf("flush late interrupted response: %v", err)
	}
	lateCtx, cancelLate := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelLate()
	if _, err := controlled.ReadFrame(lateCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read late interrupted response = %v, want deadline", err)
	}
	if controller.started != response {
		t.Fatalf("late interrupted response restarted playback as %+v", controller.started)
	}

	replacement := rtc.PlaybackResponse{ResponseID: "resp-new", ItemID: "item-new"}
	media.StartInboundResponse(replacement)
	if err := media.PushInbound(make([]int16, 720)); err != nil {
		t.Fatalf("push replacement response: %v", err)
	}
	next, err := controlled.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("read replacement response: %v", err)
	}
	if next.PlaybackResponse != replacement {
		t.Fatalf("replacement frame response = %+v, want %+v", next.PlaybackResponse, replacement)
	}
}

type sessionMediaPlaybackController struct {
	started     rtc.PlaybackResponse
	interrupted rtc.PlaybackResponse
	audioEndMS  int
}

func (c *sessionMediaPlaybackController) StartPlayback(response rtc.PlaybackResponse) {
	c.started = response
}

func (c *sessionMediaPlaybackController) InterruptPlayback(response rtc.PlaybackResponse) (int, bool) {
	c.interrupted = response
	return c.audioEndMS, true
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
	if err := media.PushInbound([]int16{1}); !errors.Is(err, rtc.ErrSessionMediaClosed) {
		t.Fatalf("push after inbound endpoint close = %v, want ErrSessionMediaClosed", err)
	}
	if err := media.FlushInbound(); !errors.Is(err, rtc.ErrSessionMediaClosed) {
		t.Fatalf("flush after inbound endpoint close = %v, want ErrSessionMediaClosed", err)
	}
	media.FailInbound(errors.New("late inbound failure"))
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

func TestSessionMediaInboundQueuePreservesFramesBeyondLegacyLimit(t *testing.T) {
	media := rtc.NewSessionMedia(func(context.Context, rtc.PCMFrame) error { return nil })
	const queuedFrames = 257
	samples := make([]int16, queuedFrames*rtc.DefaultSessionMediaFrameSamples)
	for frameIndex := 0; frameIndex < queuedFrames; frameIndex++ {
		samples[frameIndex*rtc.DefaultSessionMediaFrameSamples] = int16(frameIndex + 1) //nolint:gosec // bounded test marker
	}
	if err := media.PushInbound(samples); err != nil {
		t.Fatalf("push queued inbound frames = %v", err)
	}
	for frameIndex := 0; frameIndex < queuedFrames; frameIndex++ {
		frame, err := media.Endpoints().Inbound.ReadFrame(context.Background())
		if err != nil {
			t.Fatalf("read inbound frame %d: %v", frameIndex, err)
		}
		if got, want := frame.Samples[0], int16(frameIndex+1); got != want { //nolint:gosec // bounded test marker
			t.Fatalf("inbound frame %d marker = %d, want %d", frameIndex, got, want)
		}
	}
	if err := media.Close(); err != nil {
		t.Fatalf("close queued media = %v", err)
	}
}

func TestSessionMediaInboundBacklogLimitFailsInsteadOfDroppingPCM(t *testing.T) {
	media := rtc.NewSessionMediaAtRate(func(context.Context, rtc.PCMFrame) error { return nil }, 24000)
	defer func() { _ = media.Close() }()
	frame := make([]int16, 720)
	for frameIndex := 0; ; frameIndex++ {
		err := media.PushInbound(frame)
		if errors.Is(err, rtc.ErrSessionMediaInboundBacklog) {
			if frameIndex < 590 {
				t.Fatalf("backlog rejected frame %d before retaining the 590-frame eac8 response", frameIndex)
			}
			break
		}
		if err != nil {
			t.Fatalf("push backlog frame %d: %v", frameIndex, err)
		}
		if frameIndex > 100_000 {
			t.Fatal("inbound backlog has no defensive limit")
		}
	}

	first, err := media.Endpoints().Inbound.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("read retained frame after explicit backlog failure: %v", err)
	}
	if !reflect.DeepEqual(first.Samples, frame) {
		t.Fatal("explicit backlog failure changed already retained PCM")
	}
}
