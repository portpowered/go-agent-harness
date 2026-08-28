package chrome

import (
	"net/http"
	"time"
)

const (
	defaultEventBuffer    = 256
	defaultCommandTimeout = 15 * time.Second
)

// RuntimeOptions configures the adapter without exposing a browser protocol
// type. A zero value is valid and receives safe production defaults.
type RuntimeOptions struct {
	EventBuffer    int
	CommandTimeout time.Duration
	HTTPClient     *http.Client
}

// Option customizes a Runtime.
type Option func(*RuntimeOptions)

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

func WithHTTPClient(client *http.Client) Option {
	return func(options *RuntimeOptions) {
		if client != nil {
			options.HTTPClient = client
		}
	}
}

func defaultRuntimeOptions() RuntimeOptions {
	return RuntimeOptions{
		EventBuffer:    defaultEventBuffer,
		CommandTimeout: defaultCommandTimeout,
		HTTPClient:     http.DefaultClient,
	}
}
