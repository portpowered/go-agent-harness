package embedding_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestExternalLiveHostPreservesPCMAndCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider := newEmbeddedLiveProvider()
	defer closeForTest(t, provider)
	var starts atomic.Int32
	host := sessionwire.NewLiveService(sessionwire.LiveDependencies{
		Clock: func() time.Time { return time.Unix(123, 0) },
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			starts.Add(1)
			return provider, nil
		},
	})
	handle, err := host.OpenLive(ctx, session.LiveRequest{SessionID: "external-audio"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, handle)
	if starts.Load() != 0 {
		t.Fatal("OpenLive started the provider")
	}
	if err := handle.Start(ctx); err != nil {
		t.Fatal(err)
	}
	ports := handle.Media()
	frame := audio.PCMFrame{Samples: []int16{10, -20, 30}, Format: audio.PCM16DeviceFormat(24000), StreamID: "capture", Sequence: 4, StartSample: 90, EndOfResponse: true}
	if err := ports.Outbound.WriteFrame(ctx, frame); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-provider.outbound:
		if !reflect.DeepEqual(got, frame) {
			t.Fatalf("outbound=%+v; want %+v", got, frame)
		}
	case <-ctx.Done():
		t.Fatal("provider did not receive outbound PCM")
	}
	if err := provider.media.PushInbound(frame.Samples); err != nil {
		t.Fatal(err)
	}
	if err := provider.media.FlushInbound(); err != nil {
		t.Fatal(err)
	}
	got, err := ports.Inbound.ReadFrame(ctx)
	if err != nil || !reflect.DeepEqual(got.Samples, frame.Samples) || !got.EndOfResponse {
		t.Fatalf("inbound=%+v err=%v; want exact terminal tail", got, err)
	}
	cause := errors.New("external host stopped")
	handle.Cancel(cause)
	if err := handle.Wait(); !errors.Is(err, cause) {
		t.Fatalf("terminal cause=%v; want %v", err, cause)
	}
	assertLiveTimeline(t, handle.Events(), time.Unix(123, 0))
}

func assertLiveTimeline(t *testing.T, events <-chan session.LiveEvent, timestamp time.Time) {
	t.Helper()
	var previous uint64
	var terminal bool
	for event := range events {
		if event.Sequence <= previous || !event.Timestamp.Equal(timestamp) {
			t.Fatalf("event sequence/time=%d/%v; want sequence>%d and injected time %v", event.Sequence, event.Timestamp, previous, timestamp)
		}
		previous = event.Sequence
		terminal = event.Kind == string(session.LiveEventTerminal)
	}
	if !terminal {
		t.Fatal("event stream did not finish with terminal evidence")
	}
}

func TestExternalLiveMediaRetainsPlaybackControl(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider := newEmbeddedLiveProvider()
	defer closeForTest(t, provider)
	host := sessionwire.NewLiveService(sessionwire.LiveDependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return provider, nil
		},
	})
	handle, err := host.OpenLive(ctx, session.LiveRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, handle)
	controlled, ok := handle.Media().Inbound.(audio.PlaybackControlledInbound)
	if !ok {
		t.Fatal("public media proxy cannot bind the device playback controller required for barge-in")
	}
	controller := &embeddedPlaybackController{started: make(chan audio.PlaybackResponse, 2)}
	controlled.SetPlaybackController(controller)
	if err := handle.Start(ctx); err != nil {
		t.Fatal(err)
	}
	response := audio.PlaybackResponse{ResponseID: "response", ItemID: "audible-item"}
	provider.media.StartInboundResponse(response)
	// Offer 30ms so the controller's 17ms consumed boundary is valid.
	if err := provider.media.PushInbound(make([]int16, 720)); err != nil {
		t.Fatal(err)
	}
	if err := provider.media.FlushInbound(); err != nil {
		t.Fatal(err)
	}
	if _, err := controlled.ReadFrame(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-controller.started:
		if got != response {
			t.Fatalf("playback response=%+v; want %+v", got, response)
		}
	case <-ctx.Done():
		t.Fatal("device controller did not receive the audible response identity")
	}
	interrupted, ok := provider.media.InterruptInbound()
	if !ok || interrupted.PlaybackResponse != response || interrupted.AudioEndMS != 17 {
		t.Fatalf("interruption=%+v ok=%v; want audible item at device-consumed17ms", interrupted, ok)
	}
}
