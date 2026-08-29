package testkit

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Clock supplies injected monotonic milliseconds.
type Clock interface {
	MonotonicMillis() uint64
}

// ClockFunc adapts a function to Clock.
type ClockFunc func() uint64

func (f ClockFunc) MonotonicMillis() uint64 {
	if f == nil {
		return 0
	}
	return f()
}

// IDSource supplies deterministic opaque IDs.
type IDSource interface {
	NextID(kind string) string
}

// IDSourceFunc adapts a function to IDSource.
type IDSourceFunc func(kind string) string

func (f IDSourceFunc) NextID(kind string) string {
	if f == nil {
		return ""
	}
	return f(kind)
}

// WithClockFunc injects a function-backed monotonic clock.
func WithClockFunc(clock func() uint64) RecorderOption {
	return WithClock(ClockFunc(clock))
}

// WithIDSource injects deterministic ID allocation. A nil source is ignored.
func WithIDSource(source IDSource) RecorderOption {
	return recorderOptionFunc(func(recorder *Recorder) {
		if source != nil {
			recorder.ids = source
		}
	})
}

// WithIDFunc injects a function-backed deterministic ID source.
func WithIDFunc(source func(string) string) RecorderOption {
	return WithIDSource(IDSourceFunc(source))
}

// Recorder appends validated canonical browser event lines to an io.Writer.
// Its zero event time is deterministic; callers needing generated IDs must
// provide an IDSource and use NewID.
type Recorder struct {
	mu            sync.Mutex
	writer        io.Writer
	clock         Clock
	ids           IDSource
	redactor      *Redactor
	redactionErr  error
	nextSequence  uint64
	lastMonotonic uint64
	hasEvents     bool
}

// NewRecorder constructs a recorder. The default clock always returns zero,
// which is useful for deterministic tests and remains valid because equal
// monotonic offsets are allowed.
func NewRecorder(writer io.Writer, options ...RecorderOption) (*Recorder, error) {
	if writer == nil {
		return nil, fmt.Errorf("%w: writer is nil", ErrRecorderWrite)
	}
	recorder := &Recorder{
		writer:       writer,
		clock:        ClockFunc(func() uint64 { return 0 }),
		nextSequence: 1,
	}
	for _, option := range options {
		if option != nil {
			option.applyRecorder(recorder)
		}
	}
	if recorder.redactionErr != nil {
		return nil, recorder.redactionErr
	}
	return recorder, nil
}

// NewID obtains one deterministic ID from the configured source.
func (r *Recorder) NewID(kind string) (string, error) {
	if r == nil {
		return "", ErrIDSourceUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ids == nil {
		return "", ErrIDSourceUnavailable
	}
	id := r.ids.NextID(kind)
	if err := validateOpaqueID(id); err != nil {
		return "", fmt.Errorf("generated %s ID: %w", kind, err)
	}
	return id, nil
}

// Record assigns version, sequence, and injected monotonic time, then writes
// one event. A failed validation or write does not advance recorder state.
func (r *Recorder) Record(input EventInput) (Event, error) {
	if r == nil {
		return Event{}, fmt.Errorf("%w: recorder is nil", ErrRecorderWrite)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.clock == nil {
		return Event{}, fmt.Errorf("%w: clock is nil", ErrRecorderWrite)
	}
	event := Event{
		Version:       BrowserEventsVersion,
		Sequence:      r.nextSequence,
		MonotonicMS:   r.clock.MonotonicMillis(),
		Type:          input.Type,
		BrowserID:     input.BrowserID,
		TargetID:      input.TargetID,
		Generation:    input.Generation,
		Payload:       cloneRaw(input.Payload),
		PayloadSHA256: input.PayloadSHA256,
		Redaction:     input.Redaction,
	}
	if event.Redaction.Mode == "" {
		event.Redaction.Mode = RedactionNone
	}
	if err := r.writeLocked(event); err != nil {
		return Event{}, err
	}
	return event, nil
}

// Write appends an explicitly sequenced event. Sequence zero is assigned the
// next contiguous value; all other sequence values must match the cursor.
func (r *Recorder) Write(event Event) error {
	if r == nil {
		return fmt.Errorf("%w: recorder is nil", ErrRecorderWrite)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if event.Version == "" {
		event.Version = BrowserEventsVersion
	}
	if event.Sequence == 0 {
		event.Sequence = r.nextSequence
	}
	return r.writeLocked(event)
}

func (r *Recorder) writeLocked(event Event) error {
	if r.redactionErr != nil {
		return r.redactionErr
	}
	if r.redactor != nil {
		redacted, err := r.redactor.RedactEvent(event)
		if err != nil {
			return err
		}
		event = redacted
	}
	if event.Sequence != r.nextSequence {
		return newEventValidationError(int(r.nextSequence), "sequence", "want %d, got %d", r.nextSequence, event.Sequence)
	}
	if r.hasEvents && event.MonotonicMS < r.lastMonotonic {
		return fmt.Errorf("%w: previous=%d current=%d", ErrRecorderClock, r.lastMonotonic, event.MonotonicMS)
	}
	if err := validateEvent(event); err != nil {
		return err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("%w: encode event: %v", ErrRecorderWrite, err)
	}
	encoded = append(encoded, '\n')
	n, writeErr := r.writer.Write(encoded)
	if writeErr == nil && n != len(encoded) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return fmt.Errorf("%w: %v", ErrRecorderWrite, writeErr)
	}
	if event.Sequence == ^uint64(0) {
		return fmt.Errorf("%w: sequence overflow", ErrRecorderWrite)
	}
	r.nextSequence = event.Sequence + 1
	r.lastMonotonic = event.MonotonicMS
	r.hasEvents = true
	return nil
}
