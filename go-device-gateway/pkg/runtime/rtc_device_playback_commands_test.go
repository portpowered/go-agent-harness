package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// newPlaybackCommandSink opens a sink on the virtual output. The virtual
// device renders nothing on its own, so admitted samples stay queued until a
// test discards them, which makes every queue count below exact.
func newPlaybackCommandSink(t *testing.T) *RTCDeviceSink {
	t.Helper()
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("virtual registry: %v", err)
	}
	sink, err := NewRTCDeviceSink(registry, "virtual:output")
	if err != nil {
		t.Fatalf("open sink: %v", err)
	}
	t.Cleanup(func() { closeSinkBoundarySink(t, sink) })
	return sink
}

func playbackCommandFrame(value int16) []int16 {
	frame := make([]int16, audio.FrameSize)
	for index := range frame {
		frame[index] = value
	}
	return frame
}

func writePlaybackCommandFrame(t *testing.T, sink *RTCDeviceSink, value int16) {
	t.Helper()
	if err := sink.WritePlayback(t.Context(), playbackCommandFrame(value)); err != nil {
		t.Fatalf("write playback frame %d: %v", value, err)
	}
}

// submitEpochDiscard queues a loop interrupt through the same bounded port the
// audio subsystem uses and waits for the worker's receipt.
func submitEpochDiscard(t *testing.T, sink *RTCDeviceSink, id, epoch uint64) audio.PlaybackReceipt {
	t.Helper()
	receipts, err := sink.PlaybackCommands().TrySubmitWithReceipt(audio.Command{ID: id, Epoch: epoch, Kind: audio.CommandInterrupt})
	if err != nil {
		t.Fatalf("submit epoch %d discard: %v", epoch, err)
	}
	return <-receipts
}

func assertPlaybackState(t *testing.T, sink *RTCDeviceSink, label string, wantGeneration uint64, wantBlocked bool) {
	t.Helper()
	generation, blocked := sink.PlaybackState()
	if generation != wantGeneration || blocked != wantBlocked {
		t.Fatalf("%s: playback state = (generation %d, blocked %t), want (%d, %t)", label, generation, blocked, wantGeneration, wantBlocked)
	}
	if epoch := sink.PlaybackBuffer().Snapshot().Epoch; epoch != wantGeneration {
		t.Fatalf("%s: loop snapshot epoch = %d, want %d", label, epoch, wantGeneration)
	}
}

func assertQueuedSamples(t *testing.T, sink *RTCDeviceSink, label string, want int) {
	t.Helper()
	if got := sink.PlaybackStats().QueuedSamples; got != want {
		t.Fatalf("%s: queued samples = %d, want %d", label, got, want)
	}
}

// TestRTCDeviceSinkEpochDiscardBlocksPlaybackUntilResume walks the playback
// pause/resume state machine through the command worker: an admitted loop
// interrupt flushes queued audio and adopts the loop's epoch, frames written
// while blocked never reach the device, and only a resume reopens admission
// under a fresh generation.
func TestRTCDeviceSinkEpochDiscardBlocksPlaybackUntilResume(t *testing.T) {
	sink := newPlaybackCommandSink(t)
	// The worker notifies the observer after the caller's receipt, so read
	// observations from a channel rather than a shared slice.
	observed := make(chan audio.PlaybackReceipt, 2)
	sink.SetPlaybackReceiptObserver(func(receipt audio.PlaybackReceipt) { observed <- receipt })

	writePlaybackCommandFrame(t, sink, 11)
	writePlaybackCommandFrame(t, sink, 12)
	assertQueuedSamples(t, sink, "before interrupt", 2*audio.FrameSize)

	receipt := submitEpochDiscard(t, sink, 41, 5)
	if !receipt.Applied || receipt.Err != nil || receipt.CommandID != 41 || receipt.Epoch != 5 {
		t.Fatalf("epoch discard receipt = %+v, want applied command 41 at epoch 5", receipt)
	}
	assertQueuedSamples(t, sink, "after interrupt", 0)
	if got := sink.PlaybackStats().DiscardedSamples; got != uint64(2*audio.FrameSize) {
		t.Fatalf("discarded samples = %d, want %d", got, 2*audio.FrameSize)
	}
	assertPlaybackState(t, sink, "after interrupt", 5, true)

	// A producer racing the interrupt is told nothing went wrong, but its
	// audio must not become audible after the barge-in.
	writePlaybackCommandFrame(t, sink, 13)
	assertQueuedSamples(t, sink, "write while blocked", 0)

	if err := sink.PlaybackCommand(t.Context(), audio.PlaybackResume); err != nil {
		t.Fatalf("resume: %v", err)
	}
	assertPlaybackState(t, sink, "after resume", 6, false)
	writePlaybackCommandFrame(t, sink, 14)
	assertQueuedSamples(t, sink, "write after resume", audio.FrameSize)

	interrupt, resume := <-observed, <-observed
	if interrupt.CommandID != 41 || !interrupt.Applied || !resume.Applied || resume.CommandID == 41 {
		t.Fatalf("observed receipts = %+v then %+v, want the interrupt then the resume, both applied", interrupt, resume)
	}
}

