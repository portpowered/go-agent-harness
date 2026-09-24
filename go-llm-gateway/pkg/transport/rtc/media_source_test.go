package rtc

import (
	"bufio"
	"bytes"
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

	"github.com/pion/rtp"
	audiocodec "github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
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
		typed := typedSourceError(t, err, SourceErrorUnreachable, "secret")
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

	t.Run("bad credentials", testProbeRejectsBadCredentials)
	t.Run("unknown go2rtc source", testProbeClassifiesUnknownGo2RTCSource)
	t.Run("source has no audio", testProbeClassifiesSourceWithoutAudio)
	t.Run("non-responsive endpoint is deadline bounded", testProbeBoundsNonResponsiveEndpoint)
}

func testProbeRejectsBadCredentials(t *testing.T) {
	var observed rtspFixtureObservation
	addr, serverDone := startRTSPFixture(t, &observed, "correct-password", rtspFixtureOptions{challengeAuth: true, requireAuth: true})
	_, err := ProbeMediaSource(context.Background(), fmt.Sprintf("rtsp://camera:wrong-password@%s/camera", addr))
	assertSourceError(t, err, SourceErrorAuthentication, "wrong-password")
	awaitFixtureDone(t, serverDone, "bad-credentials fixture")
}

func testProbeClassifiesUnknownGo2RTCSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != go2rtcFixtureSignalingPath || r.URL.Query().Get("src") != "missing-camera" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ProbeMediaSource(context.Background(), "go2rtc://"+u.Host+"/api/ws?src=missing-camera")
	assertSourceError(t, err, SourceErrorUnknown, "")
}

func testProbeClassifiesSourceWithoutAudio(t *testing.T) {
	var observed rtspFixtureObservation
	addr, serverDone := startRTSPFixture(t, &observed, "", rtspFixtureOptions{body: "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=video-only\r\nt=0 0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=control:trackID=1\r\n", challengeAuth: false})
	_, err := ProbeMediaSource(context.Background(), fmt.Sprintf("rtsp://%s/video-only", addr))
	assertSourceError(t, err, SourceErrorNoAudio, "")
	awaitFixtureDone(t, serverDone, "no-audio fixture")
}

func testProbeBoundsNonResponsiveEndpoint(t *testing.T) {
	listener := listenLoopback(t)
	serverDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		_, copyErr := io.Copy(io.Discard, conn)
		serverDone <- errors.Join(copyErr, conn.Close())
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := ProbeMediaSource(ctx, fmt.Sprintf("rtsp://%s/non-responsive", listener.Addr()))
	if time.Since(started) > time.Second {
		t.Fatalf("probe exceeded bound: %v", time.Since(started))
	}
	assertSourceError(t, err, SourceErrorUnreachable, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v, want context deadline identity", err)
	}
	awaitFixtureDone(t, serverDone, "non-responsive fixture")
}

// listenLoopback opens a loopback TCP listener that is closed when the test
// ends.
func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requireClosed(t, "listener", listener) })
	return listener
}

// startRTSPFixture serves one RTSP fixture connection on a loopback listener
// and returns its address plus the fixture's completion channel.
func startRTSPFixture(t *testing.T, observed *rtspFixtureObservation, secret string, options ...rtspFixtureOptions) (net.Addr, <-chan error) {
	t.Helper()
	listener := listenLoopback(t)
	serverDone := make(chan error, 1)
	go func() { serverDone <- serveRTSPFixture(listener, observed, secret, options...) }()
	return listener.Addr(), serverDone
}

// awaitFixtureDone waits for a fixture goroutine to finish. The fixture's
// result is not asserted because clients intentionally abandon connections
// mid-protocol.
func awaitFixtureDone(t *testing.T, serverDone <-chan error, fixture string) {
	t.Helper()
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal(fixture + " did not close")
	}
}

func typedSourceError(t *testing.T, err error, wantKind SourceErrorKind, secret string) *MediaSourceError {
	t.Helper()
	assertSourceError(t, err, wantKind, secret)
	var typed *MediaSourceError
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want *MediaSourceError", err)
	}
	return typed
}

func assertSourceError(t *testing.T, err error, wantKind SourceErrorKind, secret string) {
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
}

