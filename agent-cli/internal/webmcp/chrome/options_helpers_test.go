package chrome

import "time"

func WithEventBuffer(size int) Option {
	return func(options *RuntimeOptions) {
		if size > 0 {
			options.EventBuffer = size
		}
	}
}

func WithCommandTimeout(timeout time.Duration) Option {
	return func(options *RuntimeOptions) {
		if timeout > 0 {
			options.CommandTimeout = timeout
		}
	}
}

// WithWireTraceSink records safe target/session and CDP method evidence at
// the command boundary. The sink never receives endpoint, input, or output
// values from the adapter.
func WithWireTraceSink(sink WireTraceSink) Option {
	return func(options *RuntimeOptions) {
		if sink != nil {
			options.WireTrace = sink
		}
	}
}
