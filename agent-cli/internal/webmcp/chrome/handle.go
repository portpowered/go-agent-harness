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
	// browserExecutor is normally the same value as browser. Keeping the
	// executor as a private seam lets teardown and browser-level operations be
	// verified without constructing a websocket-backed chromedp.Browser.
	browserExecutor cdp.Executor
	httpClient      *http.Client
	commandTimeout  time.Duration
	eventBuffer     int
	wireTrace       webmcp.WireTraceSink
	sessions        map[*targetSession]struct{}
	targetOps       targetContextOps

	closed    bool
	closeErr  error
	done      chan struct{}
	closeOnce sync.Once

	disconnected   bool
	disconnectDone chan struct{}
}

type targetContextOps struct {
	newContext    func(context.Context, target.ID) (context.Context, context.CancelFunc)
	listen        func(context.Context, func(any))
	listenBrowser func(context.Context, func(any))
	run           func(context.Context, ...chromedp.Action) error
	target        func(context.Context) *chromedp.Target
}

func (h *handle) resolvedTargetContextOps() targetContextOps {
	h.mu.Lock()
	ops := h.targetOps
	parent := h.browserContext
	customContext := ops.newContext != nil
	h.mu.Unlock()

	if ops.newContext == nil {
		ops.newContext = func(parent context.Context, targetID target.ID) (context.Context, context.CancelFunc) {
			return chromedp.NewContext(parent, chromedp.WithTargetID(targetID))
		}
	}
	if ops.listen == nil {
		ops.listen = chromedp.ListenTarget
	}
	if ops.listenBrowser == nil {
		if customContext {
			ops.listenBrowser = func(context.Context, func(any)) {}
		} else {
			ops.listenBrowser = chromedp.ListenBrowser
		}
	}
	if ops.run == nil {
		ops.run = chromedp.Run
	}
	if ops.target == nil {
		ops.target = func(ctx context.Context) *chromedp.Target {
			data := chromedp.FromContext(ctx)
			if data == nil {
				return nil
			}
			return data.Target
		}
	}
	if parent == nil && !customContext {
		// Open always supplies a chromedp context. This branch is useful for a
		// clearer error in tests or callers that construct a handle directly.
		return targetContextOps{}
	}
	return ops
}

