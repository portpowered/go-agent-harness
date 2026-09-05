package audio

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPlaybackCommandReceiptIsIndependentOfFullPCMBuffer(t *testing.T) {
	data, _, _, err := NewFrameBuffer(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.TrySubmit(PCMFrame{Samples: []int16{1}}); err != nil {
		t.Fatal(err)
	}
	commands, err := NewPlaybackCommands(1)
	if err != nil {
		t.Fatal(err)
	}
	defer commands.Close()
	done := make(chan PlaybackReceipt, 1)
	go func() { done <- commands.Exchange(context.Background(), PlaybackInterruptActive, PlaybackResponse{}) }()
	req, err := commands.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("admission was reported as application")
	default:
	}
	req.Complete(PlaybackReceipt{Applied: true, Interruption: PlaybackInterruption{AudioEndMS: 42}})
	receipt := <-done
	if !receipt.Applied || receipt.CommandID != req.ID || receipt.Interruption.AudioEndMS != 42 {
		t.Fatalf("receipt=%+v", receipt)
	}
}
func TestPlaybackCommandCloseReleasesOutstandingReceipt(t *testing.T) {
	commands, _ := NewPlaybackCommands(1)
	done := make(chan PlaybackReceipt, 1)
	go func() { done <- commands.Exchange(context.Background(), PlaybackResume, PlaybackResponse{}) }()
	req, err := commands.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	commands.Close()
	if receipt := <-done; !errors.Is(receipt.Err, ErrClosed) {
		t.Fatal(receipt)
	}
	// A late worker receipt never blocks after its requester stopped waiting.
	req.Complete(PlaybackReceipt{Applied: true})
	req.Complete(PlaybackReceipt{Applied: true})
}
func TestPlaybackCommandPreCancelledRequestIsNotAdmitted(t *testing.T) {
	commands, _ := NewPlaybackCommands(1)
	defer commands.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	receipt := commands.Exchange(ctx, PlaybackResume, PlaybackResponse{})
	if !errors.Is(receipt.Err, context.Canceled) || len(commands.requests) != 0 {
		t.Fatalf("receipt=%+v queued=%d", receipt, len(commands.requests))
	}
}

func TestPlaybackCommandsTrySubmitAdmitsInterruptWithoutWaiting(t *testing.T) {
	commands, err := NewPlaybackCommands(1)
	if err != nil {
		t.Fatal(err)
	}
	defer commands.Close()
	if err := commands.TrySubmit(Command{ID: 41, Epoch: 7, Kind: CommandInterrupt}); err != nil {
		t.Fatal(err)
	}
	if err := commands.TrySubmit(Command{ID: 42, Epoch: 8, Kind: CommandInterrupt}); !errors.Is(err, ErrControlFull) {
		t.Fatalf("second admission = %v, want ErrControlFull", err)
	}
	request, err := commands.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if request.Operation != PlaybackDiscard || request.ID != 41 || request.Epoch != 7 {
		t.Fatalf("request = %+v, want interrupt identity preserved", request)
	}
	request.Complete(PlaybackReceipt{Applied: true})
}

func TestPlaybackCommandsTrySubmitRejectsUnsupportedDrain(t *testing.T) {
	commands, err := NewPlaybackCommands(1)
	if err != nil {
		t.Fatal(err)
	}
	defer commands.Close()
	if err := commands.TrySubmit(Command{Kind: CommandDrain}); !errors.Is(err, ErrUnsupportedPlaybackCommand) {
		t.Fatalf("drain admission = %v, want ErrUnsupportedPlaybackCommand", err)
	}
}

func TestPlaybackCommandsTrySubmitWithReceiptPreservesIdentity(t *testing.T) {
	commands, err := NewPlaybackCommands(1)
	if err != nil {
		t.Fatal(err)
	}
	defer commands.Close()
	receipts, err := commands.TrySubmitWithReceipt(Command{ID: 73, Epoch: 9, Kind: CommandInterrupt})
	if err != nil {
		t.Fatal(err)
	}
	request, err := commands.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request.Complete(PlaybackReceipt{Applied: true})
	select {
	case receipt := <-receipts:
		if receipt.CommandID != 73 || !receipt.Applied {
			t.Fatalf("receipt = %+v", receipt)
		}
	default:
		t.Fatal("worker receipt was not observable")
	}
}

func TestPlaybackCommandsTrySubmitDeliversAppliedReceiptToObserver(t *testing.T) {
	commands, err := NewPlaybackCommands(1)
	if err != nil {
		t.Fatal(err)
	}
	defer commands.Close()
	receipts := make(chan PlaybackReceipt, 1)
	commands.SetReceiptObserver(func(receipt PlaybackReceipt) { receipts <- receipt })
	if err := commands.TrySubmit(Command{ID: 91, Epoch: 4, Kind: CommandInterrupt}); err != nil {
		t.Fatal(err)
	}
	request, err := commands.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request.Complete(PlaybackReceipt{Applied: true})
	select {
	case receipt := <-receipts:
		if receipt.CommandID != 91 || !receipt.Applied {
			t.Fatalf("receipt = %+v", receipt)
		}
	case <-time.After(time.Second):
		t.Fatal("observer did not receive worker receipt")
	}
}
