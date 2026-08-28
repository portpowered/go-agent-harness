package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

type handle struct {
	mu sync.Mutex

	candidate       webmcp.BrowserCandidate
	browserContext  context.Context
	cancelBrowser   context.CancelFunc
	cancelAllocator context.CancelFunc
	browser         *chromedp.Browser
	httpClient      *http.Client
	commandTimeout  time.Duration
	eventBuffer     int
	sessions        map[*targetSession]struct{}

	closed    bool
	closeErr  error
	done      chan struct{}
	closeOnce sync.Once
}

func (h *handle) Candidate() webmcp.BrowserCandidate {
	h.mu.Lock()
	defer h.mu.Unlock()
	return cloneCandidate(h.candidate)
}

func (h *handle) ListTargets(ctx context.Context) ([]webmcp.Target, error) {
	if err := contextError(ctx); err != nil {
		return nil, classifiedHandleError(h.candidate, webmcp.ErrorBrowserDisconnected, "list_targets", err)
	}

	if targets, err := h.listTargetsHTTP(ctx); err == nil {
		return targets, nil
	} else if !errors.Is(err, errHTTPUnavailable) {
		return nil, err
	}

	h.mu.Lock()
	browser := h.browser
	candidate := h.candidate
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil, webmcp.ErrClosed
	}
	if browser == nil {
		return nil, classifiedHandleError(candidate, webmcp.ErrorBrowserDisconnected, "list_targets", errors.New("browser connection is unavailable"))
	}

	infos, err := target.GetTargets().Do(cdp.WithExecutor(ctx, browser))
	if err != nil {
		return nil, classifiedHandleError(candidate, webmcp.ErrorBrowserDisconnected, "list_targets", err)
	}
	result := make([]webmcp.Target, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			continue
		}
		result = append(result, normalizeTarget(candidate, targetInfo{
			ID:       string(info.TargetID),
			Type:     info.Type,
			Title:    info.Title,
			URL:      info.URL,
			Attached: info.Attached,
			WSURL:    targetWebSocketURL(candidate, string(info.TargetID)),
		}))
	}
	return result, nil
}

func (h *handle) Activate(ctx context.Context, targetID webmcp.TargetID) error {
	if err := contextError(ctx); err != nil {
		return classifiedTargetError(h.candidate, targetID, "activate", err)
	}
	if targetID == "" {
		return classifiedTargetError(h.candidate, targetID, "activate", errors.New("target ID is empty"))
	}
	h.mu.Lock()
	browser := h.browser
	closed := h.closed
	candidate := h.candidate
	h.mu.Unlock()
	if closed {
		return webmcp.ErrClosed
	}
	if browser == nil {
		return classifiedTargetError(candidate, targetID, "activate", errors.New("browser connection is unavailable"))
	}
	if err := target.ActivateTarget(target.ID(targetID)).Do(cdp.WithExecutor(ctx, browser)); err != nil {
		return classifiedTargetError(candidate, targetID, "activate", err)
	}
	return nil
}

func (h *handle) Attach(ctx context.Context, targetID webmcp.TargetID, ownership webmcp.TargetOwnership) (webmcp.TargetSession, error) {
	if err := contextError(ctx); err != nil {
		return nil, classifiedTargetError(h.candidate, targetID, "attach", err)
	}
	if targetID == "" {
		return nil, classifiedTargetError(h.candidate, targetID, "attach", errors.New("target ID is empty"))
	}
	if ownership != webmcp.TargetOwnershipExternal && ownership != webmcp.TargetOwnershipHarnessOwned {
		return nil, classifiedTargetError(h.candidate, targetID, "attach", errors.New("target ownership is invalid"))
	}

	targets, err := h.ListTargets(ctx)
	if err != nil {
		return nil, classifiedTargetError(h.candidate, targetID, "lookup", err)
	}
	var selected webmcp.Target
	found := false
	for _, candidateTarget := range targets {
		if candidateTarget.ID == targetID {
			selected = candidateTarget
			found = true
			break
		}
	}
	if !found {
		return nil, classifiedTargetError(h.candidate, targetID, "lookup", webmcp.ErrTargetNotFound)
	}

	targetContext, cancelTarget := chromedp.NewContext(h.browserContext, chromedp.WithTargetID(target.ID(targetID)))
	session := newTargetSession(h, targetContext, cancelTarget, selected, ownership)
	chromedp.ListenTarget(targetContext, session.enqueueProtocolEvent)

	attachCtx, cancelAttach := context.WithTimeout(targetContext, h.commandTimeout)
	err = chromedp.Run(attachCtx, chromedp.ActionFunc(func(context.Context) error { return nil }))
	cancelAttach()
	if err != nil {
		session.abortOpen()
		return nil, classifiedTargetError(h.candidate, targetID, "attach", err)
	}

	data := chromedp.FromContext(targetContext)
	if data == nil || data.Target == nil {
		session.abortOpen()
		return nil, classifiedTargetError(h.candidate, targetID, "attach", errors.New("target context did not attach"))
	}
	session.setProtocolTarget(data.Target)
	session.publishAttached()

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = session.Close()
		return nil, webmcp.ErrClosed
	}
	h.sessions[session] = struct{}{}
	h.mu.Unlock()
	return session, nil
}

