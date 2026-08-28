package webmcp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestStatefulBrokerDoesNotTreatSuccessfulDomainEnableAsPageToolReadiness(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Product: "fixture", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page", Eligible: true}),
	))
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: staticDiscoverer{candidate},
	})
	defer func() { _ = broker.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := broker.Select(ctx, webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"})
	if err == nil {
		t.Fatal("select succeeded without page-tool evidence")
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("select error = %T %v, want classified page-tool evidence error", err, err)
	}
	if classified.Code != webmcp.ErrorBrowserProtocol {
		t.Fatalf("select error code = %s, want %s", classified.Code, webmcp.ErrorBrowserProtocol)
	}
	if classified.Details["webmcp_domain"] != "supported" || classified.Details["page_tools"] != "unverified" {
		t.Fatalf("readiness details = %#v, want supported domain and unverified page tools", classified.Details)
	}
	if classified.Details["reason_code"] != "page_tools_unverified" {
		t.Fatalf("reason details = %#v, want page_tools_unverified", classified.Details)
	}
}

func TestStatefulBrokerAcceptsExplicitEmptyCatalogEvidence(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Product: "fixture", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page", Eligible: true},
			testkit.WithEnableEvents(webmcp.BrowserEvent{Type: webmcp.EventCatalogReady, CatalogReady: true, ToolCountKnown: true}),
		),
	))
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: staticDiscoverer{candidate},
	})
	defer func() { _ = broker.Close() }()

	selected, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"})
	if err != nil {
		t.Fatalf("select explicit empty catalog: %v", err)
	}
	if !selected.WebMCPDomainSupported || !selected.CatalogReady || !selected.Ready {
		t.Fatalf("selected readiness = %+v, want supported domain, ready catalog, and ready page", selected)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list explicit empty catalog: %v", err)
	}
	if !snapshot.Context.CatalogReady || !snapshot.Context.Ready || len(snapshot.Tools) != 0 {
		t.Fatalf("empty catalog snapshot = %+v, want ready empty catalog", snapshot)
	}
}
