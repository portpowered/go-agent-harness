package service

import (
	"context"
	"errors"
	"fmt"
	"io"
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

func (i *input) finishContinuousEOF(ctx context.Context) error {
	if !i.turnHasSamples && i.lastBoundary {
		return nil
	}
	if err := i.notifyBoundary(ctx); err != nil {
		return err
	}
	i.turnHasSamples = false
	i.lastBoundary = true
	return nil
}

func validateReadError(err error) error {
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read audio input: %w", err)
	}
	return nil
}

func noProgressError(err error) error {
	if errors.Is(err, io.EOF) {
		return nil
	}
	return io.ErrNoProgress
}

func clearProcessedFrames(frames []audio.PCMFrame) {
	for index := range frames {
		clear(frames[index].Samples)
	}
}
