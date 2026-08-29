package cli

import (
	"context"
	"encoding/json"
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
