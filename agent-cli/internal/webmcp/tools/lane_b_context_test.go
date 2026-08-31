package tools

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestLaneBGetContextReportsNoPageSelected(t *testing.T) {
	response, err := New(Options{Service: &fakeDiscovery{}}).Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "lane-b-no-page",
		Name:      GetContextToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("get context without selection: %v", err)
	}
	envelope, err := UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode no-page context: %v", err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(ErrorStaleSelection) || envelope.Error.Message != "no page is selected" {
		t.Fatalf("no-page context envelope = %+v, want truthful no-selection failure", envelope)
	}
	if details := envelope.Error.Details; details["browser_id"] != "" || details["target_id"] != "" || details["selected_generation"] != float64(0) || details["reason"] != "selection_not_connected" {
		t.Fatalf("no-page context details = %#v, want empty identity at generation zero", details)
	}
}
