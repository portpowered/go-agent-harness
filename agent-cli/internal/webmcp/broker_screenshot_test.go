package webmcp_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestStatefulBrokerCapturesTheExactSelectedPageWithoutActivation(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-capture", Product: "fixture", Loopback: true}
	firstImage := screenshotPNG(t, color.RGBA{R: 0xff, A: 0xff})
	secondImage := screenshotPNG(t, color.RGBA{G: 0xff, A: 0xff})
	runtime := testkit.NewScriptedBrowserRuntime(
		testkit.NewBrowserConfig(candidate,
			testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-first", Type: "page"},
				testkit.WithInitialCatalog(pageTool("read_first", "frame-1", `{"type":"object","additionalProperties":false}`)),
				testkit.WithPageScreenshot(webmcp.PageScreenshot{MIMEType: "image/png", Bytes: firstImage}),
			),
			testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-second", Type: "page"},
				testkit.WithInitialCatalog(pageTool("read_second", "frame-2", `{"type":"object","additionalProperties":false}`)),
				testkit.WithPageScreenshot(webmcp.PageScreenshot{MIMEType: "image/png", Bytes: secondImage}),
			),
		),
	)
	defer func() { _ = runtime.Close() }()
	broker := webmcp.NewBroker(webmcp.BrokerOptions{Runtime: runtime, Discoverer: staticDiscoverer{candidate}})
	defer func() { _ = broker.Close() }()

	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-second"}); err != nil {
		t.Fatalf("select exact target: %v", err)
	}
	got, err := broker.CapturePageScreenshot(context.Background())
	if err != nil {
		t.Fatalf("capture exact target: %v", err)
	}
	if got.BrowserID != candidate.ID || got.TargetID != "tab-second" || got.MIMEType != "image/png" || !bytes.Equal(got.Bytes, secondImage) {
		t.Fatalf("capture = %+v, want second target and its image", got)
	}

	var captures []testkit.Operation
	for _, operation := range runtime.Operations() {
		if operation.Kind == testkit.OperationCapturePageScreenshot {
			captures = append(captures, operation)
		}
		if operation.Kind == testkit.OperationActivate {
			t.Fatalf("capture unexpectedly activated a target: %+v", operation)
		}
	}
	if len(captures) != 1 || captures[0].BrowserID != candidate.ID || captures[0].TargetID != "tab-second" {
		t.Fatalf("capture operations = %+v, want one exact-target operation", captures)
	}
}

func TestStatefulBrokerCaptureClassifiesSelectionLifecycleFailures(t *testing.T) {
	cases := []struct {
		name string
		end  func(*testkit.ScriptedTargetSession) error
		code webmcp.ErrorCode
	}{
		{name: "detached", end: func(session *testkit.ScriptedTargetSession) error { return session.Detach("capture_target_closed") }, code: webmcp.ErrorTargetDetached},
		{name: "disconnected", end: func(session *testkit.ScriptedTargetSession) error {
			return session.Disconnect("capture_transport_lost")
		}, code: webmcp.ErrorBrowserDisconnected},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := webmcp.BrowserCandidate{ID: webmcp.BrowserID("browser-lifecycle-" + testCase.name), Product: "fixture", Loopback: true}
			runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
				testkit.NewTargetConfig(
					webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
					testkit.WithInitialCatalog(pageTool("read_state", "frame-1", `{"type":"object","additionalProperties":false}`)),
					testkit.WithPageScreenshot(webmcp.PageScreenshot{MIMEType: "image/png", Bytes: screenshotPNG(t, color.RGBA{B: 0xff, A: 0xff})}),
				),
			))
			defer func() { _ = runtime.Close() }()
			broker := webmcp.NewBroker(webmcp.BrokerOptions{Runtime: runtime, Discoverer: staticDiscoverer{candidate}})
			defer func() { _ = broker.Close() }()
			if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"}); err != nil {
				t.Fatalf("select: %v", err)
			}
			session := runtime.Browser(candidate.ID).TargetSession("tab-a")
			if session == nil {
				t.Fatal("selected test session is nil")
			}
			if err := testCase.end(session); err != nil {
				t.Fatalf("end session: %v", err)
			}
			_, err := broker.CapturePageScreenshot(context.Background())
			var classified *webmcp.ClassifiedError
			if !errors.As(err, &classified) || classified.Code != testCase.code {
				t.Fatalf("capture error = %v (%T), want %s", err, err, testCase.code)
			}
		})
	}
}

func TestStatefulBrokerCaptureIsolatedAcrossConcurrentSelections(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-concurrent", Product: "fixture", Loopback: true}
	firstImage := screenshotPNG(t, color.RGBA{R: 0xff, A: 0xff})
	secondImage := screenshotPNG(t, color.RGBA{G: 0xff, A: 0xff})
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
			testkit.WithInitialCatalog(pageTool("read_a", "frame-a", `{"type":"object","additionalProperties":false}`)),
			testkit.WithPageScreenshot(webmcp.PageScreenshot{MIMEType: "image/png", Bytes: firstImage})),
		testkit.NewTargetConfig(webmcp.Target{BrowserID: candidate.ID, ID: "tab-b", Type: "page"},
			testkit.WithInitialCatalog(pageTool("read_b", "frame-b", `{"type":"object","additionalProperties":false}`)),
			testkit.WithPageScreenshot(webmcp.PageScreenshot{MIMEType: "image/png", Bytes: secondImage})),
	))
	defer func() { _ = runtime.Close() }()
	newBroker := func() *webmcp.StatefulBroker {
		return webmcp.NewBroker(webmcp.BrokerOptions{Runtime: runtime, Discoverer: staticDiscoverer{candidate}})
	}
	firstBroker, secondBroker := newBroker(), newBroker()
	defer func() { _ = firstBroker.Close() }()
	defer func() { _ = secondBroker.Close() }()
	if _, err := firstBroker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"}); err != nil {
		t.Fatalf("select tab-a: %v", err)
	}
	if _, err := secondBroker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-b"}); err != nil {
		t.Fatalf("select tab-b: %v", err)
	}

	type captureResult struct {
		page webmcp.PageScreenshot
		err  error
	}
	results := make(chan captureResult, 2)
	var wait sync.WaitGroup
	for _, broker := range []*webmcp.StatefulBroker{firstBroker, secondBroker} {
		wait.Add(1)
		go func(broker *webmcp.StatefulBroker) {
			defer wait.Done()
			page, err := broker.CapturePageScreenshot(context.Background())
			results <- captureResult{page: page, err: err}
		}(broker)
	}
	wait.Wait()
	close(results)
	seen := map[webmcp.TargetID]bool{}
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent capture: %v", result.err)
		}
		seen[result.page.TargetID] = true
	}
	if len(seen) != 2 || !seen["tab-a"] || !seen["tab-b"] {
		t.Fatalf("concurrent capture target IDs = %#v, want both isolated targets", seen)
	}
}

func screenshotPNG(t *testing.T, fill color.RGBA) []byte {
	t.Helper()
	imageValue := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			imageValue.SetRGBA(x, y, fill)
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, imageValue); err != nil {
		t.Fatalf("encode screenshot fixture: %v", err)
	}
	return buffer.Bytes()
}
