package audio_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestFrameBufferTailOwnershipAndAccounting(t *testing.T) {
	p, c, ctl, err := audio.NewFrameBuffer(3, 7)
	if err != nil {
		t.Fatal(err)
	}
	samples := []int16{1, 2, 3, 4, 5}
	if err := p.TrySubmit(audio.PCMFrame{Samples: samples, StreamID: "response", StartSample: 12}); err != nil {
		t.Fatal(err)
	}
	samples[0] = 99
	if err := p.TrySubmit(audio.PCMFrame{Samples: []int16{6, 7}, EndOfResponse: true}); err != nil {
		t.Fatal(err)
	}
	if err := p.TrySubmit(audio.PCMFrame{Samples: []int16{8}}); !errors.Is(err, audio.ErrBufferFull) {
		t.Fatalf("overflow = %v", err)
	}
	p.Close()
	first, err := c.Receive(context.Background())
	if err != nil || !reflect.DeepEqual(first.Samples, []int16{1, 2, 3, 4, 5}) || first.StartSample != 12 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	tail, err := c.Receive(context.Background())
	if err != nil || !tail.EndOfResponse || !reflect.DeepEqual(tail.Samples, []int16{6, 7}) {
		t.Fatalf("tail=%+v err=%v", tail, err)
	}
	if _, err = c.Receive(context.Background()); err != io.EOF {
		t.Fatalf("after drain=%v", err)
	}
	s := ctl.Snapshot()
	if s.AdmittedSamples != 7 || s.ConsumedSamples != 7 || s.QueuedSamples != 0 || s.DiscardedSamples != 0 {
		t.Fatalf("accounting=%+v", s)
	}
}

func TestFrameBufferInterruptRejectsBlockedOldEpoch(t *testing.T) {
	p, c, ctl, err := audio.NewFrameBuffer(1, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.TrySubmit(audio.PCMFrame{Samples: []int16{1, 2, 3, 4}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.Submit(context.Background(), audio.PCMFrame{Samples: []int16{5}}) }()
	if discarded := ctl.Invalidate(1); discarded != 4 {
		t.Fatalf("discarded=%d", discarded)
	}
	if err := <-done; !errors.Is(err, audio.ErrStaleEpoch) {
		t.Fatalf("old epoch=%v", err)
	}
	if discarded := ctl.Invalidate(1); discarded != 0 {
		t.Fatalf("duplicate interrupt=%d", discarded)
	}
	if err := p.TrySubmit(audio.PCMFrame{Epoch: 1, Samples: []int16{9}}); err != nil {
		t.Fatal(err)
	}
	f, ok, err := c.TryReceive()
	if err != nil || !ok || !reflect.DeepEqual(f.Samples, []int16{9}) {
		t.Fatalf("new epoch=%+v,%v,%v", f, ok, err)
	}
	s := ctl.Snapshot()
	if s.AdmittedSamples != s.ConsumedSamples+s.DiscardedSamples+uint64(s.QueuedSamples) {
		t.Fatalf("lost accounting=%+v", s)
	}
}

func TestFrameBufferCancellationAndEmptyBoundaryCapacity(t *testing.T) {
	p, c, _, err := audio.NewFrameBuffer(1, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.TryReceive(); ok || err != nil {
		t.Fatalf("empty=%v,%v", ok, err)
	}
	if err := p.TrySubmit(audio.PCMFrame{EndOfResponse: true}); err != nil {
		t.Fatal(err)
	}
	if err := p.TrySubmit(audio.PCMFrame{EndOfResponse: true}); err != audio.ErrBufferFull {
		t.Fatalf("unbounded markers: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Submit(ctx, audio.PCMFrame{}); err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
	if _, err := c.Receive(ctx); err != context.Canceled {
		t.Fatalf("receive cancellation=%v", err)
	}
	if err := p.TrySubmit(audio.PCMFrame{Samples: make([]int16, 5)}); err != audio.ErrFrameTooLarge {
		t.Fatalf("oversize=%v", err)
	}
	p.Close()
	if err := p.TrySubmit(audio.PCMFrame{}); err != audio.ErrBufferClosed {
		t.Fatalf("closed=%v", err)
	}
}