func TestMediaSourceAPIAndProtocolEdgeContracts(t *testing.T) {
	t.Run("error text and identities", testMediaSourceErrorContracts)
	t.Run("parsing and accessors", testMediaSourceParsingAndAccessorContracts)
	t.Run("stream lifecycle and wrappers", testMediaStreamLifecycleContracts)
	t.Run("SDP and RTSP control parsing", testMediaSourceSDPAndControlContracts)
	t.Run("canceled and closed inbound reads", testMediaSourceInboundReadContracts)
	t.Run("RTSP protocol framing", testRTSPProtocolFramingContracts)
}

func testMediaSourceErrorContracts(t *testing.T) {
	var nilSourceError *MediaSourceError
	if nilSourceError.Error() != "media source error" {
		t.Fatalf("nil source error = %q", nilSourceError.Error())
	}
	unknownError := (&MediaSourceError{Identity: "rtsp://host/camera", Kind: "custom source failure"}).Error()
	if !strings.Contains(unknownError, "rtsp://host/camera") || !strings.Contains(unknownError, "check the source") {
		t.Fatalf("unknown source error = %q", unknownError)
	}
	if (safeCause{err: errors.New("private cause")}).Error() != "source operation failed" {
		t.Fatal("safe cause did not use stable public text")
	}
	if errString(nil) != "" {
		t.Fatal("nil dial error did not have empty diagnostic text")
	}
	wrongPort := &MediaSourceError{Kind: SourceErrorWrongPort, Source: "source"}
	if !errors.Is(wrongPort, ErrSourceWrongPort) || !errors.Is(wrongPort, ErrSourceUnreachable) {
		t.Fatal("wrong-port error lost its stable identities")
	}
	if errors.Is(wrongPort, &MediaSourceError{Kind: SourceErrorWrongPort, Source: "other"}) {
		t.Fatal("source error matched a different source identity")
	}
}

func testMediaSourceParsingAndAccessorContracts(t *testing.T) {
	source, err := NewMediaSource("rtsp://camera:password@host:554/main")
	if err != nil {
		t.Fatal(err)
	}
	if source.Identity() != source.String() || source.URL() != source.String() || source.Kind() != SourceKindRTSP {
		t.Fatalf("source accessors = %q/%q/%q", source.Identity(), source.URL(), source.Kind())
	}
	if parsed, err := ParseSource("go2rtc://host:1984/api/ws?src=main"); err != nil || parsed.Kind() != SourceKindGo2RTC {
		t.Fatalf("ParseSource = %#v/%v", parsed, err)
	}
	if privateGo2RTC, err := ParseMediaSource("go2rtc://user:password@host:1984/api/ws?src=main"); err != nil || privateGo2RTC.password != "password" {
		t.Fatalf("go2rtc private credentials = %#v/%v", privateGo2RTC, err)
	}
	if got := safeIdentity("%"); got != "<invalid source>" {
		t.Fatalf("invalid safe identity = %q", got)
	}
	if got := safeIdentity("rtsp://host:554/main"); got != "rtsp://host:554/main" {
		t.Fatalf("valid safe identity = %q", got)
	}
	for _, raw := range []string{"%", "rtsp://host:554/main#fragment", "rtsp://host:554/main\n"} {
		if _, err := ParseMediaSource(raw); !errors.Is(err, ErrMalformedSource) {
			t.Fatalf("ParseMediaSource(%q) = %v", raw, err)
		}
	}
}

