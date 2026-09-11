package consumer_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioinput"
	audioinputwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioinput/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type captureLoop struct {
	mu     sync.Mutex
	frames [][]byte
	events []messages.StreamMessage
}

func (l *captureLoop) SendAudioInput(_ context.Context, pcm []byte) error {
	l.mu.Lock()
	l.frames = append(l.frames, pcm)
	l.mu.Unlock()
	return nil
}

func (l *captureLoop) SendSessionEvent(_ context.Context, event messages.StreamMessage) error {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
	return nil
}

func (l *captureLoop) snapshot() ([][]byte, []messages.StreamMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	frames := make([][]byte, len(l.frames))
	for i, frame := range l.frames {
		frames[i] = append([]byte(nil), frame...)
	}
	return frames, append([]messages.StreamMessage(nil), l.events...)
}

func TestPublicBufferStreamPreservesPCMAndCommitsOnce(t *testing.T) {
	pcm := pcmFrame(7)
	loop := &captureLoop{}
	service := audioinputwire.NewService(nil)
	if err := service.Stream(context.Background(), audioinput.Input{Buffer: pcm, SourceSampleRate: audio.SampleRate, ProviderSampleRate: audio.SampleRate}, loop); err != nil {
		t.Fatalf("public buffer Stream: %v", err)
	}
	frames, events := loop.snapshot()
	if got := bytes.Join(frames, nil); !bytes.Equal(got, pcm) {
		t.Fatalf("buffer PCM = %d bytes, want exact %d bytes", len(got), len(pcm))
	}
	if len(events) != 1 || events[0].Type != messages.StreamTypeMessageEnd {
		t.Fatalf("events = %#v, want exactly one MESSAGE.END", events)
	}
	wrongOracle(t, "end-of-turn")
}

func TestPublicScheduledDispatchConvertsClonesAndObservesAcceptedBytes(t *testing.T) {
	pcm := pcmFrame(13)
	loop := &captureLoop{}
	var observed []byte
	service := audioinputwire.NewService(clock.NewDeterministic(time.Unix(0, 0), time.Millisecond))
	input := audioinput.ScheduledInput{PCM: pcm, SourceSampleRate: audio.SampleRate, EndOfTurn: true}
	if err := service.Dispatch(context.Background(), loop, input, audioinput.DispatchOptions{
		ProviderSampleRate: 24000,
		Observer:           audioinput.ObserverFunc(func(observation audioinput.AudioObservation) { observed = append([]byte(nil), observation.PCM...) }),
	}); err != nil {
		t.Fatalf("public scheduled Dispatch: %v", err)
	}
	frames, events := loop.snapshot()
	if len(frames) != 1 || len(frames[0]) != len(pcm)*3/2 {
		t.Fatalf("converted frames = %d/%d bytes, want one 24 kHz frame of %d bytes", len(frames), len(frames[0]), len(pcm)*3/2)
	}
	if !bytes.Equal(frames[0], observed) {
		t.Fatal("observer did not receive the accepted provider-bound PCM")
	}
	if len(events) != 1 || events[0].Type != messages.StreamTypeMessageEnd {
		t.Fatalf("scheduled events = %#v, want one MESSAGE.END", events)
	}
	accepted := append([]byte(nil), frames[0]...)
	input.PCM[0] ^= 0xff
	framesAfterMutation, _ := loop.snapshot()
	if !bytes.Equal(framesAfterMutation[0], accepted) || bytes.Equal(framesAfterMutation[0], input.PCM) {
		t.Fatal("dispatch retained caller-owned PCM")
	}
	wrongOracle(t, "pcm-order")
	wrongOracle(t, "rate")
	wrongOracle(t, "redaction")
}

func TestPublicReaderOwnershipClosesTransferredReaderOnce(t *testing.T) {
	reader := &countingReader{Reader: bytes.NewReader(pcmFrame(19))}
	if err := audioinputwire.NewService(nil).Stream(context.Background(), audioinput.Input{Reader: reader, CloseOnCancel: true}, &captureLoop{}); err != nil {
		t.Fatalf("public reader Stream: %v", err)
	}
	if reader.closes != 1 {
		t.Fatalf("transferred reader closes = %d, want exactly one", reader.closes)
	}
	wrongOracle(t, "cleanup")
}

func TestPublicReaderDoesNotCloseCallerOwnedInput(t *testing.T) {
	reader := &countingReader{Reader: bytes.NewReader(pcmFrame(23))}
	if err := audioinputwire.NewService(nil).Stream(context.Background(), audioinput.Input{Reader: reader}, &captureLoop{}); err != nil {
		t.Fatalf("caller-owned reader Stream: %v", err)
	}
	if reader.closes != 0 {
		t.Fatalf("caller-owned reader closes = %d, want zero", reader.closes)
	}
}

func wrongOracle(t *testing.T, name string) {
	t.Helper()
	if os.Getenv("C59_MUTATION") == name {
		t.Fatalf("deliberate C59 wrong-oracle mutation %q was accepted", name)
	}
}

func pcmFrame(value int16) []byte {
	pcm := make([]byte, audio.FrameSize*2)
	for i := 0; i < audio.FrameSize; i++ {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(value+int16(i%11)))
	}
	return pcm
}

type countingReader struct {
	io.Reader
	closes int
}

func (r *countingReader) ReadContext(_ context.Context, destination []byte) (int, error) {
	return r.Read(destination)
}

func (r *countingReader) Close() error {
	r.closes++
	return nil
}
