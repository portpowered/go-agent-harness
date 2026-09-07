package livehost

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegateway "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestInterruptibleAudioSourceCloseStopsBlockedRead(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	t.Cleanup(func() {
		if err := read.Close(); err != nil {
			t.Errorf("close pipe reader: %v", err)
		}
	})
	t.Cleanup(func() {
		if err := write.Close(); err != nil {
			t.Errorf("close pipe writer: %v", err)
		}
	})

	duplicate, err := devicegateway.OpenInterruptibleInput(read)
	if err != nil {
		t.Fatalf("duplicate input: %v", err)
	}
	source, err := audio.NewFileSource("-", duplicate)
	if err != nil {
		if closeErr := duplicate.Close(); closeErr != nil {
			t.Fatalf("create file source: %v (close duplicate: %v)", err, closeErr)
		}
		t.Fatalf("create file source: %v", err)
	}
	interruptible := &interruptibleAudioSource{source: source, input: duplicate}

	readErr := make(chan error, 1)
	go func() {
		_, readErrValue := interruptible.ReadSamples(context.Background(), make([]int16, audio.FrameSize))
		readErr <- readErrValue
	}()
	select {
	case err := <-readErr:
		t.Fatalf("blocked input read returned before close: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	if err := interruptible.Close(); err != nil {
		t.Fatalf("close interruptible source: %v", err)
	}
	select {
	case err := <-readErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("input read error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("closing interruptible source did not stop blocked read")
	}

	if _, err := write.Write([]byte{0x01}); err != nil {
		t.Fatalf("write through caller-owned input after duplicate close: %v", err)
	}
	var sample [1]byte
	if count, err := read.Read(sample[:]); err != nil || count != 1 || sample[0] != 0x01 {
		t.Fatalf("caller-owned input after duplicate close = (%d, %v, %x), want one byte", count, err, sample[0])
	}
}
