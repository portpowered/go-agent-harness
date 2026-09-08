package mixer

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestMixerMixesPeersAndClips(t *testing.T) {
	mixer := newTestMixer(t, Format{SampleRate: 1000, Channels: 1, FrameDuration: time.Millisecond})
	a, err := mixer.AddInput("alice")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mixer.AddInput("bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{30000}}); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{10000}}); err != nil {
		t.Fatal(err)
	}
	frame := readFrame(t, mixer.Output())
	if !reflect.DeepEqual(frame.Samples, []int16{32767}) {
		t.Fatalf("mixed samples = %v, want clipped sum", frame.Samples)
	}
	if frame.Format.SampleRate != 1000 || frame.Format.Channels != 1 {
		t.Fatalf("mixed format = %+v, want negotiated format", frame.Format)
	}
}

func TestMixerOutputWithSourcesKeepsAttributionSeparateFromEpoch(t *testing.T) {
	mixer := newTestMixer(t, Format{SampleRate: 1000, Channels: 1, FrameDuration: time.Millisecond})
	input, err := mixer.AddInput("alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := input.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{9}, Epoch: 7}); err != nil {
		t.Fatal(err)
	}
	reader := mixer.OutputWithSources()
	mixed, err := reader.ReadMixedFrame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mixed.Sources, []string{"alice"}) {
		t.Fatalf("mixed sources = %v, want alice", mixed.Sources)
	}
	if mixed.Frame.Epoch != 0 || mixed.Frame.StreamID != "mix" {
		t.Fatalf("mixed frame lineage = epoch:%d stream:%q, want independent mix identity", mixed.Frame.Epoch, mixed.Frame.StreamID)
	}
}

func TestMixerPreservesShortResponseTailAndEpochs(t *testing.T) {
	mixer := newTestMixer(t, Format{SampleRate: 1000, Channels: 1, FrameDuration: 4 * time.Millisecond})
	input, err := mixer.AddInput("speaker")
	if err != nil {
		t.Fatal(err)
	}
	if err := input.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{1, 2}, Epoch: 2, EndOfResponse: true}); err != nil {
		t.Fatal(err)
	}
	frame := readFrame(t, mixer.Output())
	if !reflect.DeepEqual(frame.Samples, []int16{1, 2}) || !frame.EndOfResponse || frame.Epoch != 0 {
		t.Fatalf("short frame = %+v, want exact terminal tail in mix epoch domain", frame)
	}
	if frame.StreamID != "mix" || frame.Sequence != 0 || frame.StartSample != 0 {
		t.Fatalf("mixed timeline identity = stream:%q sequence:%d start:%d", frame.StreamID, frame.Sequence, frame.StartSample)
	}
	if err := input.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{9}, Epoch: 1, EndOfResponse: true}); !errors.Is(err, audio.ErrStaleEpoch) {
		t.Fatalf("stale epoch write = %v, want ErrStaleEpoch", err)
	}
	if err := input.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{7}, Epoch: 3, EndOfResponse: true}); err != nil {
		t.Fatal(err)
	}
	frame = readFrame(t, mixer.Output())
	if frame.Samples[0] != 7 || frame.Epoch != 0 {
		t.Fatalf("new epoch frame = %+v, want source epoch excluded from mix", frame)
	}
	if frame.Sequence != 1 || frame.StartSample != 2 {
		t.Fatalf("new mix timeline = sequence:%d start:%d, want 1/2", frame.Sequence, frame.StartSample)
	}
}

func TestMixerFramesProviderPacketsWithoutPadding(t *testing.T) {
	mixer := newTestMixer(t, Format{SampleRate: 1000, Channels: 1, FrameDuration: 4 * time.Millisecond})
	input, err := mixer.AddInput("provider")
	if err != nil {
		t.Fatal(err)
	}
	if err := input.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if err := input.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{3, 4, 5, 6}, EndOfResponse: true}); err != nil {
		t.Fatal(err)
	}
	first := readFrame(t, mixer.Output())
	second := readFrame(t, mixer.Output())
	if !reflect.DeepEqual(first.Samples, []int16{1, 2, 3, 4}) || first.EndOfResponse {
		t.Fatalf("first reframed packet = %+v, want full nonterminal frame", first)
	}
	if !reflect.DeepEqual(second.Samples, []int16{5, 6}) || !second.EndOfResponse {
		t.Fatalf("second reframed packet = %+v, want exact terminal tail", second)
	}
}

func TestMixerPreservesBoundaryWithoutInventingSilence(t *testing.T) {
	mixer := newTestMixer(t, Format{SampleRate: 1000, Channels: 1, FrameDuration: 2 * time.Millisecond})
	input, err := mixer.AddInput("speaker")
	if err != nil {
		t.Fatal(err)
	}
	if err := input.WriteFrame(context.Background(), audio.PCMFrame{EndOfResponse: true}); err != nil {
		t.Fatal(err)
	}
	frame := readFrame(t, mixer.Output())
	if len(frame.Samples) != 0 || !frame.EndOfResponse {
		t.Fatalf("boundary-only frame = %+v, want empty terminal marker", frame)
	}
	if frame.Sequence != 0 || frame.StartSample != 0 {
		t.Fatalf("boundary-only timeline = sequence:%d start:%d, want 0/0", frame.Sequence, frame.StartSample)
	}
}

