package mediagate

import (
	"context"
	"sync"

	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// interruptionFilter keeps the frames of an interrupted provider response
// inside the gate.
//
// The inbound bridge reads provider frames ahead of the device, so when the
// provider reports a barge-in the gate can already hold frames the provider
// has handed out. The provider discards its own queue at that moment; this
// filter applies the same rule to the gate's queue and to the frame the bridge
// is pushing. Frames are matched by response identity, not position, so audio
// of a response that starts after the interruption is never dropped.
//
// The provider calls StartPlayback for each response as it hands out the
// response's first frame, and it calls the interruption methods while no
// further frame can be handed out. The identities recorded between two
// interruptions are therefore exactly the responses whose frames may still be
// inside the gate when the second interruption lands.
// ReadFrame returns the next queued frame. Frames of a response the provider
// has interrupted are discarded here, so they never leave the gate.
func (p *inboundPort) ReadFrame(ctx context.Context) (sharedaudio.PCMFrame, error) {
	if ctx == nil {
		return sharedaudio.PCMFrame{}, mediaContextRequired()
	}
	for {
		frame, err := p.readQueuedFrame(ctx)
		if err != nil || p.interruptions.admit(frame) {
			return frame, err
		}
	}
}

type interruptionFilter struct {
	mu sync.Mutex
	// started lists, in FIFO order, the responses handed out by the provider
	// since the last interruption whose frames may still be in the gate.
	started []sharedaudio.PlaybackResponse
	// interrupted holds the responses cut off by the latest interruption.
	// Response identities are unique, so the set only needs replacing at the
	// next interruption.
	interrupted map[sharedaudio.PlaybackResponse]struct{}
}

func (f *interruptionFilter) start(response sharedaudio.PlaybackResponse) {
	if !response.HasIdentity() {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := len(f.started); n > 0 && f.started[n-1] == response {
		return
	}
	f.started = append(f.started, response)
}

// interrupt marks every response handed out since the last interruption, and
// the named responses, as interrupted.
func (f *interruptionFilter) interrupt(responses ...sharedaudio.PlaybackResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interrupted = make(map[sharedaudio.PlaybackResponse]struct{}, len(f.started)+len(responses))
	for _, response := range f.started {
		f.interrupted[response] = struct{}{}
	}
	f.started = nil
	f.addInterruptedLocked(responses...)
}

// addInterrupted extends the current interruption with responses reported by
// the playback controller after the interruption was applied.
func (f *interruptionFilter) addInterrupted(responses ...sharedaudio.PlaybackResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addInterruptedLocked(responses...)
}

func (f *interruptionFilter) addInterruptedLocked(responses ...sharedaudio.PlaybackResponse) {
	for _, response := range responses {
		if !response.HasIdentity() {
			continue
		}
		if f.interrupted == nil {
			f.interrupted = make(map[sharedaudio.PlaybackResponse]struct{})
		}
		f.interrupted[response] = struct{}{}
	}
}

// admit reports whether frame may leave the gate.
func (f *interruptionFilter) admit(frame sharedaudio.PCMFrame) bool {
	response := frame.PlaybackResponse
	if !response.HasIdentity() {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, interrupted := f.interrupted[response]; interrupted {
		return false
	}
	// Frames leave in provider order. Responses started before this one have
	// no frames left in the gate, so they need not be remembered.
	for index, started := range f.started {
		if started == response {
			f.started = append(f.started[:0], f.started[index:]...)
			break
		}
	}
	return true
}

// wrap returns the controller the provider uses. It records the provider's
// playback boundaries and forwards every call to the device controller.
func (f *interruptionFilter) wrap(controller sharedaudio.PlaybackController) sharedaudio.PlaybackController {
	if controller == nil {
		return nil
	}
	base := filteringPlaybackController{inner: controller, filter: f}
	if active, ok := controller.(sharedaudio.ActivePlaybackController); ok {
		return filteringActivePlaybackController{filteringPlaybackController: base, active: active}
	}
	return base
}

type filteringPlaybackController struct {
	inner  sharedaudio.PlaybackController
	filter *interruptionFilter
}

func (c filteringPlaybackController) StartPlayback(response sharedaudio.PlaybackResponse) {
	c.filter.start(response)
	c.inner.StartPlayback(response)
}

func (c filteringPlaybackController) InterruptPlayback(response sharedaudio.PlaybackResponse) (int, bool) {
	c.filter.interrupt(response)
	return c.inner.InterruptPlayback(response)
}

type filteringActivePlaybackController struct {
	filteringPlaybackController
	active sharedaudio.ActivePlaybackController
}

func (c filteringActivePlaybackController) InterruptActivePlayback() (sharedaudio.PlaybackInterruption, bool) {
	c.filter.interrupt()
	interruption, ok := c.active.InterruptActivePlayback()
	// The device reports the audible response, which it may know even when
	// the provider handed it out before this gate's controller was installed.
	c.filter.addInterrupted(interruption.PlaybackResponse)
	return interruption, ok
}
