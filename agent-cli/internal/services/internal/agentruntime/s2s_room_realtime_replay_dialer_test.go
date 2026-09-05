package agentruntime

import (
	"strings"
	"testing"

	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestRoomRealtimeReplayDialer_ReportsParticipantAndScriptPosition(t *testing.T) {
	tests := []struct {
		name       string
		records    []gwtesting.CapturedSessionEvent
		drive      func(transport.Conn) error
		wantStep   string
		wantActual string
	}{
		{
			name: "out of order outbound",
			records: []gwtesting.CapturedSessionEvent{
				roomRealtimeReplayEvent(1, gwtesting.DirectionServerToClient, "session.created", []byte(`{"type":"session.created"}`)),
			},
			drive: func(conn transport.Conn) error {
				return conn.WriteMessage(1, []byte(`{"type":"response.create"}`))
			},
			wantStep:   "sequence 1",
			wantActual: "response.create",
		},
		{
			name: "missing outbound on close",
			records: []gwtesting.CapturedSessionEvent{
				roomRealtimeReplayEvent(1, gwtesting.DirectionServerToClient, "session.created", []byte(`{"type":"session.created"}`)),
				roomRealtimeReplayEvent(2, gwtesting.DirectionClientToServer, "response.create", []byte(`{"type":"response.create"}`)),
			},
			drive: func(conn transport.Conn) error {
				if _, _, err := conn.ReadMessage(); err != nil {
					return err
				}
				return conn.Close()
			},
			wantStep:   "sequence 2",
			wantActual: "connection close",
		},
		{
			name: "duplicate outbound",
			records: []gwtesting.CapturedSessionEvent{
				roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "response.create", []byte(`{"type":"response.create"}`)),
			},
			drive: func(conn transport.Conn) error {
				if err := conn.WriteMessage(1, []byte(`{"type":"response.create"}`)); err != nil {
					return err
				}
				return conn.WriteMessage(1, []byte(`{"type":"response.create"}`))
			},
			wantStep:   "script position 2",
			wantActual: "response.create",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := roomRealtimeReplayCapture(openAIRealtimeDefaultModel, tt.records...)
			dialer, err := newRoomRealtimeReplayDialer("subject", capture)
			if err != nil {
				t.Fatalf("new strict dialer: %v", err)
			}
			conn, err := dialer.Dial("wss://room-replay.invalid", nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			err = tt.drive(conn)
			if err == nil {
				t.Fatal("expected strict replay divergence")
			}
			for _, want := range []string{`participant "subject"`, tt.wantStep, "expected", "actual", tt.wantActual} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("divergence = %q, want substring %q", err, want)
				}
			}
			if got := dialer.Err(); got == nil {
				t.Fatal("dialer did not retain strict replay divergence")
			}
		})
	}
}

func sameRoomReplayStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
