package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionToolCapabilitiesFactoryKeepsDisabledBrowserCompositionInert(t *testing.T) {
	calls := 0
	factory := NewSessionToolCapabilitiesFactory(nil, func(config.BrowserConfig) (webmcp.Broker, error) {
		calls++
		return nil, errors.New("broker must not be constructed")
	})
	cfg := browserCapabilityConfig(false)

	capabilities, err := factory(cfg)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if calls != 0 {
		t.Fatalf("disabled browser constructed broker %d times", calls)
	}
	for _, definition := range capabilities.Definitions {
		if isStableBrokerName(definition.Name) {
			t.Fatalf("disabled definitions include broker tool %q", definition.Name)
		}
	}
	if len(capabilities.Definitions) == 0 {
		t.Fatal("disabled composition dropped the static definitions")
	}
}

func TestSessionToolCapabilitiesFactoryComposesFilteredStaticToolsWithRealBrokerToolSet(t *testing.T) {
	broker := &capabilityBroker{
		selected: webmcp.PageContext{
			Key:        webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"},
			Generation: 1,
			Connected:  true,
			Ready:      true,
		},
	}
	var gotBrowser config.BrowserConfig
	factory := NewSessionToolCapabilitiesFactory(nil, func(browser config.BrowserConfig) (webmcp.Broker, error) {
		gotBrowser = browser
		return broker, nil
	})
	cfg := browserCapabilityConfig(true)

	capabilities, err := factory(cfg)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if !gotBrowser.BrowserBackendEnabled() {
		t.Fatalf("factory received disabled browser config: %+v", gotBrowser)
	}
	if len(capabilities.Definitions) != 7 {
		t.Fatalf("definitions = %d, want one static plus six broker tools", len(capabilities.Definitions))
	}
	if capabilities.Definitions[0].Name != "sleep" {
		t.Fatalf("static definition = %q, want filtered sleep", capabilities.Definitions[0].Name)
	}
	for _, definition := range capabilities.Definitions[1:] {
		if !isStableBrokerName(definition.Name) {
			t.Fatalf("composed definition %q is not a broker tool", definition.Name)
		}
	}

	response, err := capabilities.Executor.Execute(context.Background(), messages.ToolCall{
		ID:        "context-call",
		Name:      webmcp.GetContextToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("broker execute: %v", err)
	}
	if response.ToolCallID != "context-call" || response.Name != webmcp.GetContextToolName || len(response.ContentParts) != 0 {
		t.Fatalf("broker response = %#v", response)
	}
	if _, err := webmcp.UnmarshalToolResult([]byte(response.Content)); err != nil {
		t.Fatalf("broker response is not one WebMCP result envelope: %v; content=%s", err, response.Content)
	}
}

func TestSessionToolCapabilitiesFactoryClosesBrokerWhenCompositionFails(t *testing.T) {
	closeErr := errors.New("broker close failed")
	broker := &capabilityBroker{closeErr: closeErr}
	factory := NewSessionToolCapabilitiesFactory(nil, func(config.BrowserConfig) (webmcp.Broker, error) {
		return broker, errors.New("broker construction failed")
	})

	_, err := factory(browserCapabilityConfig(true))
	if err == nil || !strings.Contains(err.Error(), "broker construction failed") || !strings.Contains(err.Error(), "broker close failed") {
		t.Fatalf("factory error = %v, want construction and cleanup failures", err)
	}
	if broker.closeCalls != 1 {
		t.Fatalf("broker close calls = %d, want one", broker.closeCalls)
	}
}

func TestSessionToolCapabilitiesFactoryTransfersIdempotentCloseHook(t *testing.T) {
	broker := &capabilityBroker{}
	factory := NewSessionToolCapabilitiesFactory(nil, func(config.BrowserConfig) (webmcp.Broker, error) {
		return broker, nil
	})

	capabilities, err := factory(browserCapabilityConfig(true))
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if capabilities.Close == nil {
		t.Fatal("enabled capabilities did not transfer a close hook")
	}
	if err := capabilities.Close(); err != nil {
		t.Fatalf("first capability close: %v", err)
	}
	if err := capabilities.Close(); err != nil {
		t.Fatalf("second capability close: %v", err)
	}
	if broker.closeCalls != 1 {
		t.Fatalf("broker close calls = %d, want one after repeated capability closes", broker.closeCalls)
	}
}

func browserCapabilityConfig(enabled bool) *config.Config {
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = enabled
	cfg := &config.Config{Browser: browser}
	for _, id := range config.DefaultToolIDs {
		cfg.Tools.List = append(cfg.Tools.List, config.ToolEntry{ID: id, Enabled: id == "sleep"})
	}
	return cfg
}

func isStableBrokerName(name string) bool {
	for _, candidate := range webmcp.StableToolNames() {
		if candidate == name {
			return true
		}
	}
	return false
}

type capabilityBroker struct {
	selected   webmcp.PageContext
	closeErr   error
	closeCalls int
}

func (b *capabilityBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return nil, nil
}

func (b *capabilityBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	return nil, nil
}

func (b *capabilityBroker) Select(context.Context, webmcp.TargetSelector) (webmcp.PageContext, error) {
	return b.selected, nil
}

func (b *capabilityBroker) Selected(context.Context) (webmcp.PageContext, error) {
	return b.selected, nil
}

func (b *capabilityBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	return webmcp.ToolCatalogSnapshot{Context: b.selected, Generation: b.selected.Generation}, nil
}

func (b *capabilityBroker) Invoke(context.Context, webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	return webmcp.InvokeResult{}, nil
}

func (b *capabilityBroker) Cancel(context.Context, webmcp.CancelRequest) error { return nil }

func (b *capabilityBroker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	return make(chan webmcp.BrokerEvent)
}

func (b *capabilityBroker) Close() error {
	b.closeCalls++
	return b.closeErr
}

var _ webmcp.Broker = (*capabilityBroker)(nil)
