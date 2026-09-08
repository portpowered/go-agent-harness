package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/composition"
)

// capabilityHandle is the private owner for one resolved capability. Keeping
// lifecycle callbacks here lets a future browser adapter retain its concrete
// broker while exposing only the narrow public handle contract.
type capabilityHandle struct {
	definitions  []messages.ToolDefinition
	initialize   func(context.Context) error
	refresh      func(context.Context) ([]messages.ToolDefinition, error)
	close        func() error
	closeTimeout time.Duration
	closeOnce    sync.Once
	refreshMu    sync.Mutex
	closeErr     error
}

var _ public.CapabilityHandle = (*capabilityHandle)(nil)

func newCapabilityHandle(definitions []messages.ToolDefinition) public.CapabilityHandle {
	return &capabilityHandle{definitions: cloneDefinitions(definitions)}
}

func newBrowserCapabilityHandle(
	staticDefinitions []messages.ToolDefinition,
	staticExecutor messages.ToolExecutor,
	browser public.BrowserSurface,
) public.CapabilityHandle {
	handle := &capabilityHandle{definitions: cloneDefinitions(staticDefinitions), closeTimeout: browser.CloseTimeout}
	handle.initialize = browser.Initialize
	handle.close = browser.Close
	handle.refresh = func(ctx context.Context) ([]messages.ToolDefinition, error) {
		handle.refreshMu.Lock()
		defer handle.refreshMu.Unlock()
		browserDefinitions := cloneDefinitions(browser.Definitions)
		if browser.RefreshDefinitions != nil {
			refreshed, err := browser.RefreshDefinitions(ctx)
			if err != nil {
				return nil, err
			}
			browserDefinitions = refreshed
		}
		surface, err := composition.ComposeToolSurface(
			staticExecutor,
			staticDefinitions,
			browser.Executor,
			browserDefinitions,
		)
		if err != nil {
			return nil, err
		}
		handle.definitions = cloneDefinitions(surface.Definitions)
		return cloneDefinitions(surface.Definitions), nil
	}
	return handle
}

func (h *capabilityHandle) Initialize(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("capability initialization context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.initialize == nil {
		return nil
	}
	return h.initialize(ctx)
}

func (h *capabilityHandle) RefreshDefinitions(ctx context.Context) ([]messages.ToolDefinition, error) {
	if h == nil {
		return nil, nil
	}
	if ctx == nil {
		return nil, errors.New("capability refresh context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h.refresh != nil {
		return h.refresh(ctx)
	}
	return cloneDefinitions(h.definitions), nil
}

func (h *capabilityHandle) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		if h.close != nil {
			h.closeErr = closeCapabilityWithTimeout(h.close, h.closeTimeout)
		}
	})
	return h.closeErr
}

func closeCapabilityWithTimeout(closeFn func() error, timeout time.Duration) error {
	if closeFn == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = public.DefaultCapabilityCloseTimeout
	}
	done := make(chan error, 1)
	go func() {
		done <- invokeCapabilityClose(closeFn)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("%w after %s", public.ErrCapabilityCloseTimeout, timeout)
	}
}

func invokeCapabilityClose(closeFn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", public.ErrCapabilityClosePanic, recovered)
		}
	}()
	return closeFn()
}

func cloneDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	return append([]messages.ToolDefinition(nil), definitions...)
}