// TestRTCDeviceSinkRejectsStaleEpochDiscard keeps a late interrupt from
// flushing audio that belongs to a newer playback generation.
func TestRTCDeviceSinkRejectsStaleEpochDiscard(t *testing.T) {
	sink := newPlaybackCommandSink(t)
	if receipt := submitEpochDiscard(t, sink, 1, 3); !receipt.Applied {
		t.Fatalf("first interrupt receipt = %+v, want applied", receipt)
	}
	if err := sink.PlaybackCommand(t.Context(), audio.PlaybackResume); err != nil {
		t.Fatalf("resume: %v", err)
	}
	assertPlaybackState(t, sink, "after resume", 4, false)
	writePlaybackCommandFrame(t, sink, 21)

	for _, epoch := range []uint64{3, 4} {
		receipt := submitEpochDiscard(t, sink, 10+epoch, epoch)
		if receipt.Applied || !errors.Is(receipt.Err, audio.ErrStalePlaybackCommand) || receipt.Epoch != epoch {
			t.Fatalf("epoch %d receipt = %+v, want stale rejection", epoch, receipt)
		}
		assertQueuedSamples(t, sink, "after stale interrupt", audio.FrameSize)
		assertPlaybackState(t, sink, "after stale interrupt", 4, false)
	}
}

// TestRTCDeviceSinkDropsWritesFromSupersededGeneration covers the producer
// that read the playback state before an interrupt and enqueues after the
// resume: the generation it carries is stale, so its frame is dropped even
// though playback is open again.
func TestRTCDeviceSinkDropsWritesFromSupersededGeneration(t *testing.T) {
	sink := newPlaybackCommandSink(t)
	staleGeneration, blocked := sink.PlaybackState()
	if blocked {
		t.Fatal("new sink starts blocked")
	}
	if receipt := submitEpochDiscard(t, sink, 1, staleGeneration+1); !receipt.Applied {
		t.Fatalf("interrupt receipt = %+v, want applied", receipt)
	}
	if err := sink.PlaybackCommand(t.Context(), audio.PlaybackResume); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := sink.observedWritePlayback(t.Context(), playbackCommandFrame(31), staleGeneration, false, true); err != nil {
		t.Fatalf("stale-generation write: %v", err)
	}
	assertQueuedSamples(t, sink, "stale-generation write", 0)

	current, _ := sink.PlaybackState()
	if err := sink.observedWritePlayback(t.Context(), playbackCommandFrame(32), current, false, true); err != nil {
		t.Fatalf("current-generation write: %v", err)
	}
	assertQueuedSamples(t, sink, "current-generation write", audio.FrameSize)
}

// TestRTCDeviceSinkResumeOnlyReopensBlockedPlayback keeps a duplicate or
// early resume from advancing the generation, which would silently drop the
// frames producers are writing under the current one.
func TestRTCDeviceSinkResumeOnlyReopensBlockedPlayback(t *testing.T) {
	sink := newPlaybackCommandSink(t)
	before, _ := sink.PlaybackState()
	if err := sink.PlaybackCommand(t.Context(), audio.PlaybackResume); err != nil {
		t.Fatalf("resume open playback: %v", err)
	}
	assertPlaybackState(t, sink, "resume while open", before, false)

	if discarded := sink.DiscardPlayback(); discarded != 0 {
		t.Fatalf("discard of an empty queue = %d, want 0", discarded)
	}
	assertPlaybackState(t, sink, "explicit discard", before+1, true)
	for range 2 {
		if err := sink.PlaybackCommand(t.Context(), audio.PlaybackResume); err != nil {
			t.Fatalf("resume: %v", err)
		}
	}
	assertPlaybackState(t, sink, "repeated resume", before+2, false)
}

