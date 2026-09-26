package testnet

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// clampedReceiveWindowBytes is the agent's receive window in the failing
// nightly run of test46/slow_device (ss: rb52438, snd_wnd 52224). The
// fixture's side of that connection had negotiated a 64512-byte segment and
// 91908 bytes stayed unsent on a persist timer for 30 s.
const clampedReceiveWindowBytes = 52224

// TestWANSegmentServerSegmentsFitClampedReceiveWindow pins the fixture
// invariant: a connection never negotiates a segment larger than a WAN path
// carries, which is well inside the agent window observed in that run.
func TestWANSegmentServerSegmentsFitClampedReceiveWindow(t *testing.T) {
	type observation struct {
		segment int
		err     error
	}
	observed := make(chan observation, 1)
	server := NewWANSegmentServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			observed <- observation{err: err}
			return
		}
		defer func() { _ = conn.Close() }() //nolint:errcheck // the observation is already recorded; closing only ends the probe connection.
		segment, err := connectionSegmentBytes(conn)
		observed <- observation{segment: segment, err: err}
	}))
	defer server.Close()

	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial fixture: %v", err)
	}
	defer func() { _ = conn.Close() }() //nolint:errcheck // the probe connection carries no data after the request.
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: fixture\r\n\r\n"); err != nil {
		t.Fatalf("request fixture connection: %v", err)
	}
	var got observation
	select {
	case got = <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture did not report its connection's segment size")
	}
	if got.err != nil {
		t.Fatalf("read fixture segment size: %v", got.err)
	}
	if !segmentSizeEnforced {
		return
	}
	if got.segment <= 0 || got.segment > WANSegmentBytes {
		t.Fatalf("fixture segment = %d bytes, want 1..%d (inside the %d-byte agent window)", got.segment, WANSegmentBytes, clampedReceiveWindowBytes)
	}
}

func connectionSegmentBytes(conn net.Conn) (int, error) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return 0, fmt.Errorf("fixture connection is %T, want TCP", conn)
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return 0, err
	}
	segment, optionErr := 0, error(nil)
	if err := raw.Control(func(fd uintptr) { segment, optionErr = segmentBytes(fd) }); err != nil {
		return 0, err
	}
	return segment, optionErr
}
