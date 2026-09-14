package service

import (
	"context"
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func waitForSamples(ctx context.Context, scheduler clock.Scheduler, start time.Time, samples, sourceRate int) error {
	if scheduler == nil {
		return errors.New("audio input pacing requires a scheduler")
	}
	wait := start.Add(time.Duration(samples) * time.Second / time.Duration(sourceRate)).Sub(scheduler.Now())
	if wait <= 0 {
		return nil
	}
	timer := scheduler.NewTimer(wait)
	if timer == nil {
		return errors.New("audio input pacing scheduler returned a nil timer")
	}
	defer timer.Stop()
	select {
	case <-timer.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func writeFrame(ctx context.Context, outbound audio.OutboundMedia, frame audio.PCMFrame) error {
	if len(frame.Samples) == 0 {
		return nil
	}
	return outbound.WriteFrame(ctx, frame)
}
