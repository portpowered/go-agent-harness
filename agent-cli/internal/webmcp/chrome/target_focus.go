package chrome

import (
	"context"
	"sync"

	"github.com/chromedp/cdproto/emulation"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// Chrome defers metadata loading while an occluded page is hidden. Merely
// activating its tab does not change that. This override lives only on the
// attached CDP session and is released by the bounded operation's caller.
func (s *targetSession) AcquirePageFocus(ctx context.Context) (func(context.Context) error, error) {
	s.focusMu.Lock()
	if s.focusUsers == 0 {
		if err := s.run(ctx, emulation.SetFocusEmulationEnabled(true)); err != nil {
			s.focusMu.Unlock()
			var once sync.Once
			var cleanupErr error
			return func(cleanup context.Context) error {
				once.Do(func() {
					s.focusMu.Lock()
					defer s.focusMu.Unlock()
					// A later successful acquisition owns the override now.
					// Its final release will restore focus; failed acquisition
					// cleanup must not disable that active lease.
					if s.focusUsers == 0 {
						cleanupErr = s.run(cleanup, emulation.SetFocusEmulationEnabled(false))
					}
				})
				return cleanupErr
			}, err
		}
	}
	s.focusUsers++
	s.focusMu.Unlock()
	var once sync.Once
	var releaseErr error
	return func(cleanup context.Context) error {
		once.Do(func() {
			s.focusMu.Lock()
			defer s.focusMu.Unlock()
			s.focusUsers--
			if s.focusUsers == 0 {
				releaseErr = s.run(cleanup, emulation.SetFocusEmulationEnabled(false))
			}
		})
		return releaseErr
	}, nil
}

var _ webmcp.PageFocusLeaser = (*targetSession)(nil)
