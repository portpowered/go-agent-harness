package hermetic

import (
	"fmt"
	"io"
	"strings"
)

// WithFixtureClockFunc injects a function-backed monotonic clock.
func WithFixtureClockFunc(clock func() uint64) FixtureRuntimeOption {
	return WithFixtureClock(ClockFunc(clock))
}

// WithFixtureIDFunc injects a function-backed deterministic ID source.
func WithFixtureIDFunc(source func(string) string) FixtureRuntimeOption {
	return WithFixtureIDSource(IDSourceFunc(source))
}

// WithFixtureBrowserID sets the opaque browser ID used in neutral events.
func WithFixtureBrowserID(browserID string) FixtureRuntimeOption {
	return func(runtime *BrowserScriptRuntime) {
		if strings.TrimSpace(browserID) != "" {
			runtime.browserID = browserID
		}
	}
}

// WithFixtureTargetID selects a target by ID. Without this option the first
// endpoint target is selected.
func WithFixtureTargetID(targetID string) FixtureRuntimeOption {
	return func(runtime *BrowserScriptRuntime) {
		if strings.TrimSpace(targetID) != "" {
			runtime.targetID = targetID
		}
	}
}

// WithFixtureState creates an out-of-band state oracle from a JSON value.
func WithFixtureState(value any) FixtureRuntimeOption {
	return func(runtime *BrowserScriptRuntime) {
		oracle, err := NewFixtureStateOracle(value)
		if err == nil {
			runtime.state = oracle
		} else {
			runtime.optionErr = err
		}
	}
}

// NewRuntime accepts either a BrowserScript value or pointer for convenient
// use by callers that load a script through a pointer-oriented helper.
func NewRuntime(value any, options ...FixtureRuntimeOption) (*BrowserScriptRuntime, error) {
	switch script := value.(type) {
	case BrowserScript:
		return NewScriptedFixtureRuntime(script, options...)
	case *BrowserScript:
		if script == nil {
			return nil, newScriptError("script", "is nil")
		}
		return NewScriptedFixtureRuntime(*script, options...)
	default:
		return nil, newScriptError("script", "must be a BrowserScript")
	}
}

// LoadBrowserScriptReader loads a complete script from a reader.
func LoadBrowserScriptReader(reader io.Reader) (BrowserScript, error) {
	if reader == nil {
		return BrowserScript{}, newScriptError("script", "reader is nil")
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return BrowserScript{}, fmt.Errorf("read browser script: %w", err)
	}
	return LoadBrowserScript(data)
}

// IDSourceFunc adapts a function to IDSource.
type IDSourceFunc func(kind string) string

func (f IDSourceFunc) NextID(kind string) string {
	if f == nil {
		return ""
	}
	return f(kind)
}
