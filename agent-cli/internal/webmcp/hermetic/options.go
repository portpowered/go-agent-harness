package hermetic

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

// WithClock injects a deterministic clock into a semantic event Recorder.
func WithClock(clock Clock) RecorderOption {
	return recorderOptionFunc(func(recorder *Recorder) {
		if clock != nil {
			recorder.clock = clock
		}
	})
}

// discardCleanupError runs a release on an abandon or cleanup path whose
// outcome is already decided. The resource is dropped either way, so its
// release error cannot change the result reported to the caller.
func discardCleanupError(release func() error) {
	if err := release(); err != nil {
		return
	}
}
