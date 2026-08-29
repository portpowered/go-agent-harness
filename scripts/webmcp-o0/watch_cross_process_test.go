package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestValidateCrossProcessWatchRequiresTheCompleteOrderedTranscript(t *testing.T) {
	events := []crossProcessWatchEvent{
		{Type: "selected", Sequence: 1, BrowserID: "browser", TargetID: "target", Generation: 1},
		{Type: "catalog_changed", Sequence: 3, BrowserID: "browser", TargetID: "target", Generation: 1, Reason: "tools_added"},
		{Type: "catalog_changed", Sequence: 5, BrowserID: "browser", TargetID: "target", Generation: 1, Reason: "tools_added"},
		{Type: "invocation_created", Sequence: 6, BrowserID: "browser", TargetID: "target", Generation: 1, InvocationID: "inv-1", ToolRef: "webmcp.tool-ref.v1:abcdefghijklmnopqrstuv", State: "dispatched"},
		{Type: "invocation_terminal", Sequence: 7, BrowserID: "browser", TargetID: "target", Generation: 1, InvocationID: "inv-1", ToolRef: "webmcp.tool-ref.v1:abcdefghijklmnopqrstuv", State: "completed"},
		{Type: "catalog_changed", Sequence: 9, BrowserID: "browser", TargetID: "target", Generation: 1, Reason: "tools_removed"},
	}
	browserID, err := validateCrossProcessWatch(crossProcessWatchData{Status: "canceled", Events: events}, "target")
	if err != nil {
		t.Fatalf("validate complete transcript: %v", err)
	}
	if browserID != "browser" {
		t.Fatalf("browser ID = %q, want browser", browserID)
	}

	for _, test := range []struct {
		name   string
		mutate func([]crossProcessWatchEvent)
		want   string
	}{
		{
			name: "missing terminal",
			mutate: func(value []crossProcessWatchEvent) {
				value[4].Type = "catalog_changed"
			},
			want: "event 4 type",
		},
		{
			name: "duplicate sequence",
			mutate: func(value []crossProcessWatchEvent) {
				value[3].Sequence = value[2].Sequence
			},
			want: "strictly increasing",
		},
		{
			name: "cross-target event",
			mutate: func(value []crossProcessWatchEvent) {
				value[2].TargetID = "other-target"
			},
			want: "identity",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := append([]crossProcessWatchEvent(nil), events...)
			test.mutate(mutated)
			if _, err := validateCrossProcessWatch(crossProcessWatchData{Status: "canceled", Events: mutated}, "target"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestBrowserHTTPEndpointConvertsOnlyBrowserWebSocketURLs(t *testing.T) {
	got, err := browserHTTPEndpoint("ws://127.0.0.1:9222/devtools/browser/id")
	if err != nil {
		t.Fatalf("browser endpoint: %v", err)
	}
	if got != "http://127.0.0.1:9222" {
		t.Fatalf("HTTP endpoint = %q, want http://127.0.0.1:9222", got)
	}
	for _, endpoint := range []string{"", "http://127.0.0.1:9222", "ws:///devtools/browser/id"} {
		if _, err := browserHTTPEndpoint(endpoint); err == nil {
			t.Errorf("browser endpoint %q unexpectedly succeeded", endpoint)
		}
	}
}

func TestCrossProcessFixtureProvidesAnIndependentHTTPStateOracle(t *testing.T) {
	fixture, err := newCrossProcessFixture()
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer fixture.Close()

	response, err := http.Post(fixture.StateURL(), "application/json", strings.NewReader(`{"ready":true,"value":"invoked:test","visibleText":"invoked:test","invocations":["test"]}`))
	if err != nil {
		t.Fatalf("post fixture state: %v", err)
	}
	if response.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("post fixture state status = %s body=%q", response.Status, body)
	}
	response.Body.Close()

	response, err = http.Get(fixture.StateURL())
	if err != nil {
		t.Fatalf("get fixture state: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get fixture state status = %s", response.Status)
	}
	var state crossProcessPageState
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatalf("decode fixture state: %v", err)
	}
	if !stateMatchesInvocation(state, "test") {
		t.Fatalf("fixture oracle state = %+v, want test invocation", state)
	}
}
