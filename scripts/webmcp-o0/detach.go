package main

import (
	"context"
	"embed"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

//go:embed detach-fixture.html
var detachFixtureHTML embed.FS

const detachFixturePath = "detach-fixture.html"

type detachPageState struct {
	URL         string   `json:"url"`
	Ready       bool     `json:"ready"`
	Sentinel    string   `json:"sentinel"`
	VisibleText string   `json:"visibleText"`
	Transitions []string `json:"transitions"`
}

type detachLifecycleReport struct {
	API                 string `json:"api"`
	SessionID           string `json:"sessionID"`
	TargetID            string `json:"targetID"`
	TargetCloseIssued   bool   `json:"targetCloseIssued"`
	BrowserCloseIssued  bool   `json:"browserCloseIssued"`
	ContextCancelMethod string `json:"contextCancelMethod"`
	AllocatorCancel     string `json:"allocatorCancel"`
}

type detachProbeReport struct {
	ObservedAt string                `json:"observedAt"`
	Phase      string                `json:"phase"`
	Endpoint   string                `json:"endpoint"`
	TargetID   string                `json:"targetID"`
	FixtureURL string                `json:"fixtureURL"`
	Before     detachPageState       `json:"before"`
	After      detachPageState       `json:"after"`
	Lifecycle  detachLifecycleReport `json:"lifecycle"`
	Verdict    string                `json:"verdict"`
}

func detachPageExpression() string {
	return `(() => {
  const state = window.__webmcpO0Detach;
  const sentinel = state && state.sentinel !== undefined ? String(state.sentinel) : "";
  const visible = document.querySelector("#sentinel");
  return {
    url: location.href,
    ready: Boolean(state && state.ready),
    sentinel,
    visibleText: visible ? String(visible.textContent || "") : "",
    transitions: state && Array.isArray(state.transitions)
      ? state.transitions.map((value) => String(value))
      : []
  };
})()`
}

func readDetachPageState(ctx context.Context) (detachPageState, error) {
	var state detachPageState
	if err := chromedp.Run(ctx, chromedp.Evaluate(detachPageExpression(), &state)); err != nil {
		return detachPageState{}, fmt.Errorf("evaluate detach fixture state: %w", err)
	}
	return state, nil
}

// detachExternalTarget is deliberately not chromedp.Cancel. In chromedp
// v0.16.0, canceling a normal target context detaches and then closes its
// target. Detach the explicitly supplied external target first, clear the
// target reference so the context cleanup cannot issue Target.closeTarget,
// and only then cancel the client contexts.
func detachExternalTarget(targetContext context.Context, cancelTarget context.CancelFunc) (detachLifecycleReport, error) {
	client := chromedp.FromContext(targetContext)
	if client == nil || client.Browser == nil {
		cancelTarget()
		return detachLifecycleReport{
			API:                 "Target.detachFromTarget",
			TargetCloseIssued:   false,
			BrowserCloseIssued:  false,
			ContextCancelMethod: "cancelTarget after target was not attached",
		}, nil
	}

	lifecycle := detachLifecycleReport{
		API:                 "Target.detachFromTarget",
		TargetCloseIssued:   false,
		BrowserCloseIssued:  false,
		ContextCancelMethod: "cancelTarget after clearing client.Target",
		AllocatorCancel:     "cancelAllocator after target detach",
	}
	if client.Target == nil {
		cancelTarget()
		return lifecycle, nil
	}

	targetClient := client.Target
	lifecycle.SessionID = string(targetClient.SessionID)
	lifecycle.TargetID = string(targetClient.TargetID)
	if targetClient.SessionID == "" {
		client.Target = nil
		cancelTarget()
		return lifecycle, nil
	}

	detachContext, cancelDetach := context.WithTimeout(context.Background(), 5*time.Second)
	err := target.DetachFromTarget().
		WithSessionID(targetClient.SessionID).
		Do(cdp.WithExecutor(detachContext, client.Browser))
	cancelDetach()

	// Whether the detach command succeeded or returned an error, do not let
	// chromedp's target-context cancellation path see this target. Dropping the
	// remote debugging connection is safe for an external target and cannot
	// turn a cleanup path into Target.closeTarget.
	targetClient.SessionID = ""
	targetClient.TargetID = ""
	client.Target = nil
	cancelTarget()
	if err != nil {
		return lifecycle, fmt.Errorf("%s for target %s: %w", lifecycle.API, lifecycle.TargetID, err)
	}
	return lifecycle, nil
}

func detachTransition(phase string) (expected, next string, err error) {
	switch phase {
	case "initial":
		return "initial", "attached", nil
	case "reattach":
		return "attached", "reattached", nil
	default:
		return "", "", fmt.Errorf("phase must be initial or reattach, got %q", phase)
	}
}

func runDetachProbe(endpoint, targetID, phase string) (report detachProbeReport, err error) {
	if endpoint == "" {
		return detachProbeReport{}, fmt.Errorf("browser websocket endpoint is empty")
	}
	if targetID == "" {
		return detachProbeReport{}, fmt.Errorf("external target ID is empty")
	}
	expected, next, err := detachTransition(phase)
	if err != nil {
		return detachProbeReport{}, err
	}

	rootContext, cancelRoot := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	targetContext, cancelTarget := chromedp.NewContext(allocatorContext, chromedp.WithTargetID(target.ID(targetID)))
	defer func() {
		lifecycle, cleanupErr := detachExternalTarget(targetContext, cancelTarget)
		cancelAllocator()
		if report.Lifecycle.API == "" {
			report.Lifecycle = lifecycle
		}
		if err == nil && cleanupErr != nil {
			err = cleanupErr
		}
	}()

	if err := chromedp.Run(targetContext, chromedp.WaitReady("body")); err != nil {
		return detachProbeReport{}, fmt.Errorf("attach explicit target %s: %w", targetID, err)
	}

	before, err := readDetachPageState(targetContext)
	if err != nil {
		return detachProbeReport{}, err
	}
	if !isLoopbackFixtureURL(before.URL) {
		return detachProbeReport{}, fmt.Errorf("attached target URL = %q is not the loopback fixture", before.URL)
	}
	if !before.Ready {
		return detachProbeReport{}, fmt.Errorf("detach fixture was not ready")
	}
	if before.Sentinel != expected {
		return detachProbeReport{}, fmt.Errorf("phase %s sentinel = %q, want %q", phase, before.Sentinel, expected)
	}

	setExpression := `window.__webmcpO0Detach.setSentinel(` + strconv.Quote(next) + `)`
	if err := chromedp.Run(targetContext, chromedp.Evaluate(setExpression, nil)); err != nil {
		return detachProbeReport{}, fmt.Errorf("set %s sentinel: %w", next, err)
	}
	after, err := readDetachPageState(targetContext)
	if err != nil {
		return detachProbeReport{}, err
	}
	if after.Sentinel != next || after.VisibleText != next {
		return detachProbeReport{}, fmt.Errorf("updated sentinel = %q/%q, want %q", after.Sentinel, after.VisibleText, next)
	}
	if after.URL != before.URL {
		return detachProbeReport{}, fmt.Errorf("fixture URL changed from %q to %q", before.URL, after.URL)
	}

	return detachProbeReport{
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
		Phase:      phase,
		Endpoint:   endpoint,
		TargetID:   targetID,
		FixtureURL: before.URL,
		Before:     before,
		After:      after,
		Verdict:    "PASS",
	}, nil
}

func isLoopbackFixtureURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "http" && parsed.Hostname() == "127.0.0.1" && parsed.Path == "/" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func serveDetachFixture() error {
	html, err := detachFixtureHTML.ReadFile(detachFixturePath)
	if err != nil {
		return fmt.Errorf("read embedded detach fixture: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen on loopback: %w", err)
	}
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write(html)
	})
	server := &http.Server{Handler: mux}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()

	fmt.Printf("fixtureURL=http://%s/\n", listener.Addr().String())
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(shutdown)
	select {
	case err := <-serverErrors:
		if err == http.ErrServerClosed {
			return nil
		}
		return fmt.Errorf("serve detach fixture: %w", err)
	case <-shutdown:
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shutdown detach fixture: %w", err)
		}
		return nil
	}
}
