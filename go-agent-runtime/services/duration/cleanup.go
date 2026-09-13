package duration

import (
	"context"
	"errors"
)

// CleanupHooks lets a host runner stop its owned resources in the order used
// by the duration service without moving provider or device types into this
// package.
type CleanupHooks struct {
	StopTimer     func()
	StopPublisher func()
	CloseSession  func() error
	RTCError      <-chan error
	CloseBare     func() error
	CloseBinding  func() error
	Cancel        context.CancelFunc
	LoopDone      <-chan error
	StopLiveness  func()
}

func (h CleanupHooks) StopResources(ctx context.Context) error {
	h.stopImmediateResources()
	errs := h.closeResources()
	h.cancelLoop()
	errs = append(errs, h.waitForLoop(ctx)...)
	return errors.Join(errs...)
}

func (h CleanupHooks) stopImmediateResources() {
	if h.StopTimer != nil {
		h.StopTimer()
	}
	if h.StopPublisher != nil {
		h.StopPublisher()
	}
}

func (h CleanupHooks) closeResources() []error {
	var errs []error
	if h.CloseSession != nil {
		errs = append(errs, h.CloseSession())
	}
	if h.RTCError != nil {
		select {
		case err := <-h.RTCError:
			if err != nil && !errors.Is(err, context.Canceled) {
				errs = append(errs, err)
			}
		default:
		}
	}
	if h.CloseBare != nil {
		errs = append(errs, h.CloseBare())
	}
	if h.CloseBinding != nil {
		errs = append(errs, h.CloseBinding())
	}
	return errs
}

func (h CleanupHooks) cancelLoop() {
	if h.Cancel != nil {
		h.Cancel()
	}
}

func (h CleanupHooks) waitForLoop(ctx context.Context) []error {
	if h.LoopDone == nil {
		h.stopLiveness()
		return nil
	}
	var errs []error
	select {
	case err := <-h.LoopDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			errs = append(errs, err)
		}
	case <-ctx.Done():
		errs = append(errs, ctx.Err())
	}
	h.stopLiveness()
	return errs
}

func (h CleanupHooks) stopLiveness() {
	if h.StopLiveness != nil {
		h.StopLiveness()
	}
}