func (h *handle) executor() cdp.Executor {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.disconnected {
		return nil
	}
	if h.browserExecutor != nil {
		return h.browserExecutor
	}
	return h.browser
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
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, webmcp.ErrClosed
	}
	if h.disconnected {
		err := browserDisconnectedError(webmcp.PageContext{Key: webmcp.PageKey{BrowserID: h.candidate.ID}}, "list_targets", nil)
		h.mu.Unlock()
		return nil, err
	}
	h.mu.Unlock()
	commandContext, releaseContext := h.operationContext(ctx)
	defer releaseContext()

	if targets, err := h.listTargetsHTTP(commandContext); err == nil {
		h.mu.Lock()
		closed := h.closed
		disconnected := h.disconnected
		h.mu.Unlock()
		if closed {
			return nil, webmcp.ErrClosed
		}
		if disconnected {
			return nil, h.disconnectError("", "list_targets", nil)
		}
		return targets, nil
	} else if !errors.Is(err, errHTTPUnavailable) {
		if h.isDisconnected() {
			return nil, h.disconnectError("", "list_targets", err)
		}
		return nil, err
	}
	if h.isDisconnected() {
		return nil, h.disconnectError("", "list_targets", nil)
	}

	h.mu.Lock()
	candidate := h.candidate
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil, webmcp.ErrClosed
	}
	executor := h.executor()
	if executor == nil {
		if h.isDisconnected() {
			return nil, h.disconnectError("", "list_targets", nil)
		}
		return nil, classifiedHandleError(candidate, webmcp.ErrorBrowserDisconnected, "list_targets", errors.New("browser connection is unavailable"))
	}

	infos, err := target.GetTargets().Do(cdp.WithExecutor(commandContext, executor))
	if err != nil {
		if h.isDisconnected() {
			return nil, h.disconnectError("", "list_targets", err)
		}
		return nil, classifiedHandleError(candidate, webmcp.ErrorBrowserDisconnected, "list_targets", err)
	}
	if h.isDisconnected() {
		return nil, h.disconnectError("", "list_targets", nil)
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
	closed := h.closed
	disconnected := h.disconnected
	candidate := h.candidate
	h.mu.Unlock()
	if closed {
		return webmcp.ErrClosed
	}
	if disconnected {
		return h.disconnectError(targetID, "activate", nil)
	}
	executor := h.executor()
	if executor == nil {
		if h.isDisconnected() {
			return h.disconnectError(targetID, "activate", nil)
		}
		return classifiedTargetError(candidate, targetID, "activate", errors.New("browser connection is unavailable"))
	}
	commandContext, releaseContext := h.operationContext(ctx)
	defer releaseContext()
	if err := target.ActivateTarget(target.ID(targetID)).Do(cdp.WithExecutor(commandContext, executor)); err != nil {
		if h.isDisconnected() {
			return h.disconnectError(targetID, "activate", err)
		}
		return classifiedTargetError(candidate, targetID, "activate", err)
	}
	if h.isDisconnected() {
		return h.disconnectError(targetID, "activate", nil)
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
		if errors.Is(err, webmcp.ErrClosed) {
			return nil, err
		}
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

	h.mu.Lock()
	closed := h.closed
	disconnected := h.disconnected
	parent := h.browserContext
	h.mu.Unlock()
	if closed {
		return nil, webmcp.ErrClosed
	}
	if disconnected {
		return nil, browserDisconnectedError(webmcp.PageContext{Key: webmcp.PageKey{BrowserID: h.candidate.ID, TargetID: targetID}}, "attach", nil)
	}
	ops := h.resolvedTargetContextOps()
	if ops.newContext == nil || parent == nil && !hasCustomTargetContext(h) {
		return nil, classifiedTargetError(h.candidate, targetID, "attach", errors.New("browser context is unavailable"))
	}
	targetContext, cancelTarget := ops.newContext(parent, target.ID(targetID))
	if targetContext == nil || cancelTarget == nil {
		if cancelTarget != nil {
			cancelTarget()
		}
		return nil, classifiedTargetError(h.candidate, targetID, "attach", errors.New("target context is unavailable"))
	}
	session := newTargetSession(h, targetContext, cancelTarget, selected, ownership)
	session.runAction = ops.run
	ops.listen(targetContext, session.enqueueProtocolEvent)
	ops.listenBrowser(targetContext, session.enqueueBrowserEvent)
	// Both target and browser lifecycle listeners must be installed before the
	// first target command starts the chromedp event reader. Direct cancellation
	// uses this readiness bit in its sanitized wire trace.
	session.markListenerReady()

	// chromedp starts the target event reader with the context supplied to its
	// first Run call. Keep that reader alive for the target session, while still
	// binding it to the handle's disconnect signal. Do not call the returned
	// release function here: doing so would cancel the reader immediately after
	// attach and make every later target command time out. The target context
	// and handle lifecycle cancel the bound context after attach.
	attachContext, _ := h.bindDisconnect(targetContext)
	err = ops.run(attachContext, chromedp.ActionFunc(func(context.Context) error { return nil }))
	protocolTarget := ops.target(targetContext)
	session.setProtocolTarget(protocolTarget)
	if err != nil {
		cleanupErr := session.abortOpen()
		if cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
		if h.isDisconnected() {
			return nil, h.disconnectError(targetID, "attach", err)
		}
		return nil, classifiedTargetError(h.candidate, targetID, "attach", err)
	}

	if protocolTarget == nil {
		cleanupErr := session.abortOpen()
		attachErr := errors.New("target context did not attach")
		if cleanupErr != nil {
			attachErr = errors.Join(attachErr, cleanupErr)
		}
		if h.isDisconnected() {
			return nil, h.disconnectError(targetID, "attach", attachErr)
		}
		return nil, classifiedTargetError(h.candidate, targetID, "attach", attachErr)
	}
	session.publishAttached()

	h.mu.Lock()
	closed = h.closed
	disconnected = h.disconnected
	if closed || disconnected {
		h.mu.Unlock()
		if disconnected {
			session.transportLost()
			return nil, h.disconnectError(targetID, "attach", nil)
		}
		_ = session.Close()
		return nil, webmcp.ErrClosed
	}
	if h.sessions == nil {
		h.sessions = make(map[*targetSession]struct{})
	}
	h.sessions[session] = struct{}{}
	h.mu.Unlock()
	return session, nil
}

func hasCustomTargetContext(h *handle) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.targetOps.newContext != nil
}

