package production

import (
	"context"
	"errors"
	"net/http"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
)

func (p *composition) managedEnabled() bool {
	return p != nil && p.browser.UsesManagedBrowser() && p.browser.BrowserBackendEnabled()
}

// managedClaim is one locked observation of the managed-browser state. At
// most one of its outcomes applies: a settled browser/error, a pending launch
// to wait for, or a launch this caller now owns.
type managedClaim struct {
	browser *chrome.ManagedBrowser
	err     error
	wait    <-chan struct{}
	launch  *managedLaunch
}

type managedLaunch struct {
	done       chan struct{}
	manager    *chrome.ManagedBrowserManager
	configDir  string
	startupURL string
	headless   bool
	httpClient *http.Client
}

// ensureManagedBrowser is the one lazy launch boundary for the production
// composition. Concurrent commands wait for the first acquisition and then
// share the exact persisted browser instead of starting overlapping Chrome
// processes.
func (p *composition) ensureManagedBrowser(ctx context.Context) (*chrome.ManagedBrowser, error) { //nolint:contextcheck // A nil context from legacy callers falls back to Background.
	if !p.managedEnabled() {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		claim := p.claimManagedBrowser()
		if claim.wait != nil {
			select {
			case <-claim.wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if claim.launch == nil {
			return claim.browser, claim.err
		}
		return p.launchManagedBrowser(ctx, claim.launch)
	}
}

func (p *composition) claimManagedBrowser() managedClaim {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.closed:
		return managedClaim{err: webmcp.ErrClosed}
	case p.managedBrowser != nil:
		return managedClaim{browser: p.managedBrowser}
	case p.managedErr != nil:
		return managedClaim{err: p.managedErr}
	case p.managedStarting:
		return managedClaim{wait: p.managedStart}
	}
	p.managedStarting = true
	p.managedStart = make(chan struct{})
	return managedClaim{launch: &managedLaunch{
		done:       p.managedStart,
		manager:    p.managedManager,
		configDir:  p.configDir,
		startupURL: p.browser.ManagedStartupURL(),
		headless:   p.browser.Managed.Headless,
		httpClient: p.httpClient,
	}}
}

func (p *composition) launchManagedBrowser(ctx context.Context, launch *managedLaunch) (*chrome.ManagedBrowser, error) {
	var browser *chrome.ManagedBrowser
	var err error
	if launch.manager == nil {
		err = errors.New("managed browser lifecycle manager is unavailable")
	} else {
		browser, err = launch.manager.Acquire(ctx, chrome.ManagedBrowserLaunchOptions{
			ConfigDir:  launch.configDir,
			StartupURL: launch.startupURL,
			Headless:   launch.headless,
			HTTPClient: launch.httpClient,
		})
	}
	p.mu.Lock()
	p.managedStarting = false
	if err != nil {
		p.managedErr = err
	} else {
		p.managedBrowser = browser
	}
	close(launch.done)
	p.mu.Unlock()
	return browser, err
}

// Close releases the selected target through discovery and, only when the
// managed close-on-exit policy is enabled, closes the exact managed browser.
// The default policy leaves process, profile, and state warm for the next
// session. External browser configurations never enter this branch.
func (p *composition) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		start := p.managedStart
		p.mu.Unlock()
		if start != nil {
			<-start
		}
		p.mu.Lock()
		service := p.discovery
		browser := p.managedBrowser
		closeOnExit := p.browser.Managed.CloseOnExit
		p.mu.Unlock()
		var serviceErr error
		if closer, ok := service.(interface{ Close() error }); ok {
			serviceErr = closer.Close()
		}
		var browserErr error
		if closeOnExit && browser != nil {
			browserErr = browser.Close()
		}
		p.closeErr = errors.Join(serviceErr, browserErr)
	})
	return p.closeErr
}
