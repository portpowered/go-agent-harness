package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRemoteRenderMonitorStopIsBoundedBeforeFinalPoll(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case requests <- struct{}{}:
		default:
		}
		<-request.Context().Done()
	}))
	defer server.Close()
	monitor := &remoteRenderMonitor{endpoint: strings.TrimPrefix(server.URL, "http://"), observer: func(int, []int16) {}, cancel: func() {}, done: make(chan struct{})}
	started := time.Now()
	monitor.Stop()
	if elapsed := time.Since(started); elapsed > remoteRenderStopTimeout+50*time.Millisecond {
		t.Fatalf("remote render stop took %s, want at most %s", elapsed, remoteRenderStopTimeout+50*time.Millisecond)
	}
	if len(requests) != 0 {
		t.Fatal("remote render stop polled after its deadline")
	}
}
