package mediagate

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"

	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const interruptionTestRate = 16000

// interruptionTestFrameSamples is the provider's 30 ms frame at 16 kHz.
const interruptionTestFrameSamples = interruptionTestRate * 30 / 1000

// recordingPlaybackController stands in for the device clock that the host
// registers on the gate before the provider connects.
type recordingPlaybackController struct {
	mu          sync.Mutex
	calls       []string
	interrupted sharedaudio.PlaybackResponse
}

func (c *recordingPlaybackController) record(call string) {
	c.mu.Lock()
	c.calls = append(c.calls, call)
	c.mu.Unlock()
}

func (c *recordingPlaybackController) callsSnapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

func (c *recordingPlaybackController) StartPlayback(response sharedaudio.PlaybackResponse) {
	c.record("start " + response.ItemID)
}

func (c *recordingPlaybackController) InterruptPlayback(response sharedaudio.PlaybackResponse) (int, bool) {
	c.record("interrupt " + response.ItemID)
	return 0, true
}

type recordingActivePlaybackController struct {
	recordingPlaybackController
}

func (c *recordingActivePlaybackController) InterruptActivePlayback() (sharedaudio.PlaybackInterruption, bool) {
	c.record("interrupt-active")
	c.mu.Lock()
	defer c.mu.Unlock()
	return sharedaudio.PlaybackInterruption{PlaybackResponse: c.interrupted}, true
}

func interruptionTestPCM(frames int, base int16) []int16 {
	samples := make([]int16, frames*interruptionTestFrameSamples)
	for index := range samples {
		samples[index] = base + int16(index%97)
	}
	return samples
}

type recordingDevice interface {
	sharedaudio.PlaybackController
	callsSnapshot() []string
}

// TestGateDiscardsInterruptedResponseFramesAlreadyBridged drives the provider
// media adapter through a barge-in while the gate already holds frames of the
// interrupted response. The provider drops its own queue at the interruption;
// the frames the gate had already bridged must not reach the device either,
// and the next response must play in full.
func TestGateDiscardsInterruptedResponseFramesAlreadyBridged(t *testing.T) {
	t.Run("active playback controller", func(t *testing.T) {
		device := &recordingActivePlaybackController{recordingPlaybackController{interrupted: interruptionTestInterrupted()}}
		runGateBargeIn(t, device, []string{"start item-interrupted", "interrupt-active", "start item-next"})
	})
	t.Run("playback controller", func(t *testing.T) {
		runGateBargeIn(t, &recordingPlaybackController{}, []string{"start item-interrupted", "interrupt item-interrupted", "start item-next"})
	})
}

// interruptionTestInterrupted is the response cut off by the barge-in.
func interruptionTestInterrupted() sharedaudio.PlaybackResponse {
	return sharedaudio.PlaybackResponse{ResponseID: "response-interrupted", ItemID: "item-interrupted"}
}

// interruptionTestNext is the response that starts after the barge-in.
func interruptionTestNext() sharedaudio.PlaybackResponse {
	return sharedaudio.PlaybackResponse{ResponseID: "response-next", ItemID: "item-next"}
}

func runGateBargeIn(t *testing.T, controller recordingDevice, wantCalls []string) {
	t.Helper()
	provider := sharedaudio.NewSessionMediaAtRate(nil, interruptionTestRate)
	gate := New(func(err error) { t.Errorf("media failure reported: %v", err) })
	defer func() {
		if err := gate.Close(); err != nil {
			t.Errorf("close gate: %v", err)
		}
	}()
	device := gate.Endpoints().Inbound
	controlled, ok := device.(sharedaudio.PlaybackControlledInbound)
	if !ok {
		t.Fatalf("gate inbound %T does not accept a playback controller", device)
	}
	controlled.SetPlaybackController(controller)

	const bridgedFrames = 3
	observed := make(chan sharedaudio.PCMFrame, 16)
	gate.SetFrameObserver(func(direction FrameDirection, frame sharedaudio.PCMFrame) {
		if direction == FrameInbound {
			observed <- frame
		}
	})
	gate.Attach(t.Context(), provider.Endpoints())

	provider.StartInboundResponse(interruptionTestInterrupted())
	if err := provider.PushInbound(interruptionTestPCM(bridgedFrames, 1000)); err != nil {
		t.Fatalf("push interrupted response: %v", err)
	}
	awaitBridgedFrames(t, observed, bridgedFrames)

	if _, ok := provider.InterruptInbound(); !ok {
		t.Fatal("provider interruption did not reach the device controller")
	}
	provider.StartInboundResponse(interruptionTestNext())
	want := interruptionTestPCM(2, 3000)
	if err := provider.PushInbound(want); err != nil {
		t.Fatalf("push next response: %v", err)
	}
	if err := provider.FlushInbound(); err != nil {
		t.Fatalf("flush next response: %v", err)
	}
	provider.FailInbound(io.EOF)

	if got := readDeviceUntilEOF(t, device); !reflect.DeepEqual(got, want) {
		t.Fatalf("device read %d samples, want exactly the next response's %d samples", len(got), len(want))
	}
	if calls := controller.callsSnapshot(); !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("device controller calls = %q, want %q", calls, wantCalls)
	}
}

