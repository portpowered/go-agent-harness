package agentruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// streamSessionAudioInput is the loop adapter for the public audio service.
// It translates bounded PCM frames and turn boundaries into loop messages;
// framing, conversion, pacing and tail state stay in audioio.
func streamSessionAudioInput(ctx context.Context, loop *agentloop.AgentLoop, source *runtimeAudioSource) (runErr error) {
	if source == nil || source.source == nil {
		return fmt.Errorf("audio input source is unavailable")
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()

	scheduler := audioInputScheduler(source.clock, source.paced)
	input, err := audioiowire.NewService().OpenInput(ctx, audioio.InputRequest{
		Source: source.source, SourceRate: source.sourceRate, ProviderRate: source.providerRate,
		Pace: source.paced, Continuous: source.continuous || source.reader != nil, PadFinalFrame: source.paced && !source.continuous, Scheduler: scheduler,
		OnTurnBoundary: func(boundaryCtx context.Context) error {
			return sendSessionAudioBoundary(boundaryCtx, loop, source)
		},
	})
	if err != nil {
		return &RuntimeAudioInputError{Kind: RuntimeAudioInputFormat, Path: source.path, Err: err}
	}
	defer input.Close()
	err = input.Pump(ctx, &sessionAudioOutbound{loop: loop, source: source})
	if errors.Is(err, audioio.ErrEmptyInput) {
		return emptyRuntimeAudioInput(source.path)
	}
	return err
}

func audioInputScheduler(source platformclock.Source, paced bool) platformclock.Scheduler {
	if !paced {
		return nil
	}
	scheduler, _ := platformclock.Ensure(source).(platformclock.Scheduler)
	return scheduler
}

type sessionAudioOutbound struct {
	loop   *agentloop.AgentLoop
	source *runtimeAudioSource
}

func (o *sessionAudioOutbound) WriteFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
	if o == nil || o.source == nil {
		return errors.New("audio input loop is unavailable")
	}
	if len(frame.Samples) == 0 {
		return nil
	}
	pcm := make([]byte, len(frame.Samples)*2)
	if err := codec.EncodePCM16Into(pcm, frame.Samples); err != nil {
		return err
	}
	send := o.source.send
	if send == nil {
		if o.loop == nil {
			return errors.New("audio input loop is unavailable")
		}
		send = o.loop.SendAudioInput
	}
	if err := send(ctx, pcm); err != nil {
		return err
	}
	if o.source.runtime != nil {
		o.source.runtime.audioInput(pcm)
	}
	return nil
}

func (o *sessionAudioOutbound) Close() error { return nil }

func sendSessionAudioBoundary(ctx context.Context, loop *agentloop.AgentLoop, source *runtimeAudioSource) error {
	if source.endOfTurn != nil {
		if err := source.endOfTurn(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				err = fmt.Errorf("%w (%v)", ErrRuntimeAudioInputEndOfTurnLost, err)
			}
			return &RuntimeAudioInputError{Kind: RuntimeAudioInputSend, Path: source.path, Err: err}
		}
	} else if loop == nil {
		return &RuntimeAudioInputError{Kind: RuntimeAudioInputSend, Path: source.path, Err: errors.New("audio input loop is unavailable")}
	} else if err := loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return &RuntimeAudioInputError{Kind: RuntimeAudioInputSend, Path: source.path, Err: fmt.Errorf("%w (%v)", ErrRuntimeAudioInputEndOfTurnLost, err)}
		}
		return &RuntimeAudioInputError{Kind: RuntimeAudioInputSend, Path: source.path, Err: err}
	}
	return nil
}

var _ sharedaudio.OutboundMedia = (*sessionAudioOutbound)(nil)
