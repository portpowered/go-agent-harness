package transcript

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

var ErrUnsupportedConsumer = errors.New("transcript: unsupported live consumer")

// RecordSink accepts one transcript record. Writer implements this interface.
type RecordSink interface {
	Write(Record) error
}

// RecordConsumer is the live side of a Tee. The returned count and error are
// returned to the caller unchanged; a positive count means the frame was
// observed and should also be recorded.
type RecordConsumer interface {
	Write(Record) (int, error)
}

// RecordConsumerFunc adapts a function to RecordConsumer.
type RecordConsumerFunc func(Record) (int, error)

func (f RecordConsumerFunc) Write(record Record) (int, error) { return f(record) }

// TeeConfig controls the optional one-shot reporter for transcript failures.
type TeeConfig struct {
	Reporter   func(error)
	OnDegraded func(error)
}

// TeeOption mutates TeeConfig during construction.
type TeeOption func(*TeeConfig)

// WithTeeReporter installs the tee's one-shot transcript degradation reporter.
func WithTeeReporter(reporter func(error)) TeeOption {
	return func(config *TeeConfig) {
		config.Reporter = reporter
		config.OnDegraded = reporter
	}
}

// Tee composes a live record consumer with a secondary transcript sink. The
// live call is made first and its result is authoritative.
type Tee struct {
	live       func(Record) (int, error)
	transcript RecordSink
	liveErr    error
	reporter   func(error)
	reportOnce sync.Once
	closeOnce  sync.Once
	closeErr   error
}

// NewTee accepts a RecordConsumer, a compatible Consume method/function, an
// io.Writer, or a RecordSink. The broad constructor boundary lets a caller
// tee either already-framed records or the exact JSONL bytes consumed by an
// existing byte-oriented live path.
func NewTee(live any, transcript RecordSink, options ...TeeOption) *Tee {
	config := TeeConfig{}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	consumer, consumerErr := normalizeConsumer(live)
	return &Tee{
		live:       consumer,
		transcript: transcript,
		liveErr:    consumerErr,
		reporter:   teeReporter(config),
	}
}

// NewTeeWithReporter is a convenience constructor for the common reporter
// configuration without a TeeOption value.
func NewTeeWithReporter(live any, transcript RecordSink, reporter func(error)) *Tee {
	return NewTee(live, transcript, WithTeeReporter(reporter))
}

// NewTransparentTee is a descriptive constructor alias.
func NewTransparentTee(live any, transcript RecordSink, options ...TeeOption) *Tee {
	return NewTee(live, transcript, options...)
}

// Write preserves the live consumer's returned count and error. Transcript
// failures are reported and intentionally excluded from the returned result.
func (t *Tee) Write(record Record) (int, error) {
	if t == nil {
		return 0, nil
	}
	if t.liveErr != nil {
		return 0, t.liveErr
	}
	if t.live == nil {
		return 0, nil
	}

	// The live consumer receives the caller's original value. The transcript
	// gets an owned payload copy so a live implementation that reuses or mutates
	// its buffer cannot change what the tee records after the live call.
	recordForTranscript := record
	if t.transcript != nil {
		recordForTranscript.Payload = append([]byte(nil), record.Payload...)
	}

	accepted, liveErr := t.live(record)
	if accepted > 0 && t.transcript != nil {
		if err := t.transcript.Write(recordForTranscript); err != nil {
			t.reportOnce.Do(func() {
				notifyDegradation(t.reporter, err)
			})
		}
	}
	return accepted, liveErr
}

// Close closes the transcript sink when it exposes io.Closer. The live
// consumer is not owned by the tee and is never closed.
func (t *Tee) Close() error {
	if t == nil {
		return nil
	}
	t.closeOnce.Do(func() {
		if closer, ok := t.transcript.(io.Closer); ok {
			t.closeErr = closer.Close()
		}
	})
	return t.closeErr
}

func teeReporter(config TeeConfig) func(error) {
	if config.Reporter != nil {
		return config.Reporter
	}
	return config.OnDegraded
}

func normalizeConsumer(live any) (func(Record) (int, error), error) {
	switch consumer := live.(type) {
	case nil:
		return nil, nil
	case RecordConsumerFunc:
		return consumer, nil
	case func(Record) (int, error):
		return consumer, nil
	case RecordConsumer:
		return consumer.Write, nil
	case interface{ Consume(Record) (int, error) }:
		return consumer.Consume, nil
	case func(Record) error:
		return func(record Record) (int, error) {
			if err := consumer(record); err != nil {
				return 0, err
			}
			return 1, nil
		}, nil
	case interface{ Consume(Record) error }:
		return func(record Record) (int, error) {
			if err := consumer.Consume(record); err != nil {
				return 0, err
			}
			return 1, nil
		}, nil
	case RecordSink:
		return func(record Record) (int, error) {
			if err := consumer.Write(record); err != nil {
				return 0, err
			}
			return 1, nil
		}, nil
	case io.Writer:
		return func(record Record) (int, error) {
			encoded, err := Encode(record)
			if err != nil {
				return 0, fmt.Errorf("transcript: encode live record: %w", err)
			}
			return consumer.Write(encoded)
		}, nil
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnsupportedConsumer, live)
	}
}
