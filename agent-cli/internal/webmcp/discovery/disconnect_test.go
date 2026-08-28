package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDisconnectDuringDiscoveryReturnsSafeClassifiedFailure(t *testing.T) {
	httpClient := &fakeHTTPClient{err: NewBrowserDisconnectedError("", "", "version", errors.New("ws://user:secret@127.0.0.1:9222 disconnected"))}
	recorder := &eventRecorder{}
	service := New(Options{HTTPClient: httpClient, EventSink: recorder})

	_, err := service.Discover(context.Background(), ConnectionInputs{
		CDPURL: "http://127.0.0.1:9222?token=http-secret#fragment",
	})
	disconnected := assertDiscoveryError(t, err, CodeBrowserDisconnected)
	if !publicIDPattern.MatchString(disconnected.Details["browser_id"].(string)) {
		t.Fatalf("browser ID = %#v, want normalized identifier", disconnected.Details["browser_id"])
	}
	if disconnected.Details["phase"] != "version" || disconnected.Details["reconnect_required"] != true || disconnected.Retryable {
		t.Fatalf("disconnect details = %#v retryable=%v", disconnected.Details, disconnected.Retryable)
	}
	if got := eventTypes(recorder.events); strings.Join(got, ",") != "browser.discovery.started,browser.discovery.completed" {
		t.Fatalf("event types = %v, want discovery start/completion", got)
	}
	encoded, marshalErr := json.Marshal(disconnected)
	if marshalErr != nil {
		t.Fatalf("marshal disconnect error: %v", marshalErr)
	}
	for _, secret := range []string{"ws://", "user:secret", "token=http-secret", "#fragment"} {
		if strings.Contains(string(encoded), secret) || strings.Contains(err.Error(), secret) {
			t.Fatalf("disconnect result leaked %q: %s", secret, encoded)
		}
	}
	if len(httpClient.calls) != 1 {
		t.Fatalf("HTTP calls = %d, want one attempted source", len(httpClient.calls))
	}
}

func TestDisconnectAliasesAndMarkersRemainSafe(t *testing.T) {
	service := New(Options{})
	checks := []struct {
		name string
		call func() error
	}{
		{
			name: "browser alias",
			call: func() error {
				_, err := service.HandleBrowserDisconnect(context.Background(), DisconnectEvent{BrowserID: "browser-alias", Phase: "transport"})
				return err
			},
		},
		{
			name: "concise alias",
			call: func() error {
				_, err := service.Disconnect(context.Background(), "browser-alias", "", "transport")
				return err
			},
		},
		{
			name: "event alias",
			call: func() error {
				_, err := service.OnDisconnect(context.Background(), DisconnectEvent{BrowserID: "browser-alias", Phase: "transport"})
				return err
			},
		},
		{
			name: "marker alias",
			call: func() error {
				_, err := service.MarkBrowserDisconnected(context.Background(), "browser-alias", "", "transport")
				return err
			},
		},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			assertDiscoveryError(t, check.call(), CodeBrowserDisconnected)
		})
	}

	cause := errors.New("transport cause")
	marker := NewBrowserDisconnectError("browser-alias", "target-alias", "version", cause)
	if marker.Error() != "browser connection disconnected" || !errors.Is(marker, cause) || !errors.Is(marker, ErrBrowserDisconnected) || !IsBrowserDisconnected(marker) {
		t.Fatalf("disconnect marker = %v, want safe cause and classified matching", marker)
	}
	var nilMarker *BrowserDisconnectedError
	if nilMarker.Error() != "browser connection disconnected" || nilMarker.Unwrap() != nil || nilMarker.Is(ErrBrowserDisconnected) {
		t.Fatal("nil disconnect marker did not remain safe")
	}
	if IsBrowserDisconnected(nil) || !IsBrowserDisconnected(errors.New("connection closed")) {
		t.Fatal("disconnect classifier did not handle nil/closed cases")
	}
	var nilDiscoveryErr *DiscoveryError
	if nilDiscoveryErr.Error() != "<nil>" || nilDiscoveryErr.Unwrap() != nil {
		t.Fatal("nil discovery error did not remain safe")
	}
	normalDiscoveryErr := &DiscoveryError{Message: "classified", Cause: cause}
	if normalDiscoveryErr.Error() != "classified" || !errors.Is(normalDiscoveryErr, cause) {
		t.Fatal("discovery error did not preserve its classified cause")
	}
}

