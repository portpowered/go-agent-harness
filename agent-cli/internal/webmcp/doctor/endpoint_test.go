package doctor

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func TestEndpointForDescribesEachLaneRedacted(t *testing.T) {
	cases := []struct {
		name       string
		connection config.BrowserConnectionConfig
		want       Endpoint
	}{
		{name: "none", want: Endpoint{Source: "none configured", Scope: scopeUnknown}},
		{name: "http", connection: config.BrowserConnectionConfig{CDPURL: "http://user:pw@localhost:9222/json?x=1#f"}, want: Endpoint{Source: "explicit HTTP URL", Address: "http://localhost:9222/json", Scope: scopeLoopback}},
		{name: "ws", connection: config.BrowserConnectionConfig{WSEndpoint: "ws://remote.test:9222/devtools/browser/secret"}, want: Endpoint{Source: "explicit WebSocket URL", Address: "ws://remote.test:9222/%3Credacted%3E", Scope: scopeNonLoopback}},
		{name: "profile", connection: config.BrowserConnectionConfig{UserDataDir: "/profile"}, want: Endpoint{Source: "browser profile DevToolsActivePort", Address: "<profile redacted>", Scope: "local profile"}},
		{name: "process", connection: config.BrowserConnectionConfig{AllowProcessScan: true}, want: Endpoint{Source: "process discovery", Scope: "local process"}},
		{name: "unparseable", connection: config.BrowserConnectionConfig{CDPURL: "::"}, want: Endpoint{Source: "explicit HTTP URL", Address: "<redacted endpoint>", Scope: scopeUnknown}},
	}
	for _, testCase := range cases {
		if got := EndpointFor(config.BrowserConfig{Connection: testCase.connection}); got != testCase.want {
			t.Fatalf("%s: EndpointFor = %+v, want %+v", testCase.name, got, testCase.want)
		}
	}
}

func TestEndpointForCandidateAndBrowserScope(t *testing.T) {
	cases := []struct {
		candidate webmcp.BrowserCandidate
		endpoint  Endpoint
		scope     string
	}{
		{candidate: webmcp.BrowserCandidate{HTTPURL: "http://127.0.0.1:9222"}, endpoint: Endpoint{Source: "discovered HTTP URL", Address: "http://127.0.0.1:9222", Scope: scopeLoopback}, scope: scopeLoopback},
		{candidate: webmcp.BrowserCandidate{BrowserWSURL: "wss://remote.test/devtools"}, endpoint: Endpoint{Source: "discovered WebSocket URL", Address: "wss://remote.test/%3Credacted%3E", Scope: scopeNonLoopback}, scope: scopeNonLoopback},
		{candidate: webmcp.BrowserCandidate{Loopback: true}, endpoint: Endpoint{Source: "discovered browser endpoint", Scope: scopeLoopback}, scope: scopeLoopback},
		{candidate: webmcp.BrowserCandidate{}, endpoint: Endpoint{Source: "discovered browser endpoint", Scope: scopeUnknown}, scope: scopeUnknown},
	}
	for _, testCase := range cases {
		if got := EndpointForCandidate(testCase.candidate); got != testCase.endpoint {
			t.Fatalf("EndpointForCandidate(%+v) = %+v, want %+v", testCase.candidate, got, testCase.endpoint)
		}
		if got := browserFromCandidate(testCase.candidate).Scope; got != testCase.scope {
			t.Fatalf("browser scope for %+v = %q, want %q", testCase.candidate, got, testCase.scope)
		}
	}
}

func TestCheckEndpointPolicyDeniesRemoteEndpointsUnlessPermitted(t *testing.T) {
	if err := CheckEndpointPolicy(config.BrowserConfig{Connection: config.BrowserConnectionConfig{CDPURL: "http://127.0.0.1:9222"}}); err != nil {
		t.Fatalf("loopback endpoint denied: %v", err)
	}
	if err := CheckEndpointPolicy(config.BrowserConfig{Connection: config.BrowserConnectionConfig{CDPURL: "ws://host.test"}}); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	remote := config.BrowserConfig{Connection: config.BrowserConnectionConfig{CDPURL: "http://remote.test:9222"}}
	err := CheckEndpointPolicy(remote)
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified.Code != webmcp.ErrorRemoteEndpointDenied || classified.Details[keyRequiredFlag] != requiredRemoteFlag {
		t.Fatalf("remote endpoint error = %v, want remote_endpoint_denied naming %s", err, requiredRemoteFlag)
	}
	remote.Connection.AllowRemoteCDP = true
	if err := CheckEndpointPolicy(remote); err != nil {
		t.Fatalf("permitted remote endpoint denied: %v", err)
	}
}

func TestValidateEndpointsRejectsSchemesAndCredentials(t *testing.T) {
	valid := config.BrowserConfig{Connection: config.BrowserConnectionConfig{CDPURL: "https://host.test", WSEndpoint: "wss://host.test/x"}}
	if err := ValidateEndpoints(valid); err != nil {
		t.Fatalf("ValidateEndpoints(valid) = %v", err)
	}
	for _, connection := range []config.BrowserConnectionConfig{
		{CDPURL: "ws://host.test"},
		{WSEndpoint: "http://host.test"},
		{CDPURL: "http://user:pw@host.test"},
		{WSEndpoint: "not a url"},
	} {
		if err := ValidateEndpoints(config.BrowserConfig{Connection: connection}); err == nil {
			t.Fatalf("ValidateEndpoints(%+v) accepted an invalid endpoint", connection)
		}
	}
}
