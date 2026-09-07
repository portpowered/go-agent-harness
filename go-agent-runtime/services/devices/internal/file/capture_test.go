package file

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestCapturePacingUsesConsumedSamplesAfterShortRead(t *testing.T) {
	source := &shortSampleSource{}
	scheduler := &recordingScheduler{}
	capture, err := newCapture(devices.FileInput{
		Source:     source,
		SampleRate: 16_000,
		Pace:       true,
		Scheduler:  scheduler,
	}, 16_000)
	if err != nil {
		t.Fatalf("newCapture: %v", err)
	}
	outbound := &recordingOutbound{}
	if err := capture.Pump(context.Background(), outbound); err != nil {
		t.Fatalf("Pump: %v", err)
	}

	scheduler.mu.Lock()
	durations := append([]time.Duration(nil), scheduler.durations...)
	scheduler.mu.Unlock()
	if len(durations) != 1 {
		t.Fatalf("paced timer count = %d, want one timer before terminal short read (%v)", len(durations), durations)
	}
	if durations[0] != 15*time.Millisecond {
		t.Fatalf("first paced timer = %s, want 15ms for 240 consumed samples at 16kHz", durations[0])
	}
	if len(outbound.frames) != 1 || len(outbound.frames[0].Samples) != sharedaudio.FrameSize {
		t.Fatalf("provider frames = %d/%d samples, want one 480-sample frame", len(outbound.frames), frameSampleCount(outbound.frames))
	}
}

func TestContinuousCaptureForwardsFirstProcessedFrameBeforeNextRead(t *testing.T) {
	readNext := make(chan struct{})
	source := &gatedSampleSource{releaseNext: readNext}
	capture, err := newCapture(devices.FileInput{
		Source:     source,
		SampleRate: 16_000,
		Continuous: true,
	}, 16_000)
	if err != nil {
		t.Fatalf("newCapture: %v", err)
	}
	outbound := &recordingOutbound{frameReady: make(chan struct{}, 16)}
	errCh := make(chan error, 1)
	go func() { errCh <- capture.Pump(context.Background(), outbound) }()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	select {
	case <-outbound.frameReady:
	case <-deadline.C:
		t.Fatal("continuous capture withheld its first processed frame")
	}
	frames := outbound.snapshot()
	if got := len(frames[0].Samples); got != sharedaudio.FrameSize {
		t.Fatalf("first continuous frame samples = %d, want %d", got, sharedaudio.FrameSize)
	}
	close(readNext)
	if err := <-errCh; err != nil {
		t.Fatalf("Pump: %v", err)
	}
}

func TestContinuousCaptureKeepsSilenceZeroAcrossResamplingBoundary(t *testing.T) {
	releaseEOF := make(chan struct{})
	source := &speechThenSilenceSource{releaseEOF: releaseEOF}
	capture, err := newCapture(devices.FileInput{
		Source:     source,
		SampleRate: 16_000,
		Continuous: true,
	}, 24_000)
	if err != nil {
		t.Fatalf("newCapture: %v", err)
	}
	outbound := &recordingOutbound{frameReady: make(chan struct{}, 16)}
	errCh := make(chan error, 1)
	go func() { errCh <- capture.Pump(context.Background(), outbound) }()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for index := 0; index < 2; index++ {
		select {
		case <-outbound.frameReady:
		case <-deadline.C:
			t.Fatal("continuous capture did not emit speech and silence frames")
		}
	}
	frames := outbound.snapshot()
	for _, sample := range frames[1].Samples {
		if sample != 0 {
			t.Fatalf("resampled silence leaked sample %d", sample)
		}
	}
	close(releaseEOF)
	if err := <-errCh; err != nil {
		t.Fatalf("Pump: %v", err)
	}
}

func TestContinuousCaptureUsesBoundaryWithoutFiniteResamplerTail(t *testing.T) {
	outbound := &recordingOutbound{}
	boundaryFrames := -1
	capture, err := newCapture(devices.FileInput{
		Source:     &shortSampleSource{},
		SampleRate: 16_000,
		Continuous: true,
		OnTurnBoundary: func(context.Context) error {
			boundaryFrames = len(outbound.snapshot())
			return nil
		},
	}, 24_000)
	if err != nil {
		t.Fatalf("newCapture: %v", err)
	}
	if err := capture.Pump(context.Background(), outbound); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	frames := outbound.snapshot()
	if boundaryFrames != len(frames) {
		t.Fatalf("frames at continuous boundary = %d, final frames = %d; boundary callback must follow all admitted media", boundaryFrames, len(frames))
	}
	if len(frames) != 2 {
		t.Fatalf("continuous resampled frames = %d, want two available frames without a finite EOF tail", len(frames))
	}
	for index, frame := range frames {
		if frame.EndOfResponse {
			t.Fatalf("continuous frame %d carried EndOfResponse, want explicit capture boundary control", index)
		}
	}
}

