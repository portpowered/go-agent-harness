package rtc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const inboundTrackRaceTimeout = 2 * time.Second

// TestInboundTrackS8ConcurrentIngestReadCancelClose keeps the legacy race-gate
// name while exercising the remaining Pion-facing inbound adapter. RTP policy
// and transport lifecycle coverage lives in the runtime transport package.
func TestInboundTrackS8ConcurrentIngestReadCancelClose(t *testing.T) {
	t.Parallel()

	inbound := newPionInbound(nil, "race")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	produced := make(chan struct{})
	var producedOnce sync.Once
	failures := make(chan error, 3)
	var workers sync.WaitGroup
	workers.Add(3)

	go func() {
		defer workers.Done()
		defer producedOnce.Do(func() { close(produced) })
		<-started
		for index := 0; index < 24; index++ {
			frame := sharedaudio.PCMFrame{Samples: []int16{int16(index)}}
			select {
			case inbound.frames <- frame:
				if index == 8 {
					producedOnce.Do(func() { close(produced) })
				}
			case <-inbound.done:
				return
			}
		}
	}()

	go func() {
		defer workers.Done()
		<-started
		for {
			frame, err := inbound.ReadFrame(ctx)
			if err != nil {
				if !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
					failures <- fmt.Errorf("read frame after lifecycle stop: %w", err)
				}
				return
			}
			if len(frame.Samples) != 1 {
				failures <- fmt.Errorf("read frame has %d samples, want 1", len(frame.Samples))
				return
			}
		}
	}()

	go func() {
		defer workers.Done()
		<-started
		timer := time.NewTimer(inboundTrackRaceTimeout)
		defer timer.Stop()
		select {
		case <-produced:
		case <-timer.C:
			failures <- errors.New("inbound producer did not reach the close point")
		}
		cancel()
		if err := inbound.Close(); err != nil {
			failures <- fmt.Errorf("close inbound adapter: %w", err)
		}
	}()

	close(started)
	workersDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(workersDone)
	}()

	select {
	case <-workersDone:
	case <-time.After(inboundTrackRaceTimeout):
		t.Fatal("inbound adapter workers did not stop")
	}

	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}

	for {
		select {
		case <-inbound.frames:
		default:
			frame, err := inbound.ReadFrame(context.Background())
			if !errors.Is(err, io.EOF) {
				t.Fatalf("read after draining closed adapter: frame=%#v err=%v", frame, err)
			}
			return
		}
	}
}