func testMediaStreamLifecycleContracts(t *testing.T) {
	var nilContext context.Context
	normalized := (MediaCapabilities{Codec: "PCMA", AudioSampleRate: 16000, AudioChannels: 2, Video: true}).normalized()
	if normalized.AudioCodec != "PCMA" || normalized.SampleRate != 16000 || normalized.Channels != 2 || !normalized.HasVideo || !normalized.VideoPresent || !normalized.VideoPresence {
		t.Fatalf("normalized capabilities = %#v", normalized)
	}
	var nilStream *MediaStream
	if _, err := nilStream.ReadFrame(context.Background()); !errors.Is(err, ErrPeerNotConnected) || nilStream.Close() != nil {
		t.Fatal("nil stream did not preserve peer-not-connected behavior")
	}
	stream := &MediaStream{Inbound: newPionInbound(nil)}
	if err := stream.Close(); err != nil || stream.Close() != nil {
		t.Fatalf("fallback stream close = %v", err)
	}
	if _, err := stream.ReadFrame(nilContext); !errors.Is(err, io.EOF) {
		t.Fatalf("closed stream frame error = %v", err)
	}
	closeErr := errors.New("close failed")
	closeCalls := 0
	owned := &MediaStream{close: func() error { closeCalls++; return closeErr }}
	firstClose := owned.Close()
	secondClose := owned.Close()
	if !errors.Is(firstClose, closeErr) || !errors.Is(secondClose, closeErr) || closeCalls != 1 {
		t.Fatalf("owned close = %v/%v/%d", firstClose, secondClose, closeCalls)
	}
	bounded, cancel := boundedSourceContext(nilContext)
	if bounded == nil {
		t.Fatal("nil context was not replaced")
	}
	cancel()
	if _, err := (MediaSource{identity: "stub"}).Open(context.Background()); !errors.Is(err, ErrMalformedSource) {
		t.Fatalf("zero source open error = %v", err)
	}
	for name, probe := range map[string]func(context.Context, string) error{
		"probe source":      func(ctx context.Context, raw string) error { _, err := ProbeSource(ctx, raw); return err },
		"open media source": func(ctx context.Context, raw string) error { _, err := OpenMediaSource(ctx, raw); return err },
		"open source":       func(ctx context.Context, raw string) error { _, err := OpenSource(ctx, raw); return err },
	} {
		if err := probe(context.Background(), "bad://source"); !errors.Is(err, ErrMalformedSource) {
			t.Fatalf("%s error = %v", name, err)
		}
	}
	if _, err := OpenMediaSource(context.Background(), "rtsp://127.0.0.1:1/open-wrapper"); err == nil {
		t.Fatal("OpenMediaSource unexpectedly opened refused port")
	}
}

func testMediaSourceSDPAndControlContracts(t *testing.T) {
	audio, video, codec, rate, channels := parseSDP("m=audio 0 RTP/AVP 0\na=rtpmap:garbage\na=rtpmap:2 PCMU\na=rtpmap:3 telephone-event/8000\na=rtpmap:4 PCMA/16000/2\na=rtpmap:5 OPUS/48000/2\nm=video 0 RTP/AVP 96\na=rtpmap:96 H264/90000\nm=application 0 RTP/AVP 97")
	if !audio || !video || codec != "PCMA" || rate != 16000 || channels != 2 {
		t.Fatalf("complex SDP = %t/%t/%q/%d/%d", audio, video, codec, rate, channels)
	}
	if _, _, codec, rate, channels = parseSDP("m=audio 0 RTP/AVP 0\na=rtpmap:0 PCMU/8000/0"); codec != "PCMU" || rate != 8000 || channels != 1 {
		t.Fatalf("zero-channel SDP = %q/%d/%d", codec, rate, channels)
	}
	if _, _, codec, rate, channels = parseSDP("m=audio 0 RTP/AVP 0"); codec != "PCMU" || rate != 8000 || channels != 1 {
		t.Fatalf("default audio SDP = %q/%d/%d", codec, rate, channels)
	}

	trackSDP := "m=audio 0 RTP/AVP 0\na=control:*\nm=video 0 RTP/AVP 96\na=control:trackID=1\nm=application 0 RTP/AVP 97\na=control:ignored"
	tracks := parseRTSPTracks(trackSDP, "rtsp://host/base?profile=main", "rtsp://fallback/camera")
	if len(tracks) != 1 || tracks[0].audio || tracks[0].control != "rtsp://host/base/trackID=1" {
		t.Fatalf("RTSP tracks = %#v", tracks)
	}
	if got := joinRTSPControl("", "rtsp://fallback/camera", "trackID=2"); got != "rtsp://fallback/camera/trackID=2" {
		t.Fatalf("fallback RTSP control = %q", got)
	}
	if got := joinRTSPControl("rtsp://host/base", "", "rtsp://other/track"); got != "rtsp://other/track" {
		t.Fatalf("absolute RTSP control = %q", got)
	}
	if got := joinRTSPControl("rtsp://host/base", "", "/absolute"); got != "rtsp://host/absolute" {
		t.Fatalf("absolute path RTSP control = %q", got)
	}
	if got := joinRTSPControl("://bad", "rtsp://fallback/camera", "track"); got != "://bad/track" {
		t.Fatalf("invalid-base RTSP control = %q", got)
	}
}