func TestDisconnectDuringRefreshInvalidatesSelectionAndBlocksReuse(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-disconnect-refresh", Source: SourceConfigured, Loopback: true}
	descriptor := targetDescriptor("raw-page", "Page", "https://disconnect.test", 1)
	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: descriptor.ID})
	disconnected := false
	lister := TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
		if disconnected {
			return nil, NewBrowserDisconnectedError(browser.ID, targetID, "targets", errors.New("transport token must not escape"))
		}
		return []TargetDescriptor{descriptor}, nil
	})
	service := New(Options{TargetLister: lister})
	selected, err := service.SelectTarget(context.Background(), browser, targetID)
	if err != nil {
		t.Fatalf("initial selection: %v", err)
	}

	disconnected = true
	refreshed, err := service.RefreshSelection(context.Background())
	failure := assertDiscoveryError(t, err, CodeBrowserDisconnected)
	if failure.Details["browser_id"] != browser.ID || failure.Details["target_id"] != targetID || failure.Details["phase"] != "targets" || failure.Details["reconnect_required"] != true {
		t.Fatalf("refresh disconnect details = %#v", failure.Details)
	}
	if refreshed.Context().Connected || refreshed.Context().Ready {
		t.Fatalf("refreshed context = %#v, want disconnected and not ready", refreshed.Context())
	}
	current, ok := service.Selected()
	if !ok || current.Context().Connected || current.Context().Ready {
		t.Fatalf("current disconnected selection = %#v ok=%v", current.Context(), ok)
	}

	_, err = service.ValidateSelection(context.Background(), selected)
	assertDiscoveryError(t, err, CodeBrowserDisconnected)
	_, err = service.SelectTarget(context.Background(), browser, targetID)
	assertDiscoveryError(t, err, CodeBrowserDisconnected)
	if !service.IsBrowserDisconnected(browser.ID) {
		t.Fatal("disconnect state was cleared before an exact reconnect")
	}
}

func TestRetainedEndpointLossIsBrowserDisconnectedButInitialLossStaysUnreachable(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-retained-endpoint", Source: SourceConfigured, Loopback: true}
	descriptor := targetDescriptor("raw-page", "Page", "https://retained-endpoint.test", 1)
	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: descriptor.ID})
	endpointLost := false
	service := New(Options{TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
		if endpointLost {
			return nil, errors.New("dial tcp 127.0.0.1:9222: connect: connection refused")
		}
		return []TargetDescriptor{descriptor}, nil
	})})

	if _, err := service.ListTargets(context.Background(), browser); err != nil {
		t.Fatalf("initial target list: %v", err)
	}
	endpointLost = true
	_, err := service.ListTargets(context.Background(), browser)
	failure := assertDiscoveryError(t, err, CodeEndpointUnreachable)
	if failure.Details["phase"] != "targets" {
		t.Fatalf("initial endpoint failure details = %#v, want target phase", failure.Details)
	}

	endpointLost = false
	if _, err := service.SelectTarget(context.Background(), browser, targetID); err != nil {
		t.Fatalf("select retained target: %v", err)
	}
	endpointLost = true
	_, err = service.RefreshSelection(context.Background())
	failure = assertDiscoveryError(t, err, CodeBrowserDisconnected)
	if failure.Details["browser_id"] != browser.ID || failure.Details["target_id"] != targetID || failure.Details["phase"] != "targets" || failure.Details["reconnect_required"] != true {
		t.Fatalf("retained endpoint failure details = %#v", failure.Details)
	}
}

