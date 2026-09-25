package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

// TestSessionKeepsBrowserUsableWhenPersistedSelectionIsStale is the customer
// regression. A remembered WebMCP tab that no longer exists (closed, reloaded,
// or now one of several equally eligible pages) is ordinary drift, not a dead
// browser. Before the fix the whole browser capability failed: the provider
// session.update carried no page tools AND no connected-unselected grounding,
// every webmcp_* call failed for the rest of the session, and nothing was ever
// reported - so the model told the customer that browser access does not exist
// and asked for a photo.
//
// Every assertion below is on the encoded session.update frame the OpenAI
// Realtime adapter writes to the websocket, or on a real tool result. None of
// them look at internal registration bookkeeping.
func TestSessionKeepsBrowserUsableWhenPersistedSelectionIsStale(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	candidate := scriptedExplicitCandidate("browser-cube-persisted", "http://127.0.0.1:9225")
	cubeTarget := ambiguousFixtureTarget(candidate.ID, "tab-cube-persisted", "Cubecade", "cube")
	marginTarget := ambiguousFixtureTarget(candidate.ID, "tab-margin-persisted", "Margin", "cube")
	marginTarget.URL = "https://cube.example.test/margin"
	getCubeState, queueCubeMoves := wireCubeTools()
	marginTool := ambiguousFixtureTool("get_document", "Read the Margin document.", "margin-frame")
	runtime := newWireScriptedRuntime(t, candidate,
		testkit.NewTargetConfig(cubeTarget, testkit.WithInitialCatalog(getCubeState, queueCubeMoves), testkit.WithAutoResponse(json.RawMessage(`{"page":"cube"}`))),
		testkit.NewTargetConfig(marginTarget, testkit.WithInitialCatalog(marginTool), testkit.WithAutoResponse(json.RawMessage(`{"page":"margin"}`))),
	)
	discoveryService := &stalePersistedSelectionDiscovery{
		candidate:         wireLaneCandidate(candidate),
		targets:           []discovery.Target{ambiguousSessionLaneTarget(cubeTarget, 2), ambiguousSessionLaneTarget(marginTarget, 1)},
		persistedTargetID: "tab-cube-closed",
	}
	// The shipped default: no explicit selector, auto-select off, persistence on.
	cfg, capabilities := newWirePageToolsCapabilities(t, candidate.HTTPURL, runtime, discoveryService, func(browser *config.BrowserConfig) {
		browser.Selection.AutoSelect = config.BrowserAutoSelectOff
		browser.Selection.Persist = true
	})
	surface := resolveSessionToolSurface(ctx, capabilities)
	wire, runErr := startWirePageToolsSession(t, ctx, cfg, capabilities)

	initial := readWireSessionUpdate(t, ctx, runErr, wire)
	// The wire must never present a browser-enabled session as one with no
	// browser at all. Either the page tools are already there, or the
	// instructions must carry the contract that forbids denying browser access.
	if !initial.tools["get_cube_state"] && !strings.Contains(initial.instructions, sessionConnectedUnselectedBrowserGroundingMarker) {
		t.Fatalf("session.update advertised no page tools and no connected-unselected grounding; the model is free to deny browser access. tools=%v", sortedWireToolNames(initial.tools))
	}
	if !initial.tools[webmcp.ListTabsToolName] || !initial.tools[webmcp.SelectTabToolName] {
		t.Fatalf("session.update tools = %v, want the stable broker surface", sortedWireToolNames(initial.tools))
	}

	// The broker controls must actually work; a stale record must not poison
	// every later browser call.
	listEnvelope := executeAmbiguousPageToolsCall(t, ctx, surface.executor, webmcp.ListTabsToolName, `{}`)
	if !listEnvelope.OK {
		t.Fatalf("webmcp_list_tabs after a stale persisted selection failed: %+v", listEnvelope.Error)
	}
	selectEnvelope := executeAmbiguousPageToolsCall(t, ctx, surface.executor, webmcp.SelectTabToolName,
		`{"browser_id":"`+string(candidate.ID)+`","target_id":"`+string(cubeTarget.ID)+`"}`)
	if !selectEnvelope.OK {
		t.Fatalf("exact Cubecade selection after a stale persisted selection failed: %+v", selectEnvelope.Error)
	}

	deadline := time.Now().Add(10 * time.Second)
	update := readWireSessionUpdate(t, ctx, runErr, wire)
	for !update.tools[getCubeState.Name] {
		if time.Now().After(deadline) {
			t.Fatal("no session.update frame advertised the selected page tools")
		}
		update = readWireSessionUpdate(t, ctx, runErr, wire)
	}
	if !update.tools[queueCubeMoves.Name] {
		t.Fatalf("session.update advertised %v, want both Cubecade page tools", sortedWireToolNames(update.tools))
	}
}

