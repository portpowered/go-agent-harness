package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestSessionPageToolsFirstClassAgainstLiveChrome is the credit-free
// session-shape proof for first-class page tools: against an externally
// launched Chrome (WEBMCP_PAGETOOLS_LIVE_CDP_URL) whose active tab exposes a
// WebMCP catalog, the production capability factory bootstraps, the
// refreshed definitions advertise the page tools first-class, bare-name
// calls execute through the composed invoke path with terminal status, and
// an unknown name yields guidance instead of a composition dead-end.
func TestSessionPageToolsFirstClassAgainstLiveChrome(t *testing.T) {
	cdpURL := strings.TrimSpace(os.Getenv("WEBMCP_PAGETOOLS_LIVE_CDP_URL"))
	if cdpURL == "" {
		t.Skip("set WEBMCP_PAGETOOLS_LIVE_CDP_URL to a live Chrome DevTools HTTP endpoint to run the live page-tools proof")
	}

	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Tools.Backend = "webmcp"
	browser.Connection.CDPURL = cdpURL
	browser.Selection.AutoSelect = "single"
	cfg := &config.Config{Browser: browser, ConfigDir: t.TempDir()}
	for _, id := range config.DefaultToolIDs {
		cfg.Tools.List = append(cfg.Tools.List, config.ToolEntry{ID: id, Enabled: id == "exec"})
	}

	capabilities, err := NewSessionToolCapabilitiesFactory(nil, nil)(cfg)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	t.Cleanup(func() {
		if capabilities.Close != nil {
			_ = capabilities.Close()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if capabilities.Initialize != nil {
		if err := capabilities.Initialize(ctx); err != nil {
			t.Fatalf("bootstrap initialize: %v", err)
		}
	}
	base := len(capabilities.Definitions)
	refreshed := capabilities.RefreshDefinitions(ctx)
	if len(refreshed) <= base {
		t.Fatalf("refresh advertised no page tools: %d composed, %d refreshed", base, len(refreshed))
	}
	pageNames := make([]string, 0, len(refreshed)-base)
	for _, definition := range refreshed[base:] {
		pageNames = append(pageNames, definition.Name)
		t.Logf("page tool: %s | %s | params=%d closed=%v", definition.Name, definition.Description, len(definition.Parameters), definition.ParametersClosed)
	}

	execute := func(name, args string) webmcp.ToolResultEnvelope {
		t.Helper()
		response, err := capabilities.Executor.Execute(ctx, messages.ToolCall{ID: "live-" + name, Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("execute %s: %v", name, err)
		}
		var envelope webmcp.ToolResultEnvelope
		if err := json.Unmarshal([]byte(response.Content), &envelope); err != nil {
			t.Fatalf("%s content is not an envelope: %v; %s", name, err, response.Content)
		}
		return envelope
	}

	first := execute(pageNames[0], `{}`)
	if !first.OK {
		t.Fatalf("bare-name %s failed: %+v", pageNames[0], first.Error)
	}
	t.Logf("%s -> %.200s", pageNames[0], string(first.Data))

	guidance := execute("definitely_not_a_page_tool", `{}`)
	if guidance.OK || guidance.Error == nil {
		t.Fatalf("unknown name did not produce guidance: %+v", guidance)
	}
	if !strings.Contains(guidance.Error.Message, "webmcp_list_tools") {
		t.Fatalf("guidance %q lacks the stable-path hint", guidance.Error.Message)
	}
}

// TestSessionPageToolsConcurrentColdSessions is the load-shaped proof for the
// gate's paired-probe reality: two sessions bootstrap concurrently against
// two separate Chromes and each serves its FIRST first-class page-tool call
// within the bounded long-running interactive budget. Set
// WEBMCP_PAGETOOLS_LIVE_CDP_URLS to two comma-separated DevTools HTTP
// endpoints whose active tabs expose a WebMCP catalog.
func TestSessionPageToolsConcurrentColdSessions(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("WEBMCP_PAGETOOLS_LIVE_CDP_URLS"))
	if raw == "" {
		t.Skip("set WEBMCP_PAGETOOLS_LIVE_CDP_URLS=<url1>,<url2> to run the concurrent cold-session proof")
	}
	urls := strings.Split(raw, ",")
	if len(urls) != 2 {
		t.Fatalf("need exactly two endpoints, got %d", len(urls))
	}

	type outcome struct {
		index    int
		duration time.Duration
		err      error
	}
	results := make(chan outcome, len(urls))
	for index, cdpURL := range urls {
		go func(index int, cdpURL string) {
			started := time.Now()
			err := func() error {
				browser := config.DefaultBrowserConfig()
				browser.Tools.Enabled = true
				browser.Tools.Backend = "webmcp"
				browser.Connection.CDPURL = strings.TrimSpace(cdpURL)
				browser.Selection.AutoSelect = "single"
				cfg := &config.Config{Browser: browser, ConfigDir: t.TempDir()}
				for _, id := range config.DefaultToolIDs {
					cfg.Tools.List = append(cfg.Tools.List, config.ToolEntry{ID: id, Enabled: id == "exec"})
				}
				capabilities, err := NewSessionToolCapabilitiesFactory(nil, nil)(cfg)
				if err != nil {
					return fmt.Errorf("factory: %w", err)
				}
				defer func() {
					if capabilities.Close != nil {
						_ = capabilities.Close()
					}
				}()
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				if capabilities.Initialize != nil {
					if err := capabilities.Initialize(ctx); err != nil {
						return fmt.Errorf("bootstrap: %w", err)
					}
				}
				refreshed := capabilities.RefreshDefinitions(ctx)
				if len(refreshed) <= len(capabilities.Definitions) {
					return fmt.Errorf("no page tools advertised")
				}
				name := refreshed[len(capabilities.Definitions)].Name
				// The first page-tool call runs under the bounded long-running
				// interactive budget, exactly as the session executor applies it.
				callContext, cancelCall := context.WithTimeout(ctx, config.DefaultInteractiveLongRunningTimeout)
				defer cancelCall()
				callStarted := time.Now()
				response, err := capabilities.Executor.Execute(callContext, messages.ToolCall{ID: "cold-" + name, Name: name, Arguments: `{}`})
				if err != nil {
					return fmt.Errorf("first page-tool call: %w", err)
				}
				var envelope webmcp.ToolResultEnvelope
				if err := json.Unmarshal([]byte(response.Content), &envelope); err != nil || !envelope.OK {
					return fmt.Errorf("first page-tool call after %s: %s", time.Since(callStarted), response.Content)
				}
				return nil
			}()
			results <- outcome{index: index, duration: time.Since(started), err: err}
		}(index, cdpURL)
	}
	for range urls {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent session %d failed: %v", result.index, result.err)
		}
		t.Logf("session %d first page-tool call served (total %s)", result.index, result.duration)
	}
}
