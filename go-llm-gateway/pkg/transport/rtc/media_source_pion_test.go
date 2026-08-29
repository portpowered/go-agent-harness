package rtc

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

type visualContextKey struct{}

func TestVisualLookPreservesCallerDeadlineIdentityWhenTrackNeverAttaches(t *testing.T) {
	inbound := newPionInbound(nil, "go2rtc://fixture/api/ws?src=camera")
	inbound.setVideoNegotiated(true)
	defer inbound.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := inbound.Look(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked visual look error = %v, want context deadline", err)
	}
}

func TestVisualObservationAndLookAliasContracts(t *testing.T) {
	var nilContext context.Context
	available := VisualObservation{Status: VisualObservationAvailable, Bytes: []byte{1}}
	if !available.Available() {
		t.Fatal("non-empty available observation was not available")
	}
	for _, observation := range []VisualObservation{
		{Status: VisualObservationAvailable},
		{Status: VisualObservationUnavailable, Bytes: []byte{1}},
	} {
		if observation.Available() {
			t.Fatalf("observation without available visual data was available: %#v", observation)
		}
	}

	var nilStream *MediaStream
	observation, err := nilStream.Look(nilContext)
	if err != nil || observation.Source != "" || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack {
		t.Fatalf("nil stream look = %#v, error = %v", observation, err)
	}
	fallback := &MediaStream{Capabilities: MediaCapabilities{Source: "fallback-source"}}
	observation, err = fallback.Observe(nilContext)
	if err != nil || observation.Source != "fallback-source" || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack {
		t.Fatalf("fallback stream observe = %#v, error = %v", observation, err)
	}
	delegatedContext := context.WithValue(context.Background(), visualContextKey{}, "look")
	delegated := &MediaStream{look: func(ctx context.Context) (VisualObservation, error) {
		if ctx != delegatedContext {
			t.Fatalf("look callback context = %v, want caller context", ctx)
		}
		return available, nil
	}}
	if observation, err := delegated.Observe(delegatedContext); err != nil || !observation.Available() {
		t.Fatalf("delegated stream observe = %#v, error = %v", observation, err)
	}

	visualContext, cancel := boundedVisualContext(nilContext)
	if visualContext == nil {
		t.Fatal("nil visual context was not replaced")
	}
	cancel()
	if err := callerContextError(nilContext); err != nil {
		t.Fatalf("nil caller context error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(callerContextError(canceled), context.Canceled) {
		t.Fatalf("canceled caller context error = %v", callerContextError(canceled))
	}

	for name, look := range map[string]func(context.Context, string) (VisualObservation, error){
		"look media source":    LookMediaSource,
		"look source":          LookSource,
		"observe media source": ObserveMediaSource,
		"observe source":       ObserveSource,
	} {
		observation, err := look(context.Background(), "bad://source")
		if !errors.Is(err, ErrMalformedSource) || observation.Source != "" || observation.Status != "" || observation.Reason != "" || observation.MediaType != "" || len(observation.Bytes) != 0 {
			t.Fatalf("%s = %#v/%v", name, observation, err)
		}
	}
	invalid := MediaSource{identity: "invalid"}
	if _, err := invalid.Look(context.Background()); !errors.Is(err, ErrMalformedSource) {
		t.Fatalf("invalid source look error = %v", err)
	}
	if _, err := invalid.Observe(context.Background()); !errors.Is(err, ErrMalformedSource) {
		t.Fatalf("invalid source observe error = %v", err)
	}
}

func TestPionInboundLookHandlesNilTrackStates(t *testing.T) {
	var nilContext context.Context
	noTrack := newPionInbound(nil, "no-track")
	observation, err := noTrack.Look(nilContext)
	if err != nil || observation.Source != "no-track" || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack {
		t.Fatalf("nil-context no-track look = %#v, error = %v", observation, err)
	}
	noTrack.Close()

	waiting := newPionInbound(nil, "waiting")
	waiting.setVideoNegotiated(true)
	waiting.mu.Lock()
	waiting.videoMediaType = "video/H264"
	waiting.mu.Unlock()
	close(waiting.videoReady)
	waiting.visuals <- pionVisualFrame{}
	waiting.visuals <- pionVisualFrame{bytes: []byte{1, 2, 3}}
	observation, err = waiting.Look(context.Background())
	if err != nil || !observation.Available() || observation.Source != "waiting" || observation.MediaType != "video/H264" || !bytes.Equal(observation.Bytes, []byte{1, 2, 3}) {
		t.Fatalf("ready visual look = %#v, error = %v", observation, err)
	}
	waiting.Close()

	canceled := newPionInbound(nil, "canceled")
	canceled.setVideoNegotiated(true)
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := canceled.Look(canceledContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled visual look error = %v", err)
	}
	canceled.Close()

	closedBeforeAttach := newPionInbound(nil, "closed-before-attach")
	closedBeforeAttach.setVideoNegotiated(true)
	closedBeforeAttach.Close()
	if observation, err := closedBeforeAttach.Look(context.Background()); err != nil || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack {
		t.Fatalf("closed pre-attach visual look = %#v, error = %v", observation, err)
	}

	closedAfterAttach := newPionInbound(nil, "closed-after-attach")
	closedAfterAttach.mu.Lock()
	closedAfterAttach.videoNegotiated = true
	closedAfterAttach.videoSeen = true
	closedAfterAttach.mu.Unlock()
	closedAfterAttach.Close()
	if observation, err := closedAfterAttach.Look(context.Background()); err != nil || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack {
		t.Fatalf("closed attached visual look = %#v, error = %v", observation, err)
	}

	brokenRTSP := &rtspInbound{videoChannel: 2}
	if observation, err := brokenRTSP.Look(context.Background()); err != nil || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack {
		t.Fatalf("uninitialized RTSP visual look = %#v, error = %v", observation, err)
	}
}

func TestPionInboundLookReturnsUnavailableAfterObservationTimeout(t *testing.T) {
	inbound := newPionInbound(nil, "timeout")
	inbound.setVideoNegotiated(true)
	defer inbound.Close()
	started := time.Now()
	observation, err := inbound.Look(context.Background())
	if err != nil || observation.Source != "timeout" || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack || len(observation.Bytes) != 0 {
		t.Fatalf("timed-out visual look = %#v, error = %v", observation, err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("visual look exceeded its bound: %v", time.Since(started))
	}
}

func TestPionInboundAttachIgnoresNilAndDuplicateAudio(t *testing.T) {
	inbound := newPionInbound(nil)
	inbound.attach(nil)
	inbound.attachAudio(nil)
	inbound.attachVideo(nil)
	inbound.mu.Lock()
	inbound.audioSeen = true
	inbound.mu.Unlock()
	inbound.attach(&webrtc.TrackRemote{})
	inbound.Close()
}