func TestMixerDoesNotMergePeerBoundariesOrEpochs(t *testing.T) {
	mixer := newTestMixer(t, Format{SampleRate: 1000, Channels: 1, FrameDuration: 2 * time.Millisecond})
	a, err := mixer.AddInput("alice")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mixer.AddInput("bob")
	if err != nil {
		t.Fatal(err)
	}
	frame := audio.PCMFrame{Samples: []int16{3, 4}, EndOfResponse: true, Epoch: 1}
	if err := a.WriteFrame(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	frame = audio.PCMFrame{Samples: []int16{5, 6}, EndOfResponse: true, Epoch: 2}
	if err := b.WriteFrame(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	got := readFrame(t, mixer.Output())
	if got.EndOfResponse {
		t.Fatal("mixed independent source boundaries were merged")
	}
	if got.Epoch != 0 {
		t.Fatalf("mixed epochs = %d, want unknown epoch 0", got.Epoch)
	}
}

func TestMixerDoesNotAttributeBoundaryToAnotherSource(t *testing.T) {
	mixer := newTestMixer(t, Format{SampleRate: 1000, Channels: 1, FrameDuration: 2 * time.Millisecond})
	boundary, err := mixer.AddInput("boundary")
	if err != nil {
		t.Fatal(err)
	}
	speech, err := mixer.AddInput("speech")
	if err != nil {
		t.Fatal(err)
	}
	if err := boundary.WriteFrame(context.Background(), audio.PCMFrame{EndOfResponse: true}); err != nil {
		t.Fatal(err)
	}
	if err := speech.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{5, 6}}); err != nil {
		t.Fatal(err)
	}
	got := readFrame(t, mixer.Output())
	if !reflect.DeepEqual(got.Samples, []int16{5, 6}) || got.EndOfResponse {
		t.Fatalf("mixed boundary marker = %+v, want speech samples without boundary", got)
	}
}

func TestMixerHasBoundedOutputAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mixer, err := New(ctx, clock.Real{}, Config{Format: Format{SampleRate: 1000, Channels: 1, FrameDuration: time.Millisecond}, OutputQueueFrames: 1, InputQueueFrames: 1})
	if err != nil {
		t.Fatal(err)
	}
	input, err := mixer.AddInput("source")
	if err != nil {
		t.Fatal(err)
	}
	if err := input.WriteFrame(ctx, audio.PCMFrame{Samples: []int16{1}}); err != nil {
		t.Fatal(err)
	}
	// The queue is intentionally left unread. Cancellation must still join the
	// cadence worker instead of waiting for an unbounded output consumer.
	cancel()
	if err := mixer.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("close mixer = %v", err)
	}
}

func newTestMixer(t *testing.T, format Format) *Mixer {
	t.Helper()
	mixer, err := New(context.Background(), clock.Real{}, Config{Format: format, InputQueueFrames: 4, OutputQueueFrames: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mixer.Close(); err != nil {
			t.Errorf("mixer.Close(): %v", err)
		}
	})
	return mixer
}

func readFrame(t *testing.T, endpoint audio.InboundMedia) audio.PCMFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := endpoint.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return frame
}

// closeRaceClock pauses after a cadence timer fires but before mixing begins.
// This lets the test close the mixer at the otherwise intermittent boundary.
type closeRaceClock struct {
	*clock.Deterministic
	ctx     context.Context
	ready   chan struct{}
	mixing  chan struct{}
	release chan struct{}
	reads   int
}

func (c *closeRaceClock) Now() time.Time {
	const beforeFirstMix = 3
	c.reads++
	if c.reads == beforeFirstMix {
		close(c.mixing)
		select {
		case <-c.release:
		case <-c.ctx.Done():
		}
	}
	return c.Deterministic.Now()
}

func (c *closeRaceClock) NewTimer(duration time.Duration) clock.Timer {
	timer := c.Deterministic.NewTimer(duration)
	select {
	case c.ready <- struct{}{}:
	default:
	}
	return timer
}

func TestMixerCloseDuringReadyCadenceDoesNotBecomeFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	scheduler := &closeRaceClock{
		Deterministic: clock.NewDeterministic(time.Time{}, time.Millisecond), ctx: ctx,
		ready: make(chan struct{}, 1), mixing: make(chan struct{}), release: make(chan struct{}),
	}
	mix, err := New(ctx, scheduler, Config{Format: Format{SampleRate: 1000, Channels: 1, FrameDuration: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	await := func(signal <-chan struct{}, description string) {
		t.Helper()
		select {
		case <-signal:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", description)
		}
	}
	await(scheduler.ready, "cadence timer")
	scheduler.Advance()
	await(scheduler.mixing, "ready cadence")
	result := make(chan error, 1)
	go func() { result <- mix.Close() }()
	await(mix.ctx.Done(), "close initiation")
	close(scheduler.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("normal close was latched as mixer failure: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Close did not join mixer")
	}
	if err := mix.Err(); err != nil {
		t.Fatalf("closed mixer retained false failure: %v", err)
	}
}
