package discovery

import "testing"

func TestEndpointFromActivePortNormalizesValidAndRejectsInvalidRecords(t *testing.T) {
	endpoint, err := endpointFromActivePort(ActivePortRecord{Port: 9222, BrowserWebSocketPath: "/devtools/browser/browser-id"})
	if err != nil {
		t.Fatalf("endpointFromActivePort() error = %v", err)
	}
	if endpoint.CDPURL != "http://127.0.0.1:9222/json/version" || endpoint.BrowserWSEndpoint != "ws://127.0.0.1:9222/devtools/browser/browser-id" {
		t.Fatalf("endpointFromActivePort() = %#v, want normalized loopback endpoint", endpoint)
	}

	absolute, err := endpointFromActivePort(ActivePortRecord{Port: 9223, BrowserWebSocketPath: "ws://127.0.0.1:9223/devtools/browser/absolute"})
	if err != nil || absolute.CDPURL != "http://127.0.0.1:9223/json/version" || absolute.BrowserWSEndpoint != "ws://127.0.0.1:9223/devtools/browser/absolute" {
		t.Fatalf("absolute active-port endpoint = %#v, error=%v", absolute, err)
	}
	for _, record := range []ActivePortRecord{
		{Port: 0, BrowserWebSocketPath: "/devtools/browser/id"},
		{Port: 9222, BrowserWebSocketPath: "relative"},
		{Port: 9222, BrowserWebSocketPath: ""},
	} {
		if _, err := endpointFromActivePort(record); err == nil {
			t.Fatalf("endpointFromActivePort(%#v) unexpectedly succeeded", record)
		}
	}
}
