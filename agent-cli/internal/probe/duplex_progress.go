package probe

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// nilErrorText is what a nil typed error reports from Error, matching fmt's
// rendering of a nil value.
const nilErrorText = "<nil>"

const duplexProgressOutputWindow = 64 * 1024

type duplexProgressState struct {
	mu              sync.Mutex
	changed         chan struct{}
	startedAt       time.Time
	inputBytes      int64
	inputFrames     int
	outputBytes     int64
	outputReads     int
	inputSegments   int
	output          []DuplexOutputEvent
	outputData      []byte
	outputSequence  []byte
	sequenceMatched bool
	outputClosed    bool
}

func newDuplexProgressState() *duplexProgressState {
	return &duplexProgressState{changed: make(chan struct{})}
}

func (s *duplexProgressState) snapshot() DuplexProgressSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return DuplexProgressSnapshot{
		At:            s.elapsedLocked(),
		InputBytes:    s.inputBytes,
		InputFrames:   s.inputFrames,
		OutputBytes:   s.outputBytes,
		OutputReads:   s.outputReads,
		InputSegments: s.inputSegments,
		OutputClosed:  s.outputClosed,
	}
}

func (s *duplexProgressState) setStartedAt(startedAt time.Time) {
	s.mu.Lock()
	s.startedAt = startedAt
	s.mu.Unlock()
}

func (s *duplexProgressState) elapsed() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.elapsedLocked()
}

func (s *duplexProgressState) elapsedLocked() time.Duration {
	if s.startedAt.IsZero() {
		return 0
	}
	return time.Since(s.startedAt)
}

