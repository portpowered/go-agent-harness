package runtime

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// runPlaybackCommands is a device worker separate from the PCM pump. An
// interrupt can therefore discard a full output queue and wake its producer.
func (s *RTCDeviceSink) runPlaybackCommands() {
	defer close(s.commandDone)
	for {
		request, err := s.commands.Receive(s.lifeCtx)
		if err != nil {
			return
		}
		receipt := audio.PlaybackReceipt{Applied: true}
		switch request.Operation {
		case audio.PlaybackStart:
			s.StartPlayback(request.Response)
		case audio.PlaybackDiscard:
			if request.Epoch == 0 {
				s.DiscardPlayback()
			} else {
				_, receipt.Applied = s.discardPlaybackAtEpoch(request.Epoch)
				if !receipt.Applied {
					receipt.Err = audio.ErrStalePlaybackCommand
				}
			}
		case audio.PlaybackResume:
			s.resumePlayback()
		case audio.PlaybackInterrupt:
			receipt.Interruption.PlaybackResponse = request.Response
			receipt.Interruption.AudioEndMS, receipt.Applied = s.InterruptPlayback(request.Response)
		case audio.PlaybackInterruptActive:
			receipt.Interruption, receipt.Applied = s.InterruptActivePlayback()
		default:
			receipt.Applied = false
			receipt.Err = fmt.Errorf("unknown playback operation %d", request.Operation)
		}
		request.Complete(receipt)
	}
}

// PlaybackCommand applies one ordered playback control operation and waits
// for its receipt. PCM pumping remains independent from this command worker.
func (s *RTCDeviceSink) PlaybackCommand(ctx context.Context, operation audio.PlaybackOperation) error {
	if s == nil || s.commands == nil {
		return ErrRTCDeviceSinkClosed
	}
	return s.commands.Exchange(ctx, operation, audio.PlaybackResponse{}).Err
}

// rtcDevicePlaybackSpan maps one provider response onto the monotonic count
// of complete samples consumed by the physical device, including underflow
// silence. Multiple spans may be queued at once so a tool continuation stays
// gapless without confusing the latest response with audible speech.
type rtcDevicePlaybackSpan struct {
	response audio.PlaybackResponse
	start    uint64
	end      uint64
	complete bool
}

// StartPlayback opens a provider response on the local device clock. The
// consumed-sample baseline is captured immediately before the first model
// frame is admitted, so idle underflow and hold-tone samples are excluded.
func (s *RTCDeviceSink) StartPlayback(response audio.PlaybackResponse) {
	if s == nil || s.sink == nil || response.ItemID == "" {
		return
	}
	s.playbackMu.Lock()
	if s.playbackResponse == response && !s.playbackBlocked {
		s.playbackMu.Unlock()
		return
	}
	if s.playbackBlocked {
		s.playbackBlocked = false
		s.playbackGeneration++
		s.snapshotEpoch.Store(s.playbackGeneration)
	}
	s.playbackResponse = response
	s.playbackMu.Unlock()
}

// PlaybackController exposes the sink's device-clocked interruption state to
// an owning live session. Callers that only need ordinary PCM pumping can
// ignore this optional capability.
func (s *RTCDeviceSink) PlaybackController() audio.PlaybackController {
	if s == nil {
		return nil
	}
	return s
}

// finishPlayback retires a fully drained provider response from the device
// clock. A later server-VAD event belongs to a new user turn and must not
// truncate the completed response, even if local hold-tone audio played during
// the intervening silence.
func (s *RTCDeviceSink) finishPlayback(response audio.PlaybackResponse) {
	if s == nil || response.ItemID == "" {
		return
	}
	s.playbackMu.Lock()
	defer s.playbackMu.Unlock()
	for index := len(s.playbackSpans) - 1; index >= 0; index-- {
		if s.playbackSpans[index].response == response {
			s.playbackSpans[index].complete = true
			break
		}
	}
	// A continuation may already be the latest prefetched response when the
	// preceding response reaches its provider boundary. Retire only the
	// response that is currently active; completing an older span must not
	// advance the shared generation and invalidate the continuation's queued
	// frames.
	if s.playbackResponse != response {
		return
	}
	s.playbackGeneration++
	s.snapshotEpoch.Store(s.playbackGeneration)
	s.playbackResponse = audio.PlaybackResponse{}
}

func consumedPlaybackSamples(stats audio.PlaybackQueueStats) uint64 {
	if stats.RenderedSamples < stats.UnderflowSamples {
		return 0
	}
	return stats.RenderedSamples - stats.UnderflowSamples
}

// resumePlayback opens a new local response boundary. Frames read under a
// prior generation remain stale even if they race with this transition.
func (s *RTCDeviceSink) resumePlayback() {
	if s == nil || s.sink == nil {
		return
	}
	s.playbackMu.Lock()
	// A normal tool-result continuation can request another response while the
	// preceding response is still draining to the physical device. Playback is
	// already open in that case: advancing the generation would make a frame
	// read just before response.create stale and discard its samples. Only a
	// prior accepted cancellation sets playbackBlocked and requires a new
	// generation boundary.
	if !s.playbackBlocked {
		s.playbackMu.Unlock()
		return
	}
	s.playbackBlocked = false
	s.playbackGeneration++
	s.snapshotEpoch.Store(s.playbackGeneration)
	s.playbackMu.Unlock()
}