func (h *handle) timeout() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.commandTimeout > 0 {
		return h.commandTimeout
	}
	return defaultCommandTimeout
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
		if h.done != nil {
			close(h.done)
		}
		h.mu.Unlock()
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closeErr
}

func (h *handle) isDisconnected() bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.disconnected
}

func (h *handle) disconnectError(targetID webmcp.TargetID, phase string, cause error) error {
	if h == nil {
		return webmcp.NewClassifiedError(webmcp.ErrorBrowserDisconnected, webmcp.DefaultErrorMessage(webmcp.ErrorBrowserDisconnected), map[string]any{
			"phase":              phase,
			"reconnect_required": true,
		})
	}
	h.mu.Lock()
	page := webmcp.PageContext{Key: webmcp.PageKey{BrowserID: h.candidate.ID, TargetID: targetID}}
	h.mu.Unlock()
	return browserDisconnectedError(page, phase, cause)
}

func (h *handle) disconnectSignalLocked() chan struct{} {
	if h.disconnectDone == nil {
		h.disconnectDone = make(chan struct{})
		if h.disconnected {
			close(h.disconnectDone)
		}
	}
	return h.disconnectDone
}

// bindDisconnect makes a long-lived target operation observe browser loss
// without tying its lifetime to a short command timeout.
func (h *handle) bindDisconnect(ctx context.Context) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	if h == nil {
		return ctx, func() {}
	}
	h.mu.Lock()
	disconnectDone := h.disconnectSignalLocked()
	done := h.done
	disconnected := h.disconnected
	h.mu.Unlock()
	bound, cancel := context.WithCancel(ctx)
	if disconnected {
		cancel()
		return bound, cancel
	}
	stop := make(chan struct{})
	go func() {
		select {
		case <-disconnectDone:
			cancel()
		case <-done:
			cancel()
		case <-stop:
		case <-bound.Done():
		}
	}()
	return bound, func() {
		close(stop)
		cancel()
	}
}

// operationContext applies the configured command bound and the browser
// disconnect signal to one handle-level operation.
func (h *handle) operationContext(ctx context.Context) (context.Context, func()) {
	bound, release := h.bindDisconnect(ctx)
	timeout := h.timeout()
	if timeout <= 0 {
		return bound, release
	}
	commandContext, cancel := context.WithTimeout(bound, timeout)
	return commandContext, func() {
		cancel()
		release()
	}
}

func (h *handle) unregister(session *targetSession) {
	h.mu.Lock()
	delete(h.sessions, session)
	h.mu.Unlock()
}

func (h *handle) watchBrowserConnection() {
	if h == nil {
		return
	}
	h.mu.Lock()
	browser := h.browser
	done := h.done
	h.mu.Unlock()
	if browser == nil {
		return
	}
	select {
	case <-done:
		return
	case <-browser.LostConnection:
		h.markTransportLost()
	}
}

func (h *handle) markTransportLost() {
	if h == nil {
		return
	}
	h.mu.Lock()
	disconnectDone := h.disconnectSignalLocked()
	if !h.disconnected {
		h.disconnected = true
		close(disconnectDone)
	}
	sessions := make([]*targetSession, 0, len(h.sessions))
	for session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.mu.Unlock()
	for _, session := range sessions {
		session.transportLost()
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
	client := h.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
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
	if endpoint == "" {
		endpoint = strings.TrimSpace(candidate.HTTPURL)
	}
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
