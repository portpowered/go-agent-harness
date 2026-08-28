package main

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

const (
	hermeticInitialValue = "initial"
	hermeticFinalValue   = "transitioned"
)

//go:embed hermetic-fixture.html
var hermeticFixtureHTML embed.FS

type hermeticStateReport struct {
	URL         string   `json:"url"`
	Ready       bool     `json:"ready"`
	Value       string   `json:"value"`
	VisibleText string   `json:"visibleText"`
	Transitions []string `json:"transitions"`
}

type hermeticActionResult struct {
	Value       string `json:"value"`
	VisibleText string `json:"visibleText"`
}

type hermeticActionReport struct {
	Attempted bool                 `json:"attempted"`
	Outcome   string               `json:"outcome"`
	Returned  hermeticActionResult `json:"returned,omitempty"`
	Error     string               `json:"error,omitempty"`
}

type hermeticProbeReport struct {
	ObservedAt    string               `json:"observedAt"`
	Endpoint      string               `json:"endpoint"`
	FixtureOrigin string               `json:"fixtureOrigin"`
	FixtureURL    string               `json:"fixtureURL"`
	ReadySignal   string               `json:"readySignal"`
	Initial       hermeticStateReport  `json:"initial"`
	Action        hermeticActionReport `json:"action"`
	Final         hermeticStateReport  `json:"final"`
	ExpectedFinal string               `json:"expectedFinal"`
	StateMatch    bool                 `json:"stateMatch"`
	ControlPath   string               `json:"controlPath"`
	WebMCPPath    string               `json:"webmcpPath"`
	Verdict       string               `json:"verdict"`
}

func startHermeticFixture() *httptest.Server {
	contents, err := hermeticFixtureHTML.ReadFile("hermetic-fixture.html")
	if err != nil {
		panic(fmt.Sprintf("read embedded hermetic fixture: %v", err))
	}
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write(contents)
	}))
}

func hermeticStateExpression() string {
	return `(() => {
  const state = window.__webmcpO0Hermetic;
  const visible = document.querySelector("#state");
  return {
    url: location.href,
    ready: Boolean(state && state.ready),
    value: state && state.value !== undefined ? String(state.value) : "",
    visibleText: visible ? String(visible.textContent || "") : "",
    transitions: state && Array.isArray(state.transitions)
      ? state.transitions.map((value) => String(value))
      : []
  };
})()`
}

func hermeticTransitionExpression() string {
	return `(() => {
  const state = window.__webmcpO0Hermetic;
  if (!state || typeof state.transition !== "function") {
    throw new Error("hermetic fixture transition is unavailable");
  }
  const value = state.transition();
  const visible = document.querySelector("#state");
  return {
    value: String(value),
    visibleText: visible ? String(visible.textContent || "") : ""
  };
})()`
}

func readHermeticState(ctx context.Context) (hermeticStateReport, error) {
	var state hermeticStateReport
	if err := chromedp.Run(ctx, chromedp.Evaluate(hermeticStateExpression(), &state)); err != nil {
		return hermeticStateReport{}, fmt.Errorf("evaluate hermetic fixture state: %w", err)
	}
	return state, nil
}

func runHermeticProbe(endpoint string) (report hermeticProbeReport, err error) {
	if endpoint == "" {
		return hermeticProbeReport{}, fmt.Errorf("browser websocket endpoint is empty")
	}

	fixture := startHermeticFixture()
	defer fixture.Close()
	fixtureURL := fixture.URL + "/"
	if !isLoopbackFixtureURL(fixtureURL) {
		return hermeticProbeReport{}, fmt.Errorf("hermetic fixture URL is not loopback: %s", fixtureURL)
	}
	parsedFixtureURL, parseErr := url.Parse(fixtureURL)
	if parseErr != nil {
		return hermeticProbeReport{}, fmt.Errorf("parse hermetic fixture URL: %w", parseErr)
	}

	rootContext, cancelRoot := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	defer cancelAllocator()
	targetContext, cancelTarget := chromedp.NewContext(allocatorContext)
	defer func() {
		// This temporary target belongs to this probe. The remote allocator and
		// browser process remain owned by the launcher.
		_ = chromedp.Cancel(targetContext)
		cancelTarget()
	}()

	if err := chromedp.Run(targetContext, chromedp.Navigate(fixtureURL), chromedp.WaitReady("#ready")); err != nil {
		return hermeticProbeReport{}, fmt.Errorf("navigate/readiness for hermetic fixture %s: %w", fixtureURL, err)
	}
	var readyText string
	if err := chromedp.Run(targetContext, chromedp.Text("#ready", &readyText)); err != nil {
		return hermeticProbeReport{}, fmt.Errorf("read hermetic fixture readiness signal: %w", err)
	}
	readyText = strings.TrimSpace(readyText)
	if readyText != "ready" {
		return hermeticProbeReport{}, fmt.Errorf("hermetic fixture readiness signal = %q, want %q", readyText, "ready")
	}

	initial, err := readHermeticState(targetContext)
	if err != nil {
		return hermeticProbeReport{}, fmt.Errorf("read initial hermetic fixture state: %w", err)
	}
	if initial.URL != fixtureURL {
		return hermeticProbeReport{}, fmt.Errorf("initial fixture URL = %q, want %q", initial.URL, fixtureURL)
	}
	if !initial.Ready || initial.Value != hermeticInitialValue || initial.VisibleText != hermeticInitialValue {
		return hermeticProbeReport{}, fmt.Errorf("initial hermetic fixture state = %+v, want ready initial state", initial)
	}

	action := hermeticActionReport{Attempted: true}
	var actionResult hermeticActionResult
	if err := chromedp.Run(targetContext, chromedp.Evaluate(hermeticTransitionExpression(), &actionResult)); err != nil {
		action.Outcome = "error"
		action.Error = err.Error()
		return hermeticProbeReport{}, fmt.Errorf("evaluate hermetic fixture transition: %w", err)
	}
	action.Outcome = "success"
	action.Returned = actionResult
	if actionResult.Value != hermeticFinalValue || actionResult.VisibleText != hermeticFinalValue {
		return hermeticProbeReport{}, fmt.Errorf("transition result = %+v, want value and visible text %q", actionResult, hermeticFinalValue)
	}

	final, err := readHermeticState(targetContext)
	if err != nil {
		return hermeticProbeReport{}, fmt.Errorf("read final hermetic fixture state: %w", err)
	}
	stateMatch := final.URL == fixtureURL && final.Ready &&
		final.Value == hermeticFinalValue && final.VisibleText == hermeticFinalValue &&
		len(final.Transitions) == 1 && final.Transitions[0] == hermeticFinalValue
	if !stateMatch {
		return hermeticProbeReport{}, fmt.Errorf("final hermetic fixture state = %+v, expected one %q transition", final, hermeticFinalValue)
	}

	return hermeticProbeReport{
		ObservedAt:    time.Now().UTC().Format(time.RFC3339),
		Endpoint:      endpoint,
		FixtureOrigin: parsedFixtureURL.Scheme + "://" + parsedFixtureURL.Host,
		FixtureURL:    fixtureURL,
		ReadySignal:   "#ready text = ready",
		Initial:       initial,
		Action:        action,
		Final:         final,
		ExpectedFinal: hermeticFinalValue,
		StateMatch:    stateMatch,
		ControlPath:   "chromedp.Navigate + WaitReady + Evaluate over pinned CDP",
		WebMCPPath:    "not exercised; this row proves generic CDP fixture control only",
		Verdict:       "PASS",
	}, nil
}