// awaitBridgedFrames waits until the gate queue holds count frames of the
// response that is about to be interrupted.
func awaitBridgedFrames(t *testing.T, observed <-chan sharedaudio.PCMFrame, count int) {
	t.Helper()
	for range count {
		select {
		case frame := <-observed:
			if frame.PlaybackResponse != interruptionTestInterrupted() {
				t.Fatalf("bridged frame response = %+v, want %+v", frame.PlaybackResponse, interruptionTestInterrupted())
			}
		case <-t.Context().Done():
			t.Fatal("interrupted response was not bridged")
		}
	}
}

// readDeviceUntilEOF reads what the device would play and requires every frame
// to belong to the response that follows the interruption.
func readDeviceUntilEOF(t *testing.T, device sharedaudio.InboundMedia) []int16 {
	t.Helper()
	var got []int16
	for {
		frame, err := device.ReadFrame(t.Context())
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Fatalf("device read: %v", err)
		}
		if frame.PlaybackResponse != interruptionTestNext() {
			t.Fatalf("device read a frame of %+v after the interruption, want only %+v", frame.PlaybackResponse, interruptionTestNext())
		}
		got = append(got, frame.Samples...)
	}
}

// TestGateDiscardsInterruptedFrameBridgedAfterInterruption covers a frame the
// bridge read from the provider before the interruption but pushed into the
// gate after it: it still belongs to the interrupted response and must not
// reach the device.
func TestGateDiscardsInterruptedFrameBridgedAfterInterruption(t *testing.T) {
	interrupted, next := interruptionTestInterrupted(), interruptionTestNext()
	port := newInboundPort(8)
	device := &recordingActivePlaybackController{recordingPlaybackController{interrupted: interrupted}}
	port.SetPlaybackController(device)
	controller, ok := port.getPlaybackController().(sharedaudio.ActivePlaybackController)
	if !ok {
		t.Fatalf("provider controller %T lost the active interruption capability", port.getPlaybackController())
	}

	ctx := context.Background()
	controller.StartPlayback(interrupted)
	if err := port.push(ctx, sharedaudio.PCMFrame{Samples: []int16{1}, PlaybackResponse: interrupted}, nil); err != nil {
		t.Fatalf("push queued frame: %v", err)
	}
	if _, ok := controller.InterruptActivePlayback(); !ok {
		t.Fatal("interruption was not applied")
	}
	if err := port.push(ctx, sharedaudio.PCMFrame{Samples: []int16{2}, EndOfResponse: true, PlaybackResponse: interrupted}, nil); err != nil {
		t.Fatalf("push in-flight frame: %v", err)
	}
	controller.StartPlayback(next)
	want := sharedaudio.PCMFrame{Samples: []int16{3}, PlaybackResponse: next}
	if err := port.push(ctx, want, nil); err != nil {
		t.Fatalf("push next frame: %v", err)
	}

	got, err := port.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("device read: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("device read %+v, want the next response's frame %+v", got, want)
	}
}

// TestGateInterruptionKeepsFramesWithoutResponseIdentity keeps ordinary media
// that carries no provider response identity playable across an interruption.
func TestGateInterruptionKeepsFramesWithoutResponseIdentity(t *testing.T) {
	port := newInboundPort(4)
	port.SetPlaybackController(&recordingPlaybackController{})
	controller := port.getPlaybackController()
	controller.StartPlayback(sharedaudio.PlaybackResponse{ResponseID: "response", ItemID: "item"})
	want := sharedaudio.PCMFrame{Samples: []int16{5}}
	if err := port.push(context.Background(), want, nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	controller.InterruptPlayback(sharedaudio.PlaybackResponse{ResponseID: "response", ItemID: "item"})
	got, err := port.ReadFrame(context.Background())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadFrame = (%+v, %v), want %+v", got, err, want)
	}
}