// sessionConnectedUnselectedBrowserGroundingMarker is the customer-visible
// heading of the connected-but-unselected contract carried in the provider
// instructions.
const sessionConnectedUnselectedBrowserGroundingMarker = "WebMCP browser selection:"

type wireSessionUpdate struct {
	tools        map[string]bool
	instructions string
}

func readWireSessionUpdate(t *testing.T, ctx context.Context, runErr <-chan error, wire *sessionUpdateWire) wireSessionUpdate {
	t.Helper()
	select {
	case payload := <-wire.updates:
		var event struct {
			Session struct {
				Instructions string `json:"instructions"`
				Tools        []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"session"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatalf("decode session.update frame: %v", err)
		}
		names := make(map[string]bool, len(event.Session.Tools))
		for _, tool := range event.Session.Tools {
			names[tool.Name] = true
		}
		return wireSessionUpdate{tools: names, instructions: event.Session.Instructions}
	case err := <-runErr:
		t.Fatalf("session ended before writing session.update: %v", err)
	case <-ctx.Done():
		t.Fatalf("waiting for session.update frame: %v", ctx.Err())
	}
	return wireSessionUpdate{}
}

// stalePersistedSelectionDiscovery models the shipped persistence path with a
// remembered tab that is gone. Exact selection still resolves.
type stalePersistedSelectionDiscovery struct {
	candidate         discovery.BrowserCandidate
	targets           []discovery.Target
	persistedTargetID string
}

func (d *stalePersistedSelectionDiscovery) DiscoverAll(context.Context, discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error) {
	return []discovery.BrowserCandidate{d.candidate}, nil
}

func (d *stalePersistedSelectionDiscovery) ListTargetSnapshot(context.Context, discovery.BrowserCandidate, ...discovery.TargetListOptions) (discovery.TargetSnapshot, error) {
	targets := append([]discovery.Target(nil), d.targets...)
	return discovery.TargetSnapshot{
		Browsers:       []discovery.BrowserCandidate{d.candidate},
		Targets:        targets,
		CandidateCount: len(targets),
		EligibleCount:  len(targets),
	}, nil
}

func (d *stalePersistedSelectionDiscovery) LoadPersistedSelection(context.Context) (discovery.PersistedSelection, bool, error) {
	return discovery.PersistedSelection{
		Version:    1,
		EndpointID: d.candidate.ID,
		BrowserID:  d.candidate.ID,
		TargetID:   d.persistedTargetID,
		Origin:     "https://cube.example.test",
		Generation: 1,
		SelectedAt: time.Now(),
	}, true, nil
}

func (d *stalePersistedSelectionDiscovery) Select(_ context.Context, request discovery.TargetSelectionRequest) (discovery.Selection, error) {
	for _, target := range d.targets {
		if target.BrowserID == request.BrowserID && target.ID == request.TargetID {
			return discovery.Selection{
				BrowserID:  request.BrowserID,
				TargetID:   request.TargetID,
				Generation: target.Generation,
				Target:     target,
			}, nil
		}
	}
	return discovery.Selection{}, d.staleFailure(request.TargetID)
}

func (d *stalePersistedSelectionDiscovery) Selected() (discovery.Selection, bool) {
	return discovery.Selection{}, false
}

func (d *stalePersistedSelectionDiscovery) RefreshSelection(context.Context) (discovery.Selection, error) {
	return discovery.Selection{}, nil
}

func (d *stalePersistedSelectionDiscovery) Reconnect(_ context.Context, _ discovery.ConnectionInputs, options ...discovery.ReconnectOptions) (discovery.Selection, error) {
	if len(options) > 0 && options[0].TargetID != "" {
		for _, target := range d.targets {
			if target.ID == options[0].TargetID {
				return discovery.Selection{
					BrowserID:  target.BrowserID,
					TargetID:   target.ID,
					Generation: target.Generation,
					Target:     target,
				}, nil
			}
		}
	}
	// The persisted restore: the remembered tab is no longer current.
	return discovery.Selection{}, d.staleFailure(d.persistedTargetID)
}

func (d *stalePersistedSelectionDiscovery) staleFailure(targetID string) *discovery.DiscoveryError {
	return &discovery.DiscoveryError{
		Code:      discovery.CodeStaleSelection,
		Message:   "the selected browser target is no longer current",
		Retryable: true,
		Details: map[string]any{
			"browser_id":          d.candidate.ID,
			"target_id":           targetID,
			"selected_generation": uint64(1),
			"reason":              "target_missing_after_reconnect",
		},
	}
}