func testMediaSourceInboundReadContracts(t *testing.T) {
	var nilContext context.Context
	inbound := newPionInbound(nil)
	ctx, cancelRead := context.WithCancel(context.Background())
	cancelRead()
	if _, err := inbound.ReadFrame(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Pion read = %v", err)
	}
	requireClosed(t, "inbound", inbound)
	if _, err := inbound.ReadFrame(nilContext); !errors.Is(err, io.EOF) {
		t.Fatalf("closed Pion read = %v", err)
	}
	pipeReader, pipeWriter := net.Pipe()
	defer requireClosed(t, "pipe reader", pipeReader)
	defer requireClosed(t, "pipe writer", pipeWriter)
	inboundStream := &rtspInbound{client: &rtspClient{conn: pipeReader, reader: bufio.NewReader(pipeReader)}}
	canceled, cancelRTSP := context.WithCancel(context.Background())
	cancelRTSP()
	if _, err := inboundStream.ReadFrame(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled RTSP read = %v", err)
	}
}

func testRTSPProtocolFramingContracts(t *testing.T) {
	var nilContext context.Context
	response := &rtspClient{reader: bufio.NewReader(strings.NewReader("RTSP/1.0 200 OK\r\nContent-Length: 3\r\nX-Fixture: yes\r\n\r\nabc"))}
	parsedResponse, err := response.readResponse()
	if err != nil || parsedResponse.code != 200 || string(parsedResponse.body) != "abc" || parsedResponse.headers["x-fixture"] != "yes" {
		t.Fatalf("parsed RTSP response = %#v/%v", parsedResponse, err)
	}
	for _, raw := range []string{"bad\r\n", "RTSP/1.0 nope OK\r\n", "RTSP/1.0 200 OK\r\n", "RTSP/1.0 200 OK\r\nContent-Length: 3\r\n\r\nx"} {
		if _, err := (&rtspClient{reader: bufio.NewReader(strings.NewReader(raw))}).readResponse(); err == nil {
			t.Fatalf("readResponse(%q) returned nil error", raw)
		}
	}
	for _, raw := range [][]byte{{'x'}, {'$', 0}, {'$', 0, 0, 1, 0}} {
		inbound := &rtspInbound{client: &rtspClient{reader: bufio.NewReader(bytes.NewReader(raw))}}
		if _, _, err := inbound.readPacket(); err == nil {
			t.Fatalf("readPacket(%x) returned nil error", raw)
		}
	}
	emptyInbound := &rtspInbound{client: &rtspClient{reader: bufio.NewReader(strings.NewReader(""))}}
	if _, err := emptyInbound.ReadFrame(nilContext); err == nil {
		t.Fatal("empty RTSP frame read returned nil error")
	}
	packet, err := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0}, Payload: []byte{0x80}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	videoInterleaved := append([]byte{'$', 1, byte(len(packet) >> 8), byte(len(packet))}, packet...)
	videoInbound := &rtspInbound{client: &rtspClient{reader: bufio.NewReader(bytes.NewReader(videoInterleaved))}, audioChannel: 0, codec: "PCMU"}
	if _, err := videoInbound.ReadFrame(context.Background()); err == nil {
		t.Fatal("video-only RTSP frame read returned nil error")
	}
	if _, _, err := (&rtspInbound{client: &rtspClient{reader: bufio.NewReader(bytes.NewReader([]byte{'$', 0, 0, 2, 0}))}}).readPacket(); err == nil {
		t.Fatal("short RTP body returned nil error")
	}
	if got := audiocodec.DecodeRTPAudioPayload("PCMU", nil); got != nil {
		t.Fatalf("empty audio decode = %v", got)
	}
	if got := audiocodec.DecodeRTPAudioPayload("PCMA", []byte{0xd4}); len(got) != 1 || got[0] <= 0 {
		t.Fatalf("positive A-law decode = %v", got)
	}
}

// requireClosed closes a test-owned resource and reports an unexpected close
// failure. A resource a fixture or earlier close already released reports
// net.ErrClosed, which is the expected terminal state and is tolerated.
func requireClosed(t testing.TB, name string, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("close %s: %v", name, err)
	}
}
