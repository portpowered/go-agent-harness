package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioinput"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func TestStreamSendsFinitePCMAndOneEndOfTurn(t *testing.T) {
	loop := &captureLoop{}
	pcm := pcm16Frame(7)
	var observations []audioinput.AudioObservation
	service := New(nil)
	err := service.Stream(context.Background(), audioinput.Input{
		Buffer:             pcm,
		SourceSampleRate:   audio.SampleRate,
		ProviderSampleRate: audio.SampleRate,
		Observer: audioinput.ObserverFunc(func(observation audioinput.AudioObservation) {
			observations = append(observations, observation)
		}),
	}, loop)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := bytes.Join(loop.frames, nil); !bytes.Equal(got, pcm) {
		t.Fatalf("sent PCM = %d bytes, want exact %d bytes", len(got), len(pcm))
	}
	if len(loop.events) != 1 || loop.events[0].Type != messages.StreamTypeMessageEnd {
		t.Fatalf("events = %#v, want one MESSAGE.END", loop.events)
	}
	if len(observations) == 0 || !bytes.Equal(observations[0].PCM, loop.frames[0]) {
		t.Fatalf("observations = %#v, want accepted frame bytes", observations)
	}
	loop.frames[0][0] ^= 0xff
	if bytes.Equal(observations[0].PCM, loop.frames[0]) {
		t.Fatal("observation aliases sent frame")
	}
}

func TestStreamRejectsEmptyInputWithoutSend(t *testing.T) {
	loop := &captureLoop{}
	err := New(nil).Stream(context.Background(), audioinput.Input{Buffer: []byte{}}, loop)
	if !errors.Is(err, audioinput.ErrEmpty) || len(loop.frames) != 0 || len(loop.events) != 0 {
		t.Fatalf("Stream error=%v frames=%d events=%d, want empty before side effects", err, len(loop.frames), len(loop.events))
	}
}

func TestStreamJoinsEndOfTurnCancellationIdentity(t *testing.T) {
	loop := &captureLoop{eventErr: context.Canceled}
	err := New(nil).Stream(context.Background(), audioinput.Input{Buffer: pcm16Frame(3)}, loop)
	if !errors.Is(err, audioinput.ErrEndOfTurnLost) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream error = %v, want end-of-turn and cancellation identities", err)
	}
}

func TestStreamJoinsOwnedReaderCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	reader := &closingReader{Reader: bytes.NewReader(pcm16Frame(4)), err: closeErr}
	err := New(nil).Stream(context.Background(), audioinput.Input{Reader: reader, CloseOnCancel: true}, &captureLoop{})
	if !errors.Is(err, audioinput.ErrClose) || !errors.Is(err, closeErr) {
		t.Fatalf("Stream error = %v, want close identities", err)
	}
	if reader.closes != 1 {
		t.Fatalf("reader closes = %d, want 1", reader.closes)
	}
}

func TestStreamPacesWithInjectedClock(t *testing.T) {
	virtual := &signalingClock{Deterministic: clock.NewDeterministic(time.Unix(0, 0), time.Millisecond), timerCreated: make(chan struct{}, 1)}
	source := &twoFrameSource{}
	loop := &captureLoop{}
	done := make(chan error, 1)
	go func() {
		done <- New(virtual).Stream(context.Background(), audioinput.Input{
			Source: source, SourceSampleRate: audio.SampleRate, ProviderSampleRate: audio.SampleRate,
			Pace: true, Clock: virtual,
		}, loop)
	}()
	waitFor(t, func() bool { return loop.frameCount() == 1 })
	select {
	case <-virtual.timerCreated:
	case <-time.After(time.Second):
		t.Fatal("pacing timer was not created")
	}
	virtual.AdvanceBy(29 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Stream finished before pacing deadline: %v", err)
	default:
	}
	virtual.AdvanceBy(2 * time.Millisecond)
	waitFor(t, func() bool { return loop.frameCount() == 2 })
	select {
	case <-virtual.timerCreated:
	case <-time.After(time.Second):
		t.Fatal("EOF pacing timer was not created")
	}
	virtual.AdvanceBy(29 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Stream finished before EOF pacing deadline: %v", err)
	default:
	}
	virtual.AdvanceBy(time.Millisecond)
	if err := <-done; err != nil {
		t.Fatalf("paced Stream: %v", err)
	}
	if got := loop.frameCount(); got != 2 {
		t.Fatalf("frames = %d, want 2", got)
	}
}

