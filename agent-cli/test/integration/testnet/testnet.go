// Package testnet serves process-edge test fixtures over loopback TCP the way
// their production counterparts reach the agent over a real network.
package testnet

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"
)

// WANSegmentBytes is the TCP maximum segment size of an Ethernet path: a
// 1500-byte MTU minus the IPv4, TCP and timestamp headers. A remote realtime
// provider's segments are this size when they reach the agent.
const WANSegmentBytes = 1448

// NewWANSegmentServer serves handler on a loopback listener whose connections
// carry WAN-sized segments.
//
// Plain Linux loopback grows the segment to 64 KiB. In a nightly run of
// test46/slow_device, after the agent briefly fell behind a loopback-speed
// provider burst on a loaded CI runner, its socket reported a 52224-byte
// receive window, below one segment. The fixture's side then sent nothing for
// 30 s with 91908 bytes queued and the device lost the final response's tail,
// while the agent waited in read(2). WAN-sized segments keep the fixture's
// traffic shaped like a remote provider's, and always fit such a window.
// Platforms where that stall was not observed keep their loopback segment.
func NewWANSegmentServer(t testing.TB, handler http.Handler) *httptest.Server {
	t.Helper()
	config := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var optionErr error
		if err := raw.Control(func(fd uintptr) { optionErr = setWANSegmentSize(fd) }); err != nil {
			return err
		}
		return optionErr
	}}
	listener, err := config.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for WAN-segment fixture: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}}
	server.Start()
	return server
}
