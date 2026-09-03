package chrome

import (
	"context"
	"errors"
	"sync"
	"testing"

	cdpCast "github.com/chromedp/cdproto/cast"
	"github.com/chromedp/cdproto/cdp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

type castCommand struct {
	method   string
	sinkName string
}

func TestTargetSessionSurfacesChromeCastDiscoveryIssue(t *testing.T) {
	session := newInvocationTestSession(t, &castExecutor{})
	session.observeCastProtocolEvent(&cdpCast.EventIssueUpdated{IssueMessage: "Local network permission is required"})
	session.observeCastProtocolEvent(&cdpCast.EventSinksUpdated{Sinks: []*cdpCast.Sink{}})

	_, err := session.ListCastDevices(context.Background())
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified.Code != webmcp.ErrorBrowserProtocol || classified.Details["reason_code"] != "cast_issue" {
		t.Fatalf("cast discovery issue = %v, want browser_protocol_invalid/cast_issue", err)
	}
}

type castExecutor struct {
	mu    sync.Mutex
	calls []castCommand
}

func (e *castExecutor) Execute(ctx context.Context, method string, params, _ any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	call := castCommand{method: method}
	switch value := params.(type) {
	case *cdpCast.StartTabMirroringParams:
		call.sinkName = value.SinkName
	case *cdpCast.StopCastingParams:
		call.sinkName = value.SinkName
	}
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	return nil
}

func (e *castExecutor) snapshot() []castCommand {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]castCommand(nil), e.calls...)
}

var _ cdp.Executor = (*castExecutor)(nil)

func TestTargetSessionListsAndControlsCastDevicesOnItsTarget(t *testing.T) {
	executor := &castExecutor{}
	session := newInvocationTestSession(t, executor)
	session.observeCastProtocolEvent(&cdpCast.EventSinksUpdated{Sinks: []*cdpCast.Sink{{Name: "Living Room TV", ID: "sink-living"}}})

	devices, err := session.ListCastDevices(context.Background())
	if err != nil {
		t.Fatalf("list cast devices: %v", err)
	}
	if len(devices) != 1 || devices[0].Name != "Living Room TV" || devices[0].ID != "sink-living" {
		t.Fatalf("devices = %+v", devices)
	}
	if err := session.CastTab(context.Background(), devices[0].Name); err != nil {
		t.Fatalf("cast tab: %v", err)
	}
	if err := session.StopCasting(context.Background(), devices[0].Name); err != nil {
		t.Fatalf("stop casting: %v", err)
	}

	want := []castCommand{
		{method: cdpCast.CommandEnable},
		{method: cdpCast.CommandStartTabMirroring, sinkName: "Living Room TV"},
		{method: cdpCast.CommandStopCasting, sinkName: "Living Room TV"},
	}
	got := executor.snapshot()
	if len(got) != len(want) {
		t.Fatalf("Cast CDP calls = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("Cast CDP call %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}
