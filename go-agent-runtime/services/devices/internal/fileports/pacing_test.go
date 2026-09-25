package fileports

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestOpenFileMediaAppliesPacingToEveryFileInput(t *testing.T) {
	t.Parallel()
	input := writePCM(t, "in.pcm", audio.FrameSize)
	turn := writePCM(t, "turn.pcm", audio.FrameSize)
	interrupt := writePCM(t, "interrupt.pcm", audio.FrameSize)
	scheduler := clock.NewDeterministic(time.Unix(0, 0), time.Millisecond)
	for _, test := range []struct {
		name        string
		pacing      devices.FilePacing
		wantPace    bool
		accelerated bool
	}{
		{name: "zero value is real time", wantPace: true},
		{name: "speed one is real time", pacing: devices.FilePacing{Speed: 1}, wantPace: true},
		{name: "speed accelerates the scheduler", pacing: devices.FilePacing{Speed: 8}, wantPace: true, accelerated: true},
		{name: "unpaced ignores speed", pacing: devices.FilePacing{Unpaced: true, Speed: 8}},
	} {
		t.Run(test.name, func(t *testing.T) {
			handle, err := New().OpenFileMedia(devices.FileMediaRequest{
				Input: &devices.FileMediaSource{Path: input}, InputTurns: []string{turn}, Interruptions: []string{interrupt},
				Scheduler: scheduler, Pacing: test.pacing,
			})
			if err != nil {
				t.Fatalf("OpenFileMedia: %v", err)
			}
			t.Cleanup(func() {
				if err := handle.Close(); err != nil {
					t.Errorf("close: %v", err)
				}
			})
			media := handle.Media()
			for name, file := range map[string]devices.FileInput{"input": *media.Input, "turn": media.InputTurns[0], "interruption": media.Interruptions[0]} {
				if file.Pace != test.wantPace {
					t.Fatalf("%s pace = %v, want %v", name, file.Pace, test.wantPace)
				}
				_, accelerated := file.Scheduler.(*acceleratedScheduler)
				if accelerated != test.accelerated || (!accelerated && file.Scheduler != scheduler) {
					t.Fatalf("%s scheduler = %T, want accelerated=%v over the request scheduler", name, file.Scheduler, test.accelerated)
				}
			}
		})
	}
}

func TestOpenFileMediaRejectsInvalidPacingBeforeOpeningFiles(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing.pcm")
	for _, speed := range []float64{-1, math.NaN(), math.Inf(1)} {
		handle, err := New().OpenFileMedia(devices.FileMediaRequest{
			Input: &devices.FileMediaSource{Path: missing}, Pacing: devices.FilePacing{Speed: speed},
		})
		if handle != nil || !errors.Is(err, devices.ErrInvalidRequest) {
			t.Fatalf("speed %v: OpenFileMedia = (%v, %v), want ErrInvalidRequest before any open", speed, handle, err)
		}
	}
}

func TestAcceleratedSchedulerRunsBaseTimeFaster(t *testing.T) {
	t.Parallel()
	base := clock.NewDeterministic(time.Unix(100, 0), time.Millisecond)
	origin := base.Now()
	scheduler := pacingScheduler(base, devices.FilePacing{Speed: 10})
	timer := scheduler.NewTimer(time.Second)
	defer timer.Stop()
	deadline, cancelDeadline := scheduler.WithDeadline(context.Background(), origin.Add(time.Second))
	defer cancelDeadline()
	timeout, cancelTimeout := scheduler.WithTimeout(context.Background(), time.Second)
	defer cancelTimeout()
	waited := make(chan error, 1)
	go func() { waited <- scheduler.Wait(context.Background(), time.Second) }()
	if err := base.WaitForTimers(context.Background(), 4); err != nil {
		t.Fatalf("wait for armed timers: %v", err)
	}

	base.AdvanceBy(99 * time.Millisecond)
	if got := scheduler.Now().Sub(origin); got != 990*time.Millisecond {
		t.Fatalf("accelerated elapsed = %v, want 990ms after 99ms of base time", got)
	}
	select {
	case <-timer.C():
		t.Fatal("accelerated timer fired before one accelerated second")
	case <-deadline.Done():
		t.Fatal("accelerated deadline expired early")
	case <-timeout.Done():
		t.Fatal("accelerated timeout expired early")
	default:
	}

	base.AdvanceBy(time.Millisecond)
	<-timer.C()
	<-deadline.Done()
	<-timeout.Done()
	if err := <-waited; err != nil {
		t.Fatalf("accelerated wait: %v", err)
	}
}

func TestAcceleratedSchedulerNeverRoundsAPositiveWaitToZero(t *testing.T) {
	t.Parallel()
	scheduler := &acceleratedScheduler{speed: 1e12}
	if got := scheduler.baseDuration(time.Nanosecond); got != 1 {
		t.Fatalf("baseDuration(1ns) = %v, want 1ns", got)
	}
	if got := scheduler.baseDuration(-time.Second); got != -time.Second {
		t.Fatalf("baseDuration(-1s) = %v, want unchanged", got)
	}
}

func TestPacingSchedulerKeepsHostSchedulerWhenNotAccelerated(t *testing.T) {
	t.Parallel()
	base := clock.NewDeterministic(time.Unix(0, 0), time.Millisecond)
	for _, pacing := range []devices.FilePacing{{}, {Speed: 1}, {Unpaced: true, Speed: 4}} {
		if got := pacingScheduler(base, pacing); got != base {
			t.Fatalf("pacingScheduler(%+v) = %T, want the host scheduler", pacing, got)
		}
	}
	if got := pacingScheduler(nil, devices.FilePacing{Speed: 4}); got != nil {
		t.Fatalf("pacingScheduler(nil) = %T, want nil so paced admission reports the missing scheduler", got)
	}
}
