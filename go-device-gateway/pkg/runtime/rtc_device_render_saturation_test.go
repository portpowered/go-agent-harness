package runtime

import (
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// TestRenderObservationSaturationStillAdvancesTheDeviceClock covers a render
// worker that has fallen behind: once the bounded queue of pending render
// correlations is full, a device callback is accounted immediately instead of
// queued, so the device sample clock never stalls and the uncorrelated range
// is published as an explicit underflow.
func TestRenderObservationSaturationStillAdvancesTheDeviceClock(t *testing.T) {
	sink := newPlaybackCommandSink(t)
	state := &sink.playbackObservations
	subscription := &RTCDevicePlaybackObservationSubscription{
		events: make(chan RTCDevicePlaybackObservation, 1),
		done:   make(chan struct{}),
		state:  state,
	}
	callback := playbackCommandFrame(7)

	state.mu.Lock()
	state.subscription = subscription
	clockBefore := state.deviceClock
	for range maxRTCDevicePlaybackObservationSegments - len(state.pendingRenders) {
		state.pendingRenders = append(state.pendingRenders, rtcDevicePlaybackRender{id: ^uint64(0)})
	}
	state.mu.Unlock()
	if id := state.beginRender(sink.id, audio.SampleRate, callback); id != 0 {
		t.Fatalf("beginRender on a full queue = %d, want 0 (untracked)", id)
	}

	sink.observeDeviceRender(audio.SampleRate, callback)

	state.mu.Lock()
	advanced := state.deviceClock - clockBefore
	state.mu.Unlock()
	if advanced != uint64(len(callback)) {
		t.Fatalf("device clock advanced %d samples, want %d", advanced, len(callback))
	}
	event := <-subscription.events
	if event.Kind != RTCDevicePlaybackUnderflow || event.SampleCount != len(callback) || !strings.Contains(event.Reason, "saturated") {
		t.Fatalf("observation = %+v, want a saturated-queue underflow for the whole callback", event)
	}
	state.mu.Lock()
	state.subscription = nil
	state.pendingRenders = nil
	state.mu.Unlock()
}

// TestRenderWorkerDrainsQueuedRendersOnStop completes every render already
// queued when the sink stops, rather than abandoning them. With many queued
// renders the stop case is selected while work is still pending in all but a
// 2^-64 fraction of runs.
func TestRenderWorkerDrainsQueuedRendersOnStop(t *testing.T) {
	base := newPlaybackCommandSink(t)
	const queued = 64
	worker := &RTCDeviceSink{
		sink:       base.sink,
		renderWork: make(chan uint64, queued),
		renderStop: make(chan struct{}),
		renderDone: make(chan struct{}),
	}
	for id := range uint64(queued) {
		worker.renderWork <- id + 1
	}
	close(worker.renderStop)
	worker.runRenderObservations()
	if pending := len(worker.renderWork); pending != 0 {
		t.Fatalf("%d renders left unprocessed at stop", pending)
	}
	select {
	case <-worker.renderDone:
	default:
		t.Fatal("render worker did not report done")
	}
}