func (s *duplexProgressState) noteInputSegment() {
	s.mu.Lock()
	s.inputSegments++
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func (s *duplexProgressState) noteInput(data []byte) {
	s.mu.Lock()
	s.inputBytes += int64(len(data))
	s.inputFrames++
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func (s *duplexProgressState) noteOutput(event DuplexOutputEvent, data []byte) {
	s.mu.Lock()
	s.outputBytes += int64(event.Bytes)
	s.outputReads++
	event.Total = s.outputBytes
	event.Read = s.outputReads
	s.output = append(s.output, event)
	// Keep enough recent output for a gate installed after its marker crossed
	// stdout. A gate already waiting remembers its match before older bytes are
	// evicted from this bounded window.
	s.outputData = append(s.outputData, data...)
	if len(s.outputSequence) > 0 && bytes.Contains(s.outputData, s.outputSequence) {
		s.sequenceMatched = true
	}
	if len(s.outputData) > duplexProgressOutputWindow {
		s.outputData = append([]byte(nil), s.outputData[len(s.outputData)-duplexProgressOutputWindow:]...)
	}
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func (s *duplexProgressState) waitForOutputSequence(ctx context.Context, sequence []byte) error {
	for {
		s.mu.Lock()
		if !bytes.Equal(s.outputSequence, sequence) {
			s.outputSequence = append([]byte(nil), sequence...)
			s.sequenceMatched = bytes.Contains(s.outputData, sequence)
		}
		if s.sequenceMatched {
			s.mu.Unlock()
			return nil
		}
		if s.outputClosed {
			s.mu.Unlock()
			return fmt.Errorf("%w: stdout closed before output sequence", ErrDuplexPipe)
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *duplexProgressState) waitForOutput(ctx context.Context, minimum int64, reads bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.mu.Lock()
		met := s.outputBytes >= minimum
		if reads {
			met = int64(s.outputReads) >= minimum
		}
		if met {
			s.mu.Unlock()
			return nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *duplexProgressState) outputEvents() []DuplexOutputEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DuplexOutputEvent(nil), s.output...)
}

func (s *duplexProgressState) waitForChange(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.outputClosed {
		s.mu.Unlock()
		return nil
	}
	changed := s.changed
	s.mu.Unlock()
	select {
	case <-changed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *duplexProgressState) noteOutputClosed() {
	s.mu.Lock()
	if !s.outputClosed {
		s.outputClosed = true
		close(s.changed)
		s.changed = make(chan struct{})
	}
	s.mu.Unlock()
}

func (s *duplexProgressState) outputIsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outputClosed
}

// WaitForOutputSequence waits until the exact sequence has crossed stdout.
// Segment gates are sequential; only one sequence waiter may be active at a time.
func (p *DuplexProgress) WaitForOutputSequence(ctx context.Context, sequence []byte) error {
	if len(sequence) == 0 {
		return nil
	}
	if len(sequence) > duplexProgressOutputWindow {
		return fmt.Errorf("%w: output sequence exceeds %d-byte progress window", ErrDuplexConfigInvalid, duplexProgressOutputWindow)
	}
	if p == nil || p.state == nil {
		return fmt.Errorf("%w: output progress is unavailable", ErrDuplexPipe)
	}
	if ctx == nil {
		return fmt.Errorf("%w: output sequence context is required", ErrDuplexConfigInvalid)
	}
	return p.state.waitForOutputSequence(ctx, sequence)
}

func validateDuplexSegment(segment DuplexAudioSegment) error {
	if len(segment.PCM16)%2 != 0 {
		return fmt.Errorf("%w: segment %q has odd PCM16 length %d", ErrDuplexInputInvalid, segment.ID, len(segment.PCM16))
	}
	if segment.DelayBefore < 0 || segment.SilenceFor < 0 || segment.WaitForOutputBytes < 0 || segment.WaitForOutputReads < 0 {
		return fmt.Errorf("%w: segment %q has a negative delay, silence, or output gate", ErrDuplexConfigInvalid, segment.ID)
	}
	if len(segment.WaitForOutputSequence) > duplexProgressOutputWindow {
		return fmt.Errorf("%w: segment %q output sequence exceeds %d-byte progress window", ErrDuplexConfigInvalid, segment.ID, duplexProgressOutputWindow)
	}
	if len(segment.PCM16) == 0 && segment.SilenceFor <= 0 {
		return fmt.Errorf("%w: segment %q has no PCM16 or silence duration", ErrDuplexInputInvalid, segment.ID)
	}
	return nil
}

func (p *DuplexProgress) waitForSegmentOutput(ctx context.Context, segment DuplexAudioSegment) error {
	if err := p.WaitForOutputBytes(ctx, segment.WaitForOutputBytes); err != nil {
		return duplexPipeError("wait for output bytes", err)
	}
	if err := p.WaitForOutputSequence(ctx, segment.WaitForOutputSequence); err != nil {
		return duplexPipeError("wait for output sequence", err)
	}
	if err := p.WaitForOutputReads(ctx, segment.WaitForOutputReads); err != nil {
		return duplexPipeError("wait for output reads", err)
	}
	return nil
}

// DuplexProgressSnapshot is a point-in-time view available to segment gates.
type DuplexProgressSnapshot struct {
	At            time.Duration
	InputBytes    int64
	InputFrames   int
	OutputBytes   int64
	OutputReads   int
	InputSegments int
	OutputClosed  bool
}

// DuplexProgress exposes only observable stream progress to a segment gate.
// It does not expose the child process or a direct product/runtime call.
type DuplexProgress struct {
	state *duplexProgressState
}

// Snapshot returns the progress observed so far.
func (p *DuplexProgress) Snapshot() DuplexProgressSnapshot {
	if p == nil || p.state == nil {
		return DuplexProgressSnapshot{}
	}
	return p.state.snapshot()
}

// WaitForOutputBytes waits until at least minimum output bytes crossed the
// child stdout boundary. A non-positive minimum returns immediately.
func (p *DuplexProgress) WaitForOutputBytes(ctx context.Context, minimum int64) error {
	if minimum <= 0 {
		return nil
	}
	if p == nil || p.state == nil {
		return fmt.Errorf("%w: output progress is unavailable", ErrDuplexPipe)
	}
	return p.state.waitForOutput(ctx, minimum, false)
}

// WaitForOutputReads waits until at least minimum stdout reads have completed.
func (p *DuplexProgress) WaitForOutputReads(ctx context.Context, minimum int) error {
	if minimum <= 0 {
		return nil
	}
	if p == nil || p.state == nil {
		return fmt.Errorf("%w: output progress is unavailable", ErrDuplexPipe)
	}
	return p.state.waitForOutput(ctx, int64(minimum), true)
}

// Elapsed returns the runner's monotonic elapsed time at the instant of the
// snapshot. Segment gates use this to drive event-based policies while the
// child remains open.
func (p *DuplexProgress) Elapsed() time.Duration {
	if p == nil || p.state == nil {
		return 0
	}
	return p.state.elapsed()
}

// OutputEvents returns a copy of every stdout read observed so far. The
// events retain their process-relative timestamps so callers can correlate
// incremental output with another event ledger without assuming one read is
// one response.
func (p *DuplexProgress) OutputEvents() []DuplexOutputEvent {
	if p == nil || p.state == nil {
		return nil
	}
	return p.state.outputEvents()
}

// WaitForChange blocks until the child produces another observed output read
// or the stdout pump closes. It is the non-polling wake-up primitive used by
// the patience controller while the input pump keeps the process alive.
func (p *DuplexProgress) WaitForChange(ctx context.Context) error {
	if p == nil || p.state == nil {
		return fmt.Errorf("%w: progress is unavailable", ErrDuplexPipe)
	}
	return p.state.waitForChange(ctx)
}

// OutputClosed reports that the stdout pump has observed EOF or stopped after
// cancellation. It is an observable terminal boundary, not a claim that the
// child has been reaped.
func (p *DuplexProgress) OutputClosed() bool {
	if p == nil || p.state == nil {
		return false
	}
	return p.state.outputIsClosed()
}

// startupReader reports the child's first output byte, which marks the end
// of process startup for the duplex deadline.
type startupReader struct {
	io.Reader
	mark func()
}

func (r startupReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.mark()
	}
	return n, err
}