func TestCaptureEndOfTurnFlushesExactTailAndResumes(t *testing.T) {
	first := make([]int16, sharedaudio.FrameSize+1)
	first[0] = 11
	first[len(first)-1] = 12
	second := []int16{21}
	source := &markedSampleSource{turns: [][]int16{first, second}, markerAfter: []bool{true, false}}
	boundaries := 0
	capture, err := newCapture(devices.FileInput{
		Source:     source,
		SampleRate: 16_000,
		OnTurnBoundary: func(context.Context) error {
			boundaries++
			return nil
		},
	}, 16_000)
	if err != nil {
		t.Fatalf("newCapture: %v", err)
	}
	outbound := &recordingOutbound{}
	if err := capture.Pump(context.Background(), outbound); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if boundaries != 2 {
		t.Fatalf("boundary callbacks = %d, want explicit marker plus final EOF", boundaries)
	}
	if len(outbound.frames) != 3 {
		t.Fatalf("provider frames = %d, want full frame plus two exact tails", len(outbound.frames))
	}
	wantLengths := []int{sharedaudio.FrameSize, 1, 1}
	wantEpochs := []uint64{0, 0, 1}
	wantSequences := []uint64{0, 1, 0}
	wantStarts := []uint64{0, sharedaudio.FrameSize, 0}
	for index, frame := range outbound.frames {
		if len(frame.Samples) != wantLengths[index] {
			t.Errorf("frame %d samples = %d, want %d", index, len(frame.Samples), wantLengths[index])
		}
		if frame.Epoch != wantEpochs[index] || frame.Sequence != wantSequences[index] || frame.StartSample != wantStarts[index] {
			t.Errorf("frame %d lineage = epoch %d sequence %d start %d, want epoch %d sequence %d start %d", index, frame.Epoch, frame.Sequence, frame.StartSample, wantEpochs[index], wantSequences[index], wantStarts[index])
		}
		if frame.EndOfResponse != (index != 0) {
			t.Errorf("frame %d EndOfResponse = %t, want %t", index, frame.EndOfResponse, index != 0)
		}
	}
	if got := outbound.frames[1].Samples[0]; got != first[len(first)-1] {
		t.Errorf("first tail sample = %d, want %d", got, first[len(first)-1])
	}
	if got := outbound.frames[2].Samples[0]; got != second[0] {
		t.Errorf("second turn sample = %d, want %d", got, second[0])
	}
}

func TestCaptureEndOfTurnThenEOFDoesNotDuplicateCommit(t *testing.T) {
	source := &markedSampleSource{
		turns:       [][]int16{{7}},
		markerAfter: []bool{true},
	}
	boundaries := 0
	capture, err := newCapture(devices.FileInput{
		Source:     source,
		SampleRate: 16_000,
		OnTurnBoundary: func(context.Context) error {
			boundaries++
			return nil
		},
	}, 16_000)
	if err != nil {
		t.Fatalf("newCapture: %v", err)
	}
	outbound := &recordingOutbound{}
	if err := capture.Pump(context.Background(), outbound); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if boundaries != 1 {
		t.Fatalf("boundary callbacks = %d, want one for marker followed by EOF", boundaries)
	}
	if len(outbound.frames) != 1 || len(outbound.frames[0].Samples) != 1 {
		t.Fatalf("provider frames = %d/%d, want one exact sample", len(outbound.frames), frameSampleCount(outbound.frames))
	}
	if !outbound.frames[0].EndOfResponse {
		t.Fatal("marker-flushed frame did not carry EndOfResponse")
	}
}

type markedSampleSource struct {
	turns       [][]int16
	markerAfter []bool
	turn        int
	position    int
	marker      bool
}

func (s *markedSampleSource) ReadFrame(context.Context, []int16) error { return io.EOF }

func (s *markedSampleSource) ReadSamples(_ context.Context, buf []int16) (int, error) {
	if s.marker {
		s.marker = false
		s.turn++
		s.position = 0
		return 0, sharedaudio.ErrEndOfTurn
	}
	if s.turn >= len(s.turns) {
		return 0, io.EOF
	}
	samples := s.turns[s.turn]
	if s.position >= len(samples) {
		return 0, io.EOF
	}
	count := copy(buf, samples[s.position:])
	s.position += count
	if s.position == len(samples) && s.turn < len(s.markerAfter) && s.markerAfter[s.turn] {
		s.marker = true
	}
	return count, nil
}