func TestDispatchClonesConvertsAndCommitsOnce(t *testing.T) {
	loop := &captureLoop{}
	var observation audioinput.AudioObservation
	input := audioinput.ScheduledInput{PCM: pcm16Frame(9), SourceSampleRate: audio.SampleRate, EndOfTurn: true}
	want := append([]byte(nil), input.PCM...)
	err := New(nil).Dispatch(context.Background(), loop, input, audioinput.DispatchOptions{
		ProviderSampleRate: audio.SampleRate,
		Observer:           audioinput.ObserverFunc(func(got audioinput.AudioObservation) { observation = got }),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	input.PCM[0] ^= 0xff
	if !bytes.Equal(loop.frames[0], want) || !bytes.Equal(observation.PCM, want) {
		t.Fatal("Dispatch retained or observed caller-owned PCM")
	}
	if len(loop.events) != 1 {
		t.Fatalf("events = %d, want one", len(loop.events))
	}
}

func TestConvertScheduledClonesEachTurn(t *testing.T) {
	inputs := []audioinput.ScheduledInput{{PCM: pcm16Frame(1), SourceSampleRate: audio.SampleRate}}
	converted, err := New(nil).ConvertScheduled(inputs, audio.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	if &converted[0].PCM[0] == &inputs[0].PCM[0] {
		t.Fatal("ConvertScheduled aliased PCM")
	}
	inputs[0].PCM[0] ^= 0xff
	if bytes.Equal(converted[0].PCM, inputs[0].PCM) {
		t.Fatal("converted PCM changed with caller mutation")
	}
}

func TestOwnershipAdaptersJoinCleanupAndObservation(t *testing.T) {
	runErr := errors.New("run failed")
	closeErr := errors.New("close failed")
	err := WithSource(
		func() (*twoFrameSource, error) { return &twoFrameSource{}, nil },
		func(*twoFrameSource) error { return runErr },
		func(*twoFrameSource) error { return closeErr },
	)
	if !errors.Is(err, runErr) || !errors.Is(err, closeErr) {
		t.Fatalf("WithSource error = %v, want run and cleanup causes", err)
	}

	service := New(nil)
	cleanupCalls := 0
	err = StreamWithCleanup(context.Background(), service, audioinput.Input{Buffer: pcm16Frame(1)}, &captureLoop{}, func() error {
		cleanupCalls++
		return closeErr
	})
	if !errors.Is(err, closeErr) || cleanupCalls != 1 {
		t.Fatalf("StreamWithCleanup error=%v cleanupCalls=%d", err, cleanupCalls)
	}

	cleanupCalls = 0
	pcm, rate, err := ReadWithCleanup(context.Background(), service, audioinput.Input{Buffer: pcm16Bytes(1, 2)}, func() error {
		cleanupCalls++
		return closeErr
	})
	if !errors.Is(err, closeErr) || len(pcm) != 4 || rate != audio.SampleRate || cleanupCalls != 1 {
		t.Fatalf("ReadWithCleanup pcm=%d rate=%d error=%v cleanupCalls=%d", len(pcm), rate, err, cleanupCalls)
	}

	loop := &captureLoop{}
	if err := StreamManaged(context.Background(), service, &audioinput.ManagedSource{
		Source: audio.NewSliceSource([]int16{3, 4}), SourceSampleRate: audio.SampleRate,
	}, loop); err != nil {
		t.Fatalf("StreamManaged: %v", err)
	}
	managed := &audioinput.ManagedSource{Source: audio.NewSliceSource([]int16{5, 6}), SourceSampleRate: audio.SampleRate}
	pcm, rate, err = ReadManaged(context.Background(), service, managed)
	if err != nil || len(pcm) != 4 || rate != audio.SampleRate {
		t.Fatalf("ReadManaged pcm=%d rate=%d error=%v", len(pcm), rate, err)
	}

	observed := false
	if err := DispatchWithObservation(context.Background(), service, &captureLoop{}, audioinput.ScheduledInput{PCM: pcm16Bytes(7, 8)}, audioinput.DispatchOptions{}, func(observation audioinput.AudioObservation) {
		observed = bytes.Equal(observation.PCM, pcm16Bytes(7, 8))
	}); err != nil {
		t.Fatalf("DispatchWithObservation: %v", err)
	}
	if !observed {
		t.Fatal("DispatchWithObservation did not deliver the accepted PCM")
	}
}

func TestReadAndDispatchFailurePorts(t *testing.T) {
	service := New(nil)
	readErr := errors.New("source failed")
	_, _, err := service.Read(context.Background(), audioinput.Input{Source: audioSourceFunc(func(context.Context, []int16) error { return readErr })})
	if !errors.Is(err, readErr) || !errors.Is(err, audioinput.ErrRead) {
		t.Fatalf("Read error = %v, want source and read identities", err)
	}

	if err := service.Dispatch(context.Background(), &captureLoop{}, audioinput.ScheduledInput{}, audioinput.DispatchOptions{}); !errors.Is(err, audioinput.ErrEmpty) {
		t.Fatalf("empty Dispatch error = %v, want ErrEmpty", err)
	}
	if err := service.Dispatch(context.Background(), nil, audioinput.ScheduledInput{PCM: pcm16Bytes(1)}, audioinput.DispatchOptions{}); !errors.Is(err, audioinput.ErrUnavailable) {
		t.Fatalf("unavailable Dispatch error = %v, want ErrUnavailable", err)
	}

	sendErr := errors.New("send failed")
	if err := service.Dispatch(context.Background(), &captureLoop{frameErr: sendErr}, audioinput.ScheduledInput{PCM: pcm16Bytes(1)}, audioinput.DispatchOptions{}); !errors.Is(err, sendErr) || !errors.Is(err, audioinput.ErrSend) {
		t.Fatalf("frame send error = %v, want send identities", err)
	}
	if err := service.Dispatch(context.Background(), &captureLoop{eventErr: sendErr}, audioinput.ScheduledInput{PCM: pcm16Bytes(1), EndOfTurn: true}, audioinput.DispatchOptions{}); !errors.Is(err, sendErr) || !errors.Is(err, audioinput.ErrSend) {
		t.Fatalf("event send error = %v, want send identities", err)
	}
	if err := service.Dispatch(context.Background(), &captureLoop{eventErr: context.Canceled}, audioinput.ScheduledInput{PCM: pcm16Bytes(1), EndOfTurn: true}, audioinput.DispatchOptions{}); !errors.Is(err, audioinput.ErrEndOfTurnLost) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled event error = %v, want end-of-turn and cancellation identities", err)
	}

	if _, err := service.ConvertPCM([]byte{1}, audio.SampleRate, audio.SampleRate); !errors.Is(err, audioinput.ErrPCM16Truncated) {
		t.Fatalf("odd ConvertPCM error = %v, want truncation", err)
	}
	if _, err := service.ConvertPCM(pcm16Bytes(1), audio.SampleRate, 44100); err == nil {
		t.Fatal("unsupported ConvertPCM rate unexpectedly succeeded")
	}
}

func pcm16Frame(value int16) []byte {
	samples := make([]int16, audio.FrameSize)
	for index := range samples {
		samples[index] = value + int16(index%7)
	}
	return codec.EncodePCM16(samples)
}

type captureLoop struct {
	mu       sync.Mutex
	frames   [][]byte
	events   []messages.StreamMessage
	frameErr error
	eventErr error
}

func (l *captureLoop) SendAudioInput(_ context.Context, pcm []byte) error {
	if l.frameErr != nil {
		return l.frameErr
	}
	l.mu.Lock()
	l.frames = append(l.frames, append([]byte(nil), pcm...))
	l.mu.Unlock()
	return nil
}

func (l *captureLoop) SendSessionEvent(_ context.Context, event messages.StreamMessage) error {
	if l.eventErr != nil {
		return l.eventErr
	}
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
	return nil
}

func (l *captureLoop) frameCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.frames)
}

type closingReader struct {
	io.Reader
	err    error
	closes int
}

func (r *closingReader) Close() error {
	r.closes++
	return r.err
}

type twoFrameSource struct{ index int }

func (s *twoFrameSource) ReadFrame(_ context.Context, destination []int16) error {
	if s.index >= 2 {
		return io.EOF
	}
	for index := range destination {
		destination[index] = int16(s.index + 1)
	}
	s.index++
	return nil
}

func (*twoFrameSource) Close() error { return nil }

type signalingClock struct {
	*clock.Deterministic
	timerCreated chan struct{}
}

func (c *signalingClock) NewTimer(duration time.Duration) clock.Timer {
	timer := c.Deterministic.NewTimer(duration)
	select {
	case c.timerCreated <- struct{}{}:
	default:
	}
	return timer
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}