// TestRTCDeviceSinkPlaybackCommandOperations drives the remaining worker
// operations: an epochless discard is an explicit flush, a start reopens a
// blocked sink for the new response, interrupting an unknown response is not
// applied, and an unknown operation reports an error instead of guessing.
func TestRTCDeviceSinkPlaybackCommandOperations(t *testing.T) {
	sink := newPlaybackCommandSink(t)
	commands := sink.PlaybackCommands()
	writePlaybackCommandFrame(t, sink, 41)

	if err := sink.PlaybackCommand(t.Context(), audio.PlaybackDiscard); err != nil {
		t.Fatalf("epochless discard: %v", err)
	}
	assertQueuedSamples(t, sink, "epochless discard", 0)
	assertPlaybackState(t, sink, "epochless discard", 1, true)

	response := audio.PlaybackResponse{ResponseID: "command-response", ItemID: "command-item"}
	if receipt := commands.Exchange(t.Context(), audio.PlaybackStart, response); !receipt.Applied || receipt.Err != nil {
		t.Fatalf("start receipt = %+v, want applied", receipt)
	}
	assertPlaybackState(t, sink, "start after discard", 2, false)
	if receipt := commands.Exchange(t.Context(), audio.PlaybackStart, response); !receipt.Applied {
		t.Fatalf("repeated start receipt = %+v, want applied", receipt)
	}
	assertPlaybackState(t, sink, "repeated start", 2, false)

	unknown := audio.PlaybackResponse{ResponseID: "other-response", ItemID: "other-item"}
	if receipt := commands.Exchange(t.Context(), audio.PlaybackInterrupt, unknown); receipt.Applied || receipt.Interruption.PlaybackResponse != unknown {
		t.Fatalf("interrupt of unknown response receipt = %+v, want unapplied for that response", receipt)
	}
	if receipt := commands.Exchange(t.Context(), audio.PlaybackOperation(99), audio.PlaybackResponse{}); receipt.Applied || receipt.Err == nil {
		t.Fatalf("unknown operation receipt = %+v, want an error", receipt)
	}
	if sink.PlaybackController() == nil {
		t.Fatal("open sink has no playback controller")
	}
}

// TestRTCDeviceSinkCommandsAfterClose reports a closed command port instead of
// blocking a caller on a worker that has exited.
func TestRTCDeviceSinkCommandsAfterClose(t *testing.T) {
	sink := newPlaybackCommandSink(t)
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := sink.PlaybackCommand(t.Context(), audio.PlaybackResume); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("command after close = %v, want ErrClosed", err)
	}
	if _, err := sink.PlaybackCommands().TrySubmitWithReceipt(audio.Command{Epoch: 9, Kind: audio.CommandInterrupt}); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("interrupt after close = %v, want ErrClosed", err)
	}
	if !sink.PlaybackBuffer().Snapshot().Closed {
		t.Fatal("loop snapshot does not report the closed sink")
	}

	var nilSink *RTCDeviceSink
	if err := nilSink.PlaybackCommand(context.Background(), audio.PlaybackResume); !errors.Is(err, ErrRTCDeviceSinkClosed) {
		t.Fatalf("nil sink command = %v, want ErrRTCDeviceSinkClosed", err)
	}
	if nilSink.PlaybackCommands() != nil || nilSink.PlaybackController() != nil || !nilSink.PlaybackBuffer().Snapshot().Closed {
		t.Fatal("nil sink exposes playback ports")
	}
	if nilSink.discardPlaybackAtEpoch(1) {
		t.Fatal("nil sink applied an epoch discard")
	}
	nilSink.resumePlayback()
}

// TestRTCDeviceSinkInterruptPlaybackReportsHeardAudioOfRequestedResponse
// truncates a response at the audio the device actually rendered and keeps
// its late frames out of the next response's playback.
func TestRTCDeviceSinkInterruptPlaybackReportsHeardAudioOfRequestedResponse(t *testing.T) {
	registry, sink := newSinkBoundarySimulatedSink(t, 16000, audio.FrameSize)
	t.Cleanup(func() { closeSinkBoundarySink(t, sink) })
	interrupted := audio.PlaybackResponse{ResponseID: "interrupt-response", ItemID: "interrupt-item"}
	sink.StartPlayback(interrupted)
	writePlaybackCommandFrame(t, sink, 51)
	writePlaybackCommandFrame(t, sink, 52)
	sinkBoundaryAdvance(t, registry, 1, "render the first frame")

	if audioEnd, ok := sink.InterruptPlayback(audio.PlaybackResponse{ResponseID: "no-item"}); ok || audioEnd != 0 {
		t.Fatalf("interrupt without an item = (%d, %t), want rejected", audioEnd, ok)
	}
	assertQueuedSamples(t, sink, "interrupt without an item", audio.FrameSize)

	audioEnd, ok := sink.InterruptPlayback(interrupted)
	if !ok || audioEnd != 30 {
		t.Fatalf("interrupt = (%d ms, %t), want the 30 ms the device rendered", audioEnd, ok)
	}
	assertQueuedSamples(t, sink, "after interrupt", 0)
	if _, blocked := sink.PlaybackState(); !blocked {
		t.Fatal("interrupt left playback open")
	}

	next := audio.PlaybackResponse{ResponseID: "next-response", ItemID: "next-item"}
	sink.StartPlayback(next)
	if _, blocked := sink.playbackStateFor(next); blocked {
		t.Fatal("next response is blocked after its start")
	}
	if _, blocked := sink.playbackStateFor(interrupted); !blocked {
		t.Fatal("late frames of the interrupted response would play again")
	}
}