func TestReconnectUsesExactDisconnectedSelectionAndAdvancesGeneration(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-disconnect-reconnect", Source: SourceConfigured, Loopback: true}
	descriptors := []TargetDescriptor{targetDescriptor("raw-a", "A", "https://reconnect.test/a", 1)}
	probe := &fakeWebSocketProbe{version: BrowserVersion{
		Browser:              "Chrome/151",
		ProtocolVersion:      "1.3",
		WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/browser/browser-secret",
	}}
	service := New(Options{
		IDMapper:       persistenceBrowserIDMapper{id: browser.ID},
		WebSocketProbe: probe,
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return append([]TargetDescriptor(nil), descriptors...), nil
		}),
	})
	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: "raw-a"})
	selected, err := service.SelectTarget(context.Background(), browser, targetID)
	if err != nil {
		t.Fatalf("initial selection: %v", err)
	}
	if selected.Generation != 1 {
		t.Fatalf("initial generation = %d, want 1", selected.Generation)
	}
	if _, err := service.HandleDisconnect(context.Background(), DisconnectEvent{
		BrowserID: browser.ID,
		TargetID:  targetID,
		Phase:     "transport",
	}); assertDiscoveryError(t, err, CodeBrowserDisconnected).Details["target_id"] != targetID {
		t.Fatal("disconnect did not retain exact target identity")
	}

	// A single alternative must not be selected after the exact target is lost.
	descriptors = []TargetDescriptor{targetDescriptor("raw-b", "B", "https://reconnect.test/b", 1)}
	_, err = service.Reconnect(context.Background(), reconnectInputs(), ReconnectOptions{AutoSelect: AutoSelectSingle})
	stale := assertDiscoveryError(t, err, CodeStaleSelection)
	if stale.Details["browser_id"] != browser.ID || stale.Details["target_id"] != targetID || stale.Details["reason"] != "target_missing_after_reconnect" {
		t.Fatalf("alternative reconnect failure = %#v", stale.Details)
	}
	current, ok := service.Selected()
	if !ok || current.TargetID != targetID || current.Context().Connected {
		t.Fatalf("alternative reconnect changed selection = %#v ok=%v", current.Context(), ok)
	}
	_, err = service.Reconnect(context.Background(), reconnectInputs(), ReconnectOptions{
		BrowserID:  browser.ID,
		AutoSelect: AutoSelectSingle,
	})
	assertDiscoveryError(t, err, CodeBrowserDisconnected)

	// Restoring the same exact target creates a new generation, so the old
	// operation reference cannot become valid merely because the transport came
	// back.
	descriptors = []TargetDescriptor{targetDescriptor("raw-a", "A", "https://reconnect.test/a", 1)}
	reconnected, err := service.Reconnect(context.Background(), reconnectInputs(), ReconnectOptions{AutoSelect: AutoSelectSingle})
	if err != nil {
		t.Fatalf("exact reconnect: %v", err)
	}
	if reconnected.TargetID != targetID || reconnected.Generation != 2 || !reconnected.Context().Connected || !reconnected.Context().Ready || service.IsBrowserDisconnected(browser.ID) {
		t.Fatalf("reconnected selection = %#v context=%#v disconnected=%v", reconnected, reconnected.Context(), service.IsBrowserDisconnected(browser.ID))
	}
	_, err = service.ValidateSelection(context.Background(), selected)
	assertDiscoveryError(t, err, CodeStaleSelection)
	if _, err := service.ValidateSelection(context.Background(), reconnected); err != nil {
		t.Fatalf("new generation validation: %v", err)
	}
}