func (h *handle) Close() error {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		sessions := make([]*targetSession, 0, len(h.sessions))
		for session := range h.sessions {
			sessions = append(sessions, session)
		}
		h.mu.Unlock()

		var joined error
		for _, session := range sessions {
			joined = errors.Join(joined, session.Close())
		}
		if h.cancelBrowser != nil {
			h.cancelBrowser()
		}
		if h.cancelAllocator != nil {
			h.cancelAllocator()
		}

		h.mu.Lock()
		h.closeErr = joined
		close(h.done)
		h.mu.Unlock()
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closeErr
}

func (h *handle) unregister(session *targetSession) {
	h.mu.Lock()
	delete(h.sessions, session)
	h.mu.Unlock()
}

func (h *handle) watchBrowserConnection() {
	select {
	case <-h.done:
		return
	case <-h.browser.LostConnection:
		h.mu.Lock()
		sessions := make([]*targetSession, 0, len(h.sessions))
		for session := range h.sessions {
			sessions = append(sessions, session)
		}
		h.mu.Unlock()
		for _, session := range sessions {
			session.transportLost()
		}
	}
}

type targetInfo struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	WSURL    string `json:"webSocketDebuggerUrl"`
	Attached bool   `json:"attached"`
}

var errHTTPUnavailable = errors.New("chrome adapter: http target listing unavailable")

func (h *handle) listTargetsHTTP(ctx context.Context) ([]webmcp.Target, error) {
	base, err := httpEndpoint(h.candidate)
	if err != nil {
		return nil, errHTTPUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/json/list", nil)
	if err != nil {
		return nil, errHTTPUnavailable
	}
	response, err := h.httpClient.Do(request)
	if err != nil {
		return nil, errHTTPUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, errHTTPUnavailable
	}
	var infos []targetInfo
	if err := json.NewDecoder(response.Body).Decode(&infos); err != nil {
		return nil, classifiedHandleError(h.candidate, webmcp.ErrorBrowserProtocol, "list_targets", err)
	}
	result := make([]webmcp.Target, 0, len(infos))
	for _, info := range infos {
		result = append(result, normalizeTarget(h.candidate, info))
	}
	return result, nil
}

func normalizeTarget(candidate webmcp.BrowserCandidate, info targetInfo) webmcp.Target {
	origin := targetOrigin(info.URL)
	eligible := info.Type == "page" && info.ID != "" && info.WSURL != "" && !isInternalURL(info.URL)
	reason := ""
	if info.Type != "page" {
		reason = "target is not a page"
	} else if info.ID == "" {
		reason = "target ID is empty"
	} else if info.WSURL == "" {
		reason = "target websocket is unavailable"
	} else if isInternalURL(info.URL) {
		reason = "internal browser page"
	}
	return webmcp.Target{
		BrowserID:         candidate.ID,
		ID:                webmcp.TargetID(info.ID),
		Type:              info.Type,
		Title:             info.Title,
		URL:               info.URL,
		Origin:            origin,
		WebSocketURL:      info.WSURL,
		Attached:          info.Attached,
		Eligible:          eligible,
		EligibilityReason: reason,
	}
}

func httpEndpoint(candidate webmcp.BrowserCandidate) (string, error) {
	endpoint := strings.TrimSpace(candidate.HTTPURL)
	if endpoint == "" {
		endpoint = strings.TrimSpace(candidate.BrowserWSURL)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return "", errors.New("browser http endpoint is invalid")
	}
	scheme := parsed.Scheme
	if scheme == "ws" {
		scheme = "http"
	} else if scheme == "wss" {
		scheme = "https"
	}
	if scheme != "http" && scheme != "https" {
		return "", errors.New("browser http endpoint scheme is invalid")
	}
	return (&url.URL{Scheme: scheme, Host: parsed.Host}).String(), nil
}

func targetWebSocketURL(candidate webmcp.BrowserCandidate, targetID string) string {
	endpoint := strings.TrimSpace(candidate.BrowserWSURL)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || targetID == "" {
		return ""
	}
	scheme := parsed.Scheme
	if scheme == "http" {
		scheme = "ws"
	} else if scheme == "https" {
		scheme = "wss"
	}
	if scheme != "ws" && scheme != "wss" {
		return ""
	}
	return (&url.URL{Scheme: scheme, Host: parsed.Host, Path: "/devtools/page/" + url.PathEscape(targetID)}).String()
}

func targetOrigin(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func isInternalURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return true
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return false
	default:
		return true
	}
}

var _ webmcp.BrowserHandle = (*handle)(nil)
