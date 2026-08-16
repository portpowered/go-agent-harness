package logging

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode"
)

// This is a placeholder for logging functionality.
// Users can inject their own logging implementation by implementing the Logger interface.

type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
	Fatal(msg string, fields ...Field)
	Panic(msg string, fields ...Field)
}

type Field struct {
	Key   string
	Value any
}

// CrossingDirection identifies which side of a typed buffer a crossing came
// from. It is intentionally closed so a typo cannot silently create a second
// direction in log analysis.
type CrossingDirection string

const (
	CrossingDirectionIn  CrossingDirection = "in"
	CrossingDirectionOut CrossingDirection = "out"
)

// CrossingModality identifies the content modality carried by a crossing.
// The emitter validates that it is present, while allowing new modalities to
// be introduced without changing this package's API.
type CrossingModality string

const (
	CrossingModalityText  CrossingModality = "text"
	CrossingModalityAudio CrossingModality = "audio"
	CrossingModalityImage CrossingModality = "image"
	CrossingModalityVideo CrossingModality = "video"
	CrossingModalityTool  CrossingModality = "tool"
	CrossingModalityData  CrossingModality = "data"
)

const (
	// CrossingLogMessage is the single message used for every crossing record.
	CrossingLogMessage = "typed_buffer_crossing"

	CrossingFieldDirection   = "direction"
	CrossingFieldBuffer      = "buffer"
	CrossingFieldMessageType = "type"
	CrossingFieldModality    = "modality"
	CrossingFieldByteSize    = "byte_size"
	CrossingFieldSequence    = "sequence"
	CrossingFieldLogicalTick = "logical_tick"
)

var (
	// ErrInvalidCrossingEvent identifies metadata that cannot be emitted as a
	// canonical crossing record.
	ErrInvalidCrossingEvent = errors.New("invalid typed-buffer crossing event")
	// ErrCrossingSequenceExhausted identifies the only state in which a new
	// automatically assigned sequence cannot be represented.
	ErrCrossingSequenceExhausted = errors.New("typed-buffer crossing sequence exhausted")
)

// CrossingEvent is the payload-free metadata contract for one typed-buffer
// crossing. Sequence is assigned by CrossingEmitter when it is zero; a
// non-zero sequence is accepted only when it is strictly after the emitter's
// last sequence. LogicalTick is supplied by the caller and is never changed.
//
// There is deliberately no message or payload field in this type. Callers
// must calculate ByteSize at the crossing boundary and pass only that size.
type CrossingEvent struct {
	Direction   CrossingDirection `json:"direction"`
	Buffer      string            `json:"buffer"`
	MessageType string            `json:"type"`
	Modality    CrossingModality  `json:"modality"`
	ByteSize    int               `json:"byte_size"`
	Sequence    uint64            `json:"sequence"`
	LogicalTick uint64            `json:"logical_tick"`
}

// Validate checks all caller-controlled crossing metadata before any log call
// or sequence state mutation occurs. Sequence zero is the documented
// auto-assignment sentinel and is therefore valid here.
func (event CrossingEvent) Validate() error {
	if event.Direction != CrossingDirectionIn && event.Direction != CrossingDirectionOut {
		return fmt.Errorf("%w: direction %q is not %q or %q", ErrInvalidCrossingEvent, event.Direction, CrossingDirectionIn, CrossingDirectionOut)
	}
	if err := validateCrossingLabel(event.Buffer, CrossingFieldBuffer); err != nil {
		return err
	}
	if err := validateCrossingLabel(event.MessageType, CrossingFieldMessageType); err != nil {
		return err
	}
	if err := validateCrossingLabel(string(event.Modality), CrossingFieldModality); err != nil {
		return err
	}
	if event.ByteSize < 0 {
		return fmt.Errorf("%w: %s cannot be negative", ErrInvalidCrossingEvent, CrossingFieldByteSize)
	}
	return nil
}

func validateCrossingLabel(value, field string) error {
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidCrossingEvent, field)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s cannot have leading or trailing whitespace", ErrInvalidCrossingEvent, field)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s contains a control character", ErrInvalidCrossingEvent, field)
		}
	}
	return nil
}

// CrossingEmitter assigns/validates sequence state and emits exactly one
// structured Info record for each valid event. The mutex covers both sequence
// assignment and the logger call, so concurrent producers cannot duplicate,
// reorder, or tear records through this emitter.
type CrossingEmitter struct {
	mu           sync.Mutex
	logger       Logger
	nextSequence uint64
}

// NewCrossingEmitter creates a concurrency-safe crossing emitter. A nil logger
// is replaced by DummyLogger so callers can opt out without a special branch;
// event validation and sequence tracking remain active in that case.
func NewCrossingEmitter(logger Logger) *CrossingEmitter {
	if logger == nil {
		logger = DummyLogger()
	}
	return &CrossingEmitter{logger: logger}
}

// Emit validates event metadata, assigns or validates its sequence, and emits
// one canonical payload-free record. The returned event is the exact metadata
// sent to the logger, including the assigned sequence.
func (e *CrossingEmitter) Emit(event CrossingEvent) (CrossingEvent, error) {
	if e == nil {
		return CrossingEvent{}, fmt.Errorf("%w: nil emitter", ErrInvalidCrossingEvent)
	}
	if err := event.Validate(); err != nil {
		return CrossingEvent{}, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	sequence := event.Sequence
	if sequence == 0 {
		if e.nextSequence == math.MaxUint64 {
			return CrossingEvent{}, ErrCrossingSequenceExhausted
		}
		sequence = e.nextSequence + 1
	} else if sequence <= e.nextSequence {
		return CrossingEvent{}, fmt.Errorf("%w: sequence %d is not after %d", ErrInvalidCrossingEvent, sequence, e.nextSequence)
	}

	event.Sequence = sequence
	e.nextSequence = sequence
	e.logger.Info(CrossingLogMessage, crossingFields(event)...)
	return event, nil
}

// LastSequence returns the latest sequence emitted by e. It is useful to
// expose the emitter's correlation state without exposing its mutex or logger.
func (e *CrossingEmitter) LastSequence() uint64 {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nextSequence
}

func crossingFields(event CrossingEvent) []Field {
	return []Field{
		{Key: CrossingFieldDirection, Value: event.Direction},
		{Key: CrossingFieldBuffer, Value: event.Buffer},
		{Key: CrossingFieldMessageType, Value: event.MessageType},
		{Key: CrossingFieldModality, Value: event.Modality},
		{Key: CrossingFieldByteSize, Value: event.ByteSize},
		{Key: CrossingFieldSequence, Value: event.Sequence},
		{Key: CrossingFieldLogicalTick, Value: event.LogicalTick},
	}
}

// DummyLogger is a logger that does nothing.
func DummyLogger() Logger {
	return &dummyLogger{}
}

type dummyLogger struct {
}

func (d *dummyLogger) Debug(msg string, fields ...Field) {
}

func (d *dummyLogger) Info(msg string, fields ...Field) {
}

func (d *dummyLogger) Warn(msg string, fields ...Field) {
}

func (d *dummyLogger) Error(msg string, fields ...Field) {
}

func (d *dummyLogger) Fatal(msg string, fields ...Field) {
}

func (d *dummyLogger) Panic(msg string, fields ...Field) {
}

// Ensure dummyLogger implements Logger.
var _ Logger = (*dummyLogger)(nil)