func TestDisconnectedReconnectRejectsChangedContinuityMarker(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-disconnect-continuity", Source: SourceConfigured, Loopback: true}
	descriptor := targetDescriptor("raw-page", "Page", "https://continuity-reconnect.test", 1)
	descriptor.ContinuityMarker = "document-a"
	descriptors := []TargetDescriptor{descriptor}
	probe := &fakeWebSocketProbe{version: BrowserVersion{
		Browser:              "Chrome/151",
		ProtocolVersion:      "1.3",
		WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/browser/browser-secret",
	}}
	service := New(Options{
		IDMapper:       persistenceBrowserIDMapper{id: browser.ID},
		WebSocketProbe: probe,
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return append([]TargetDescriptor(nil), descriptors...), nil
		}),
	})
	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: descriptor.ID})
	selected, err := service.SelectTarget(context.Background(), browser, targetID)
	if err != nil {
		t.Fatalf("initial selection: %v", err)
	}
	if _, err := service.HandleDisconnect(context.Background(), DisconnectEvent{BrowserID: browser.ID, TargetID: targetID, Phase: "transport"}); err == nil {
		t.Fatal("disconnect returned nil")
	}

	descriptors[0].ContinuityMarker = "document-b"
	_, err = service.Reconnect(context.Background(), reconnectInputs(), ReconnectOptions{AutoSelect: AutoSelectSingle})
	stale := assertDiscoveryError(t, err, CodeStaleSelection)
	if stale.Details["browser_id"] != browser.ID || stale.Details["target_id"] != targetID || stale.Details["selected_generation"] != selected.Generation || stale.Details["reason"] != "continuity_changed" {
		t.Fatalf("changed continuity failure = %#v", stale.Details)
	}
	current, ok := service.Selected()
	if !ok || current.BrowserID != browser.ID || current.TargetID != targetID || current.Context().Connected {
		t.Fatalf("changed continuity altered disconnected selection = %#v ok=%v", current.Context(), ok)
	}
}

func TestReleaseAfterDisconnectIsIdempotentAndDetachOnly(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-disconnect-release", Source: SourceConfigured, Loopback: true}
	descriptor := targetDescriptor("raw-page", "Page", "https://release.test", 1)
	detacher := &selectionDetacher{}
	service := New(Options{
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return []TargetDescriptor{descriptor}, nil
		}),
		TargetAttacher: TargetAttacherFunc(func(context.Context, BrowserCandidate, Target) (TargetDetacher, error) {
			return detacher, nil
		}),
	})
	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: descriptor.ID})
	selected, err := service.SelectTarget(context.Background(), browser, targetID)
	if err != nil {
		t.Fatalf("selection: %v", err)
	}
	if _, err := service.HandleDisconnect(context.Background(), browser.ID, targetID, "transport"); err == nil {
		t.Fatal("HandleDisconnect returned nil")
	}
	if err := service.ReleaseSelection(); err != nil {
		t.Fatalf("release after disconnect: %v", err)
	}
	if err := service.ReleaseSelection(); err != nil {
		t.Fatalf("second release after disconnect: %v", err)
	}
	if err := selected.Close(); err != nil {
		t.Fatalf("snapshot close after release: %v", err)
	}
	if detacher.detachCalls != 1 || detacher.closeTarget != 0 || detacher.closeBrowser != 0 || detacher.terminate != 0 || detacher.deleteProfile != 0 {
		t.Fatalf("release operations = %#v, want one detach only", *detacher)
	}
}

func TestSelectionAttachDisconnectIsClassifiedAndBlocksRetry(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-disconnect-attach", Source: SourceConfigured, Loopback: true}
	descriptor := targetDescriptor("raw-page", "Page", "https://attach-disconnect.test", 1)
	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: descriptor.ID})
	service := New(Options{
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return []TargetDescriptor{descriptor}, nil
		}),
		TargetAttacher: TargetAttacherFunc(func(context.Context, BrowserCandidate, Target) (TargetDetacher, error) {
			return nil, NewBrowserDisconnectedError(browser.ID, targetID, "attach", errors.New("ws://secret must not escape"))
		}),
	})

	_, err := service.SelectTarget(context.Background(), browser, targetID)
	failure := assertDiscoveryError(t, err, CodeBrowserDisconnected)
	if failure.Details["browser_id"] != browser.ID || failure.Details["target_id"] != targetID || failure.Details["phase"] != "attach" || failure.Details["reconnect_required"] != true {
		t.Fatalf("attach disconnect details = %#v", failure.Details)
	}
	_, err = service.SelectTarget(context.Background(), browser, targetID)
	assertDiscoveryError(t, err, CodeBrowserDisconnected)
}

func reconnectInputs() ConnectionInputs {
	return ConnectionInputs{ConfiguredSources: []ConfiguredSource{StaticConfiguredSource{
		SourceName: "disconnect-reconnect",
		Value:      Endpoint{BrowserWSEndpoint: "ws://127.0.0.1:9222/devtools/browser/current?token=secret"},
	}}}
}
