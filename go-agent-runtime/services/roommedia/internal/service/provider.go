package service

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roommedia"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func (s *Service) PumpProviderInput(ctx context.Context, request roommedia.ProviderInputRequest) error {
	ctx = normalizedContext(ctx)
	if request.Mixer == nil || request.Send == nil {
		return roommedia.ErrUnavailable
	}
	readContext, ackContext := providerContexts(ctx, request)
	policy := request.Policy
	if policy == nil {
		policy = func([]string) roommedia.InputPolicy { return roommedia.InputPolicyDefault }
	}
	format := request.Mixer.Format()
	for {
		mixed, err := readProviderFrame(readContext, request.Mixer)
		if err != nil {
			return err
		}
		if err := s.deliverProviderFrame(readContext, request, mixed, format, policy); err != nil {
			return err
		}
		if err := acknowledgeProviderFrame(ctx, ackContext, request.ReplayAcks); err != nil {
			return err
		}
	}
}

func providerContexts(ctx context.Context, request roommedia.ProviderInputRequest) (context.Context, context.Context) {
	readContext := request.ReadContext
	if readContext == nil {
		readContext = ctx
	}
	ackContext := request.AckContext
	if ackContext == nil {
		ackContext = ctx
	}
	return readContext, ackContext
}

func readProviderFrame(ctx context.Context, mixer roommedia.Mixer) (roommedia.MixedFrame, error) {
	frame, err := mixer.ReadFrameWithSources(ctx)
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return frame, err
	}
	return roommedia.MixedFrame{}, fmt.Errorf("read inbound mixer: %w", err)
}

func (s *Service) deliverProviderFrame(ctx context.Context, request roommedia.ProviderInputRequest, mixed roommedia.MixedFrame, format roommedia.PCM16Format, policy func([]string) roommedia.InputPolicy) error {
	sources := append([]string(nil), mixed.Sources...)
	providerFrame, err := s.convertProviderInput(mixed.PCM, format, request.ProviderSampleRate)
	if err != nil {
		return err
	}
	if err := request.Send(ctx, append([]byte(nil), providerFrame...), policy(append([]string(nil), sources...))); err != nil {
		rejectProviderFrame(request, mixed, err, sources)
		return fmt.Errorf("send mixed PCM: %w", err)
	}
	if request.Resolve != nil {
		request.Resolve(append([]string(nil), sources...), len(mixed.PCM), "")
	}
	if request.ObserveReceived != nil {
		request.ObserveReceived(append([]byte(nil), providerFrame...))
	}
	if request.Observe != nil {
		if err := request.Observe(request.ParticipantID, append([]byte(nil), providerFrame...)); err != nil {
			return fmt.Errorf("observe mixed PCM: %w", err)
		}
	}
	return nil
}

func rejectProviderFrame(request roommedia.ProviderInputRequest, mixed roommedia.MixedFrame, err error, sources []string) {
	if request.ObserveDropped != nil && audio.PCM16HasSignal(mixed.PCM) {
		request.ObserveDropped(err.Error(), len(mixed.PCM))
	}
	if request.ObserveRejected != nil {
		request.ObserveRejected(append([]byte(nil), mixed.PCM...), err)
	}
	if request.Resolve != nil {
		request.Resolve(append([]string(nil), sources...), len(mixed.PCM), roommedia.ProviderInputRejectedReason)
	}
}

func acknowledgeProviderFrame(ctx, ackContext context.Context, acks chan<- struct{}) error {
	if acks == nil {
		return nil
	}
	select {
	case acks <- struct{}{}:
		return nil
	case <-ackContext.Done():
		return ackContext.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
