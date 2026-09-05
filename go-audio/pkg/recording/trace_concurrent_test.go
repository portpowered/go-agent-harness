package recording

import (
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"io"
	"sync"
	"testing"
)

func TestConcurrentTapAdmissionHasReplayableSampleOrder(t *testing.T) {
	directory := t.TempDir()
	trace, err := NewTrace(directory, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	var producers sync.WaitGroup
	for i := 0; i < 64; i++ {
		producers.Add(1)
		go func(value int16) {
			defer producers.Done()
			for n := 0; n < 8; n++ {
				trace.CaptureMicrophonePreGate(16000, []int16{value})
			}
		}(int16(i))
	}
	producers.Wait()
	if err := trace.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenReplay(directory)
	if err != nil {
		t.Fatal(err)
	}
	samples := 0
	for {
		_, frame, err := replay.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if frame != nil {
			samples += len(frame.Samples)
		}
	}
	if samples != 512 {
		t.Fatalf("replayed samples=%d want=512", samples)
	}
}
