package duration

import "context"

// ResultInputs describes the host signals that can finish a duration runner
// before its loop result is published.
type ResultInputs struct {
	Context      context.Context
	Loop         <-chan error
	Publisher    <-chan error
	RTC          <-chan error
	Async        <-chan error
	SessionDone  <-chan struct{}
	SessionError func() error
	Done         <-chan struct{}
	DoneError    func() error
}

func (input ResultInputs) FirstResult(ctx context.Context) <-chan error {
	resultContext := selectResultContext(ctx, input.Context)
	result := make(chan error, 1)
	go func() {
		select {
		case err := <-input.Loop:
			result <- err
		case err, ok := <-input.Publisher:
			result <- input.publisherResult(resultContext, err, ok)
		case err, ok := <-input.RTC:
			result <- input.rtcResult(err, ok)
		case err := <-input.Async:
			result <- err
		case <-input.SessionDone:
			if input.SessionError != nil {
				result <- input.SessionError()
			} else {
				result <- nil
			}
		case <-input.Done:
			if input.DoneError != nil {
				result <- input.DoneError()
			} else {
				result <- nil
			}
		}
	}()
	return result
}

func selectResultContext(ctx, fallback context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	if fallback != nil {
		return fallback
	}
	return defaultResultContext()
}

func defaultResultContext() context.Context { return context.Background() }

func (input ResultInputs) publisherResult(ctx context.Context, err error, ok bool) error {
	if ok && err != nil {
		return err
	}
	select {
	case err := <-input.Loop:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (input ResultInputs) rtcResult(err error, ok bool) error {
	if ok && err != nil {
		return err
	}
	return nil
}
