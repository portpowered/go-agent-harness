package testkit

import "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"

// RecorderOption configures a semantic event Recorder.
type RecorderOption interface {
	applyRecorder(*Recorder)
}

type recorderOptionFunc func(*Recorder)

func (f recorderOptionFunc) applyRecorder(recorder *Recorder) {
	if f != nil {
		f(recorder)
	}
}

// ScriptedTargetSessionOption configures a low-level fake target session.
type ScriptedTargetSessionOption interface {
	applySession(*ScriptedTargetSessionOptions)
}

type scriptedTargetSessionOptionFunc func(*ScriptedTargetSessionOptions)

func (f scriptedTargetSessionOptionFunc) applySession(options *ScriptedTargetSessionOptions) {
	if f != nil {
		f(options)
	}
}

// clockOption can configure either of the package's deterministic clock
// seams. A shared option keeps WithClock useful for both recorder and browser
// runtime callers after the low-level runtime was added to the same package.
type clockOption struct {
	recorder Clock
	session  webmcp.Clock
}

func (o clockOption) applyRecorder(recorder *Recorder) {
	if o.recorder != nil {
		recorder.clock = o.recorder
	}
}

func (o clockOption) applySession(options *ScriptedTargetSessionOptions) {
	if o.session != nil {
		options.Clock = o.session
	}
}

// WithClock injects a deterministic clock into whichever testkit seam
// receives the returned option. Values implementing both Clock interfaces,
// such as FakeClock, configure both seams.
func WithClock(value any) clockOption {
	option := clockOption{}
	if clock, ok := value.(Clock); ok {
		option.recorder = clock
	}
	if clock, ok := value.(webmcp.Clock); ok {
		option.session = clock
	}
	return option
}

// WithSessionClock is the explicit low-level runtime spelling of WithClock.
func WithSessionClock(clock webmcp.Clock) ScriptedTargetSessionOption {
	return WithClock(clock)
}
