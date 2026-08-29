package rtc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type rtspFixtureObservation struct {
	sync.Mutex
	methods []string
	paths   []string
	path    string
	auth    string
}

type rtspFixtureOptions struct {
	body          string
	challengeAuth bool
	requireAuth   bool
	frameGate     <-chan struct{}
	videoPayload  []byte
}

func TestMediaSourceParsingRedactsPrivateDialState(t *testing.T) {
	secret := "pw-" + "marker"
	source, err := ParseMediaSource("rtsp://camera:" + secret + "@127.0.0.1:554/cam/main")
	if err != nil {
		t.Fatal(err)
	}
	if source.String() != "rtsp://camera:"+RedactionMarker+"@127.0.0.1:554/cam/main" {
		t.Fatalf("identity = %q", source)
	}
	if strings.Contains(source.String(), secret) || !strings.Contains(source.String(), RedactionMarker) {
		t.Fatalf("identity leaked or omitted marker: %q", source)
	}
	if source.password != secret {
		t.Fatal("private auth state did not retain credentials for the protocol boundary")
	}

	go2rtc, err := ParseMediaSource("go2rtc://localhost:1984/api/ws?src=tuya-main")
	if err != nil {
		t.Fatal(err)
	}
	if got := go2rtc.dialURL; got != "ws://localhost:1984/api/ws?src=tuya-main" {
		t.Fatalf("go2rtc dial URL = %q", got)
	}
	if go2rtc.String() != "go2rtc://localhost:1984/api/ws?src=tuya-main" {
		t.Fatalf("go2rtc identity = %q", go2rtc)
	}
}

func TestMediaSourceS4TypedErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want error
	}{
		{"missing src", "go2rtc://localhost:1984/api/ws", ErrMalformedSource},
		{"wrong scheme", "http://localhost/camera", ErrMalformedSource},
		{"no audio shape", "rtsp://localhost:554/", ErrMalformedSource},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseMediaSource(tc.raw)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, tc.want)
			}
			var typed *MediaSourceError
			if !errors.As(err, &typed) || typed.Source == "" || typed.Kind == "" {
				t.Fatalf("error = %v, want typed safe source error", err)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := ProbeMediaSource(ctx, "rtsp://127.0.0.1:1/camera")
	if !errors.Is(err, ErrSourceWrongPort) || !errors.Is(err, ErrSourceUnreachable) || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("unreachable error = %v", err)
	}
}

func TestMediaSourceS4RuntimeErrorTaxonomy(t *testing.T) {
	t.Run("unreachable host preserves network cause", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		_, err := ProbeMediaSource(ctx, "rtsp://camera:secret@unreachable.invalid:554/camera")
		typed := assertSourceError(t, err, SourceErrorUnreachable, "secret")
		if !strings.Contains(typed.Source, "unreachable.invalid") {
			t.Fatalf("source identity = %q", typed.Source)
		}
		var networkErr net.Error
		if !errors.As(err, &networkErr) {
			t.Fatalf("error = %v, want preserved network cause", err)
		}
	})

	t.Run("wrong port has stable subtype", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		_, err := ProbeMediaSource(ctx, "rtsp://camera:secret@127.0.0.1:1/camera")
		assertSourceError(t, err, SourceErrorWrongPort, "secret")
		if !errors.Is(err, ErrSourceUnreachable) {
			t.Fatalf("wrong-port error = %v, want general unreachable identity too", err)
		}
	})

	t.Run("bad credentials", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		var observed rtspFixtureObservation
		serverDone := make(chan error, 1)
		go func() {
			serverDone <- serveRTSPFixture(listener, &observed, "correct-password", rtspFixtureOptions{challengeAuth: true, requireAuth: true})
		}()
		_, err = ProbeMediaSource(context.Background(), fmt.Sprintf("rtsp://camera:wrong-password@%s/camera", listener.Addr()))
		assertSourceError(t, err, SourceErrorAuthentication, "wrong-password")
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Fatal("bad-credentials fixture did not close")
		}
	})

	t.Run("unknown go2rtc source", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/ws" || r.URL.Query().Get("src") != "missing-camera" {
				t.Fatalf("request = %s %s", r.Method, r.URL.String())
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()
		u, _ := url.Parse(server.URL)
		_, err := ProbeMediaSource(context.Background(), "go2rtc://"+u.Host+"/api/ws?src=missing-camera")
		assertSourceError(t, err, SourceErrorUnknown, "")
	})

	t.Run("source has no audio", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		var observed rtspFixtureObservation
		serverDone := make(chan error, 1)
		go func() {
			serverDone <- serveRTSPFixture(listener, &observed, "", rtspFixtureOptions{body: "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=video-only\r\nt=0 0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=control:trackID=1\r\n", challengeAuth: false})
		}()
		_, err = ProbeMediaSource(context.Background(), fmt.Sprintf("rtsp://%s/video-only", listener.Addr()))
		assertSourceError(t, err, SourceErrorNoAudio, "")
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Fatal("no-audio fixture did not close")
		}
	})

	t.Run("non-responsive endpoint is deadline bounded", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		serverDone := make(chan error, 1)
		go func() {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				serverDone <- acceptErr
				return
			}
			defer conn.Close()
			_, copyErr := io.Copy(io.Discard, conn)
			serverDone <- copyErr
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		started := time.Now()
		_, err = ProbeMediaSource(ctx, fmt.Sprintf("rtsp://%s/non-responsive", listener.Addr()))
		if time.Since(started) > time.Second {
			t.Fatalf("probe exceeded bound: %v", time.Since(started))
		}
		assertSourceError(t, err, SourceErrorUnreachable, "")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline error = %v, want context deadline identity", err)
		}
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Fatal("non-responsive fixture did not close")
		}
	})
}

func assertSourceError(t *testing.T, err error, wantKind SourceErrorKind, secret string) *MediaSourceError {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", wantKind)
	}
	var typed *MediaSourceError
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want *MediaSourceError", err)
	}
	if typed.Kind != wantKind || typed.Source == "" || !strings.Contains(err.Error(), typed.Source) {
		t.Fatalf("typed error = %#v, text = %q", typed, err)
	}
	if secret != "" && strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked secret %q: %v", secret, err)
	}
	return typed
}