// TestRTCDeviceSinkCueAndDeviceFramesBypassModelTimeline admits local cues and
// device-tier frames to the queue without opening a model-audio span, so an
// interruption finds no model response to truncate.
func TestRTCDeviceSinkCueAndDeviceFramesBypassModelTimeline(t *testing.T) {
	sink := newPlaybackCommandSink(t)
	if err := sink.WritePlaybackCue(t.Context(), playbackCommandFrame(61)); err != nil {
		t.Fatalf("write cue: %v", err)
	}
	if err := sink.WriteDeviceFrame(t.Context(), playbackCommandFrame(62)); err != nil {
		t.Fatalf("write device frame: %v", err)
	}
	assertQueuedSamples(t, sink, "cue and device frame", 2*audio.FrameSize)
	if interruption, ok := sink.InterruptActivePlayback(); ok {
		t.Fatalf("interruption = %+v, want no model response", interruption)
	}
	assertQueuedSamples(t, sink, "after interruption", 0)

	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := sink.WriteDeviceFrame(t.Context(), playbackCommandFrame(63)); err == nil {
		t.Fatal("device frame written to a closed sink")
	}
	var nilSink *RTCDeviceSink
	if err := nilSink.WriteDeviceFrame(t.Context(), playbackCommandFrame(64)); !errors.Is(err, ErrRTCDeviceSinkClosed) {
		t.Fatalf("nil sink device frame = %v, want ErrRTCDeviceSinkClosed", err)
	}
}

// gatedRTCInboundMedia blocks its first read until released, so a test can
// observe the sink while a pump is running.
type gatedRTCInboundMedia struct {
	reading chan struct{}
	release chan struct{}
	err     error
}

func (m *gatedRTCInboundMedia) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	close(m.reading)
	select {
	case <-m.release:
		return audio.PCMFrame{}, m.err
	case <-ctx.Done():
		return audio.PCMFrame{}, ctx.Err()
	}
}

func (m *gatedRTCInboundMedia) Close() error { return nil }

// TestRTCDeviceSinkWaitForPumpTracksTheRunningPump lets a session drain the
// provider pump before closing: waiting honours its context while the pump
// runs, and a read failure is reported as a typed sink error.
func TestRTCDeviceSinkWaitForPumpTracksTheRunningPump(t *testing.T) {
	sink := newPlaybackCommandSink(t)
	sink.holdToneConfig.GapThreshold = time.Hour
	if err := sink.WaitForPump(t.Context()); err != nil {
		t.Fatalf("wait without a pump: %v", err)
	}
	readFailure := errors.New("provider read failed")
	media := &gatedRTCInboundMedia{reading: make(chan struct{}), release: make(chan struct{}), err: readFailure}
	pumped := make(chan error, 1)
	go func() { pumped <- sink.Run(t.Context(), media) }()
	<-media.reading

	if err := sink.Pump(t.Context(), &recordingRTCInboundMedia{}); !errors.Is(err, ErrRTCDeviceSinkRunning) {
		t.Fatalf("second pump = %v, want ErrRTCDeviceSinkRunning", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := sink.WaitForPump(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait with a cancelled context = %v, want context.Canceled", err)
	}
	var nilContext context.Context
	if err := sink.WaitForPump(nilContext); err == nil {
		t.Fatal("wait with a nil context while pumping succeeded")
	}

	close(media.release)
	err := <-pumped
	var sinkErr *RTCDeviceSinkError
	if !errors.As(err, &sinkErr) || sinkErr.Operation != "read" || !errors.Is(err, readFailure) || sinkErr.Error() == "" {
		t.Fatalf("pump error = %v, want a typed read failure wrapping the provider error", err)
	}
	if err := sink.WaitForPump(t.Context()); err != nil {
		t.Fatalf("wait after the pump finished: %v", err)
	}
}
