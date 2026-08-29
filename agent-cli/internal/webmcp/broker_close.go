package webmcp

import (
	"fmt"
	"sync"
	"time"
)

func invokeBrokerCloseWithTimeout(timeout time.Duration, phase string, closeFn func() error) error {
	if closeFn == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultBrokerCloseTimeout
	}
	done := make(chan error, 1)
	go func() {
		done <- invokeBrokerClose(closeFn)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("%s: %w after %s", phase, ErrCloseTimeout, timeout)
	}
}

func invokeBrokerClose(closeFn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", ErrClosePanic, recovered)
		}
	}()
	return closeFn()
}

func waitForBrokerWorkersWithTimeout(timeout time.Duration, workers *sync.WaitGroup) error {
	if workers == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultBrokerCloseTimeout
	}
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return fmt.Errorf("broker workers: %w after %s", ErrCloseTimeout, timeout)
	}
}
