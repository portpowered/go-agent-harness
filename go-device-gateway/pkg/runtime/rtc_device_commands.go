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
