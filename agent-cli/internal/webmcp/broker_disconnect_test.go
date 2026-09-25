package webmcp_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func TestStatefulBrokerPreservesEndpointFailureBeforeBrowserTransportIsEstablished(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-never-opened", Loopback: true}
	for _, testCase := range []struct {
		name string
		err  error
	}{
		{name: "connection refused", err: errors.New("dial tcp 127.0.0.1:9222: connection refused")},
		{name: "closed transport", err: net.ErrClosed},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			broker := webmcp.NewBroker(webmcp.BrokerOptions{
				Runtime:    failingOpenRuntime{err: testCase.err},
				Discoverer: staticDiscoverer{candidate},
			})
			defer closeAtTestEnd(t, broker)

			_, err := broker.Select(context.Background(), webmcp.TargetSelector{
				BrowserID: candidate.ID,
				TargetID:  "tab-never-opened",
			})
			var classified *webmcp.ClassifiedError
			if !errors.As(err, &classified) {
				t.Fatalf("select error = %T %v, want classified endpoint failure", err, err)
			}
			if classified.Code != webmcp.ErrorEndpointUnreachable {
				t.Fatalf("select error code = %s, want %s", classified.Code, webmcp.ErrorEndpointUnreachable)
			}
			if classified.Details["phase"] != "open" {
				t.Fatalf("select error details = %#v, want open phase", classified.Details)
			}
		})
	}
}

type failingOpenRuntime struct {
	err error
}

func (r failingOpenRuntime) Open(context.Context, webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	return nil, r.err
}

// assertLifecycleTerminalDetails checks the frozen safe details of a
// detach or disconnect terminal result.
func assertLifecycleTerminalDetails(t *testing.T, terminal webmcp.InvokeResult, wantCode webmcp.ErrorCode, browserID webmcp.BrowserID, wantReason string) {
	t.Helper()
	details := terminal.ErrorDetails
	if wantCode == webmcp.ErrorTargetDetached {
		if details["browser_id"] != string(browserID) || details["target_id"] != primaryTargetID || details["generation"] != uint64(1) || details["reason"] != wantReason {
			t.Fatalf("detach details = %#v, want frozen safe details", details)
		}
		return
	}
	if wantCode == webmcp.ErrorBrowserDisconnected {
		if details["browser_id"] != string(browserID) || details["target_id"] != primaryTargetID || details["phase"] != "lifecycle" || details["reconnect_required"] != true {
			t.Fatalf("disconnect details = %#v, want frozen safe details", details)
		}
	}
}