func (*markedSampleSource) Close() error { return nil }

func frameSampleCount(frames []sharedaudio.PCMFrame) int {
	total := 0
	for _, frame := range frames {
		total += len(frame.Samples)
	}
	return total
}

type shortSampleSource struct {
	read int
}

func (s *shortSampleSource) ReadFrame(context.Context, []int16) error { return io.EOF }

func (s *shortSampleSource) ReadSamples(_ context.Context, buf []int16) (int, error) {
	s.read++
	count := 240
	for index := 0; index < count; index++ {
		buf[index] = int16(index + 1)
	}
	if s.read == 2 {
		return count, io.EOF
	}
	return count, nil
}

func (*shortSampleSource) Close() error { return nil }

type gatedSampleSource struct {
	releaseNext <-chan struct{}
	read        int
}

func (s *gatedSampleSource) ReadFrame(context.Context, []int16) error { return io.EOF }

func (s *gatedSampleSource) ReadSamples(ctx context.Context, buf []int16) (int, error) {
	s.read++
	if s.read > 1 {
		select {
		case <-s.releaseNext:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		return 0, io.EOF
	}
	for index := range buf {
		buf[index] = int16(index + 1)
	}
	return len(buf), nil
}

func (*gatedSampleSource) Close() error { return nil }

type speechThenSilenceSource struct {
	releaseEOF <-chan struct{}
	read       int
}

func (s *speechThenSilenceSource) ReadFrame(context.Context, []int16) error { return io.EOF }

func (s *speechThenSilenceSource) ReadSamples(ctx context.Context, buf []int16) (int, error) {
	s.read++
	switch s.read {
	case 1:
		for index := range buf {
			buf[index] = 257
		}
		return len(buf), nil
	case 2:
		clear(buf)
		return len(buf), nil
	default:
		select {
		case <-s.releaseEOF:
			return 0, io.EOF
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

func (*speechThenSilenceSource) Close() error { return nil }

type recordingOutbound struct {
	mu         sync.Mutex
	frames     []sharedaudio.PCMFrame
	frameReady chan struct{}
}

func (o *recordingOutbound) WriteFrame(_ context.Context, frame sharedaudio.PCMFrame) error {
	frame.Samples = append([]int16(nil), frame.Samples...)
	o.mu.Lock()
	o.frames = append(o.frames, frame)
	if o.frameReady == nil {
		o.frameReady = make(chan struct{}, 16)
	}
	ready := o.frameReady
	o.mu.Unlock()
	select {
	case ready <- struct{}{}:
	default:
	}
	return nil
}

func (o *recordingOutbound) snapshot() []sharedaudio.PCMFrame {
	o.mu.Lock()
	defer o.mu.Unlock()
	frames := make([]sharedaudio.PCMFrame, len(o.frames))
	for index, frame := range o.frames {
		frames[index] = frame
		frames[index].Samples = append([]int16(nil), frame.Samples...)
	}
	return frames
}

func (*recordingOutbound) Close() error { return nil }

type recordingScheduler struct {
	mu        sync.Mutex
	durations []time.Duration
}

func (s *recordingScheduler) Now() time.Time { return time.Unix(0, 0).UTC() }

func (s *recordingScheduler) NewTimer(duration time.Duration) platformclock.Timer {
	s.mu.Lock()
	s.durations = append(s.durations, duration)
	s.mu.Unlock()
	channel := make(chan time.Time, 1)
	channel <- s.Now()
	return recordingTimer{channel: channel}
}

func (s *recordingScheduler) Wait(ctx context.Context, duration time.Duration) error {
	return platformclock.Real{}.Wait(ctx, duration)
}

func (s *recordingScheduler) WithDeadline(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return platformclock.Real{}.WithDeadline(ctx, deadline)
}

func (s *recordingScheduler) WithTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return platformclock.Real{}.WithTimeout(ctx, timeout)
}

type recordingTimer struct {
	channel <-chan time.Time
}

func (t recordingTimer) C() <-chan time.Time { return t.channel }
func (t recordingTimer) Stop() bool          { return true }

var _ sharedaudio.SampleSource = (*shortSampleSource)(nil)
var _ sharedaudio.SampleSource = (*gatedSampleSource)(nil)
var _ sharedaudio.SampleSource = (*speechThenSilenceSource)(nil)
var _ sharedaudio.OutboundMedia = (*recordingOutbound)(nil)
var _ platformclock.Scheduler = (*recordingScheduler)(nil)
