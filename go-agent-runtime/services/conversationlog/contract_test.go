package conversationlog_test

import (
	"encoding/json"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/conversationlog"
)

func TestToolEventJSONKeepsCallResultAndUnknownShapesDistinct(t *testing.T) {
	image := &conversationlog.ImageEvidence{Path: "images/screen.png", Source: "browser", MIMEType: "image/png", ByteLength: 4, Width: 2, Height: 2, SHA256: "hash", TypedProjection: "screen"}
	tests := []struct {
		name  string
		event conversationlog.ToolEvent
		want  string
	}{
		{
			name:  "call",
			event: conversationlog.ToolEvent{Sequence: 1, Type: "tool_call", ToolCallID: "call", ToolName: "screen", Arguments: "{}"},
			want:  `{"sequence":1,"type":"tool_call","tool_call_id":"call","tool_name":"screen","arguments":"{}"}`,
		},
		{
			name:  "result",
			event: conversationlog.ToolEvent{Sequence: 2, Type: "tool_result", ToolCallID: "call", ToolName: "screen", Status: "completed", Image: image},
			want:  `{"sequence":2,"type":"tool_result","tool_call_id":"call","tool_name":"screen","status":"completed","content":"","image":{"path":"images/screen.png","source":"browser","mime_type":"image/png","byte_length":4,"width":2,"height":2,"sha256":"hash","typed_projection":"screen"}}`,
		},
		{
			name:  "unknown",
			event: conversationlog.ToolEvent{Sequence: 3, Type: "other", ToolCallID: "call", ToolName: "screen", Arguments: "args", Status: "failed", Content: "content"},
			want:  `{"sequence":3,"type":"other","tool_call_id":"call","tool_name":"screen","arguments":"args","status":"failed","content":"content"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := json.Marshal(test.event)
			if err != nil {
				t.Fatalf("Marshal = %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("Marshal = %s, want %s", got, test.want)
			}
		})
	}
}
