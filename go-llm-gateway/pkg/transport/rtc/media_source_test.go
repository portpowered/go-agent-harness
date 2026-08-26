package rtc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
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

type visualContextKey struct{}

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

func TestRTSPMediaSourceStubNegotiatesAndStreams(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	secret := "rtsp-" + "marker"
	var observed rtspFixtureObservation
	serverDone := make(chan error, 1)
	go func() { serverDone <- serveRTSPFixture(listener, &observed, secret) }()

	source, err := ParseMediaSource(fmt.Sprintf("rtsp://camera:%s@%s/camera/main", secret, listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	frame, err := stream.ReadFrame(context.Background())
	if err != nil || !equalSamples(frame.Samples, []int16{32124, -32124, -716}) {
		t.Fatalf("frame = %#v, error = %v", frame, err)
	}
	if stream.Capabilities.AudioCodec != "PCMU" || stream.Capabilities.SampleRate != 8000 || stream.Capabilities.Channels != 1 || !stream.Capabilities.Video {
		t.Fatalf("capabilities = %#v", stream.Capabilities)
	}
	if strings.Contains(fmt.Sprint(stream.Capabilities), secret) || strings.Contains(stream.Capabilities.Source, secret) {
		t.Fatal("stream capabilities leaked the RTSP password")
	}
	observed.Lock()
	defer observed.Unlock()
	if !strings.Contains(observed.path, "/camera/main") || observed.auth != "Basic "+base64.StdEncoding.EncodeToString([]byte("camera:"+secret)) {
		t.Fatalf("observed path/auth = %q/%q", observed.path, observed.auth)
	}
	if len(observed.methods) < 4 || observed.methods[0] != "DESCRIBE" || observed.methods[1] != "DESCRIBE" || observed.methods[2] != "SETUP" || observed.methods[3] != "SETUP" {
		t.Fatalf("RTSP method order = %v", observed.methods)
	}
	_ = stream.Close()
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("RTSP fixture did not close")
	}
}

func TestRTSPReadRemainsUsableAfterSetupContextCompletes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	frameGate := make(chan struct{})
	var observed rtspFixtureObservation
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveRTSPFixture(listener, &observed, "deadline-password", rtspFixtureOptions{challengeAuth: true, frameGate: frameGate})
	}()

	source, err := ParseMediaSource(fmt.Sprintf("rtsp://camera:deadline-password@%s/camera/main", listener.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(100 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	stream, err := source.Open(ctx)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	timer := time.NewTimer(time.Until(deadline) + 30*time.Millisecond)
	<-timer.C
	close(frameGate)
	frameCtx, frameCancel := context.WithTimeout(context.Background(), time.Second)
	defer frameCancel()
	frame, err := stream.ReadFrame(frameCtx)
	if err != nil || !equalSamples(frame.Samples, []int16{32124, -32124, -716}) {
		t.Fatalf("post-setup frame = %#v, error = %v", frame, err)
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("deadline regression fixture did not close")
	}
}

func TestRTSPVisualLookQueuesAudioAndReturnsCopiedVideo(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var observed rtspFixtureObservation
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveRTSPFixture(listener, &observed, "", rtspFixtureOptions{
			challengeAuth: false,
			videoPayload:  []byte{0x65, 4, 5, 6},
		})
	}()

	source, err := ParseMediaSource(fmt.Sprintf("rtsp://%s/camera/main", listener.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	observation, err := stream.Look(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Source != source.Identity() || observation.Status != VisualObservationAvailable || observation.MediaType != "video/H264" || !bytes.Equal(observation.Bytes, []byte{0x65, 4, 5, 6}) {
		t.Fatalf("RTSP visual observation = %#v", observation)
	}
	observation.Bytes[0] = 0
	frame, err := stream.ReadFrame(ctx)
	if err != nil || !equalSamples(frame.Samples, []int16{32124, -32124, -716}) {
		t.Fatalf("queued RTSP audio frame = %#v, error = %v", frame, err)
	}
	if unavailable, err := stream.Look(ctx); err != nil || unavailable.Status != VisualObservationUnavailable || unavailable.Reason != VisualObservationReasonNoVideoTrack || len(unavailable.Bytes) != 0 {
		t.Fatalf("RTSP visual EOF result = %#v, error = %v", unavailable, err)
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("RTSP visual fixture did not close")
	}
}

func TestLookMediaSourceUsesPublicRTSPContract(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var observed rtspFixtureObservation
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveRTSPFixture(listener, &observed, "", rtspFixtureOptions{
			challengeAuth: false,
			videoPayload:  []byte{0x65, 7, 8, 9},
		})
	}()

	rawURL := fmt.Sprintf("rtsp://%s/camera/public-look", listener.Addr())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observation, err := LookMediaSource(ctx, rawURL)
	if err != nil || observation.Source != rawURL || !observation.Available() || observation.MediaType != "video/H264" || !bytes.Equal(observation.Bytes, []byte{0x65, 7, 8, 9}) {
		t.Fatalf("public RTSP look = %#v, error = %v", observation, err)
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("public RTSP look fixture did not close")
	}
}

func TestRTSPAudioOnlyVisualLookIsUnavailableAndAudioRemainsUsable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var observed rtspFixtureObservation
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveRTSPFixture(listener, &observed, "", rtspFixtureOptions{
			body:          "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=audio-only\r\nt=0 0\r\nm=audio 0 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000/1\r\na=control:trackID=0\r\n",
			challengeAuth: false,
		})
	}()

	source, err := ParseMediaSource(fmt.Sprintf("rtsp://%s/audio-only", listener.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observation, err := stream.Look(ctx)
	if err != nil || observation.Source != source.Identity() || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack || len(observation.Bytes) != 0 {
		t.Fatalf("audio-only RTSP visual observation = %#v, error = %v", observation, err)
	}
	frame, err := stream.ReadFrame(ctx)
	if err != nil || len(frame.Samples) == 0 {
		t.Fatalf("audio-only RTSP frame = %#v, error = %v", frame, err)
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("audio-only RTSP fixture did not close")
	}
}

func serveRTSPFixture(listener net.Listener, observed *rtspFixtureObservation, secret string, options ...rtspFixtureOptions) error {
	config := rtspFixtureOptions{challengeAuth: true}
	if len(options) > 0 {
		config = options[0]
	}
	if config.body == "" {
		config.body = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=fixture\r\nt=0 0\r\nm=audio 0 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000/1\r\na=control:trackID=0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=control:trackID=1\r\n"
	}
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	challenge := false
	for {
		method, path, headers, err := readRTSPRequest(reader)
		if err != nil {
			return err
		}
		observed.Lock()
		observed.methods = append(observed.methods, method)
		observed.paths = append(observed.paths, path)
		observed.path = path
		observed.auth = headers["authorization"]
		observed.Unlock()
		switch method {
		case "DESCRIBE":
			if config.challengeAuth && !challenge {
				challenge = true
				fmt.Fprint(conn, "RTSP/1.0 401 Unauthorized\r\nCSeq: 1\r\nWWW-Authenticate: Basic realm=fixture\r\nContent-Length: 0\r\n\r\n")
				continue
			}
			if config.challengeAuth && headers["authorization"] != "Basic "+base64.StdEncoding.EncodeToString([]byte("camera:"+secret)) {
				if config.requireAuth {
					fmt.Fprintf(conn, "RTSP/1.0 401 Unauthorized\r\nCSeq: %s\r\nWWW-Authenticate: Basic realm=fixture\r\nContent-Length: 0\r\n\r\n", headers["cseq"])
					continue
				}
				return errors.New("fixture did not receive expected authorization")
			}
			fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nContent-Base: %s\r\nContent-Length: %d\r\n\r\n%s", headers["cseq"], path, len(config.body), config.body)
		case "SETUP":
			fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: fixture-session\r\nTransport: RTP/AVP/TCP;unicast;interleaved=%s\r\nContent-Length: 0\r\n\r\n", headers["cseq"], interleavedForSetup(headers["transport"]))
		case "PLAY":
			fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: fixture-session\r\nContent-Length: 0\r\n\r\n", headers["cseq"])
			if config.frameGate != nil {
				<-config.frameGate
			}
			packet, err := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 1, Timestamp: 1}, Payload: []byte{0x80, 0x00, 0x55}}).Marshal()
			if err != nil {
				return err
			}
			if _, err := conn.Write([]byte{'$', 0, byte(len(packet) >> 8), byte(len(packet))}); err != nil {
				return err
			}
			if _, err := conn.Write(packet); err != nil {
				return err
			}
			if len(config.videoPayload) > 0 {
				videoPacket, marshalErr := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: 2, Timestamp: 3000}, Payload: config.videoPayload}).Marshal()
				if marshalErr != nil {
					return marshalErr
				}
				if _, writeErr := conn.Write([]byte{'$', 2, byte(len(videoPacket) >> 8), byte(len(videoPacket))}); writeErr != nil {
					return writeErr
				}
				if _, writeErr := conn.Write(videoPacket); writeErr != nil {
					return writeErr
				}
			}
			return nil
		}
	}
}

func readRTSPRequest(reader *bufio.Reader) (string, string, map[string]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", "", nil, err
	}
	parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
	if len(parts) < 2 {
		return "", "", nil, errors.New("bad fixture request")
	}
	headers := map[string]string{}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			return "", "", nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			headers[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	return parts[0], parts[1], headers, nil
}

func interleavedForSetup(transport string) string {
	if value := strings.TrimPrefix(strings.Split(strings.TrimPrefix(transport, "RTP/AVP/TCP;unicast;interleaved="), ";")[0], ""); value != "" {
		return value
	}
	return "0-1"
}

func TestGo2RTCMediaSourceStubNegotiatesAndStreams(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var sourceName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ws" {
			http.NotFound(w, r)
			return
		}
		sourceName = r.URL.Query().Get("src")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var offer go2rtcMessage
		if jsonErr := json.Unmarshal(data, &offer); jsonErr != nil || offer.Type != "webrtc/offer" {
			return
		}
		mediaEngine := &webrtc.MediaEngine{}
		if err = mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1}, PayloadType: 0}, webrtc.RTPCodecTypeAudio); err != nil {
			return
		}
		if err = mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, PayloadType: 96}, webrtc.RTPCodecTypeVideo); err != nil {
			return
		}
		api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))
		pc, err := api.NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			return
		}
		defer pc.Close()
		connected := make(chan struct{})
		var connectedOnce sync.Once
		pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
			if state == webrtc.PeerConnectionStateConnected {
				connectedOnce.Do(func() { close(connected) })
			}
		})
		audio, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1}, "audio", "fixture")
		if _, err = pc.AddTrack(audio); err != nil {
			return
		}
		if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.Value}); err != nil {
			return
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			return
		}
		if err = pc.SetLocalDescription(answer); err != nil {
			return
		}
		select {
		case <-webrtc.GatheringCompletePromise(pc):
		case <-time.After(time.Second):
			return
		}
		local := pc.LocalDescription()
		if err = conn.WriteJSON(go2rtcMessage{Type: "webrtc/answer", Value: local.SDP}); err != nil {
			return
		}
		select {
		case <-connected:
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
			return
		}
		for i := 0; i < 5; i++ {
			if err = audio.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: uint16(i + 1), Timestamp: uint32(i * 160)}, Payload: []byte{0xff, 0x00, 0x7f}}); err != nil {
				return
			}
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	source, err := ParseMediaSource("go2rtc://" + u.Host + "/api/ws?src=tuya-main")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	frame, err := stream.ReadFrame(context.Background())
	if err != nil || len(frame.Samples) == 0 {
		t.Fatalf("frame = %#v, error = %v", frame, err)
	}
	if sourceName != "tuya-main" || stream.Capabilities.AudioCodec != "PCMU" || stream.Capabilities.SampleRate != 8000 || stream.Capabilities.Channels != 1 || stream.Capabilities.Video || !equalSamples(frame.Samples, []int16{0, -32124, 0}) {
		t.Fatalf("source/capabilities/frame = %q/%#v/%#v", sourceName, stream.Capabilities, frame.Samples)
	}
}

func TestGo2RTCVisualLookReturnsCopiedVideoAndPreservesAudio(t *testing.T) {
	rawURL, cleanup := startGo2RTCVisualFixture(t, true)
	defer cleanup()

	source, err := ParseMediaSource(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	before, err := stream.ReadFrame(readCtx)
	if err != nil || len(before.Samples) == 0 {
		t.Fatalf("audio before look = %#v, error = %v", before, err)
	}
	observation, err := stream.Look(readCtx)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Source != source.Identity() || observation.Status != VisualObservationAvailable || observation.Reason != "" || observation.MediaType != "video/H264" || !bytes.Equal(observation.Bytes, []byte{0x65, 1, 2, 3}) {
		t.Fatalf("visual observation = %#v", observation)
	}
	observation.Bytes[0] = 0
	after, err := stream.ReadFrame(readCtx)
	if err != nil || len(after.Samples) == 0 {
		t.Fatalf("audio after look = %#v, error = %v", after, err)
	}
	if err := stream.Close(); err != nil || stream.Close() != nil {
		t.Fatalf("idempotent stream close = %v", err)
	}
}

func TestGo2RTCAudioOnlyVisualLookIsUnavailableWithoutLosingAudio(t *testing.T) {
	rawURL, cleanup := startGo2RTCVisualFixture(t, false)
	defer cleanup()

	stream, err := OpenMediaSource(context.Background(), rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	before, err := stream.ReadFrame(readCtx)
	if err != nil || len(before.Samples) == 0 {
		t.Fatalf("audio-only frame before look = %#v, error = %v", before, err)
	}
	observation, err := stream.Look(readCtx)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack || len(observation.Bytes) != 0 || observation.MediaType != "" {
		t.Fatalf("audio-only visual observation = %#v", observation)
	}
	after, err := stream.ReadFrame(readCtx)
	if err != nil || len(after.Samples) == 0 {
		t.Fatalf("audio-only frame after look = %#v, error = %v", after, err)
	}
}

func TestVisualLookPreservesCallerDeadlineIdentityWhenTrackNeverAttaches(t *testing.T) {
	inbound := newPionInbound(nil, "go2rtc://fixture/api/ws?src=camera")
	inbound.setVideoNegotiated(true)
	defer inbound.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := inbound.Look(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked visual look error = %v, want context deadline", err)
	}
}

func TestVisualObservationAndLookAliasContracts(t *testing.T) {
	var nilContext context.Context
	available := VisualObservation{Status: VisualObservationAvailable, Bytes: []byte{1}}
	if !available.Available() {
		t.Fatal("non-empty available observation was not available")
	}
	for _, observation := range []VisualObservation{
		{Status: VisualObservationAvailable},
		{Status: VisualObservationUnavailable, Bytes: []byte{1}},
	} {
		if observation.Available() {
			t.Fatalf("observation without available visual data was available: %#v", observation)
		}
	}

	var nilStream *MediaStream
	observation, err := nilStream.Look(nilContext)
	if err != nil || observation.Source != "" || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack {
		t.Fatalf("nil stream look = %#v, error = %v", observation, err)
	}
	fallback := &MediaStream{Capabilities: MediaCapabilities{Source: "fallback-source"}}
	observation, err = fallback.Observe(nilContext)
	if err != nil || observation.Source != "fallback-source" || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack {
		t.Fatalf("fallback stream observe = %#v, error = %v", observation, err)
	}
	delegatedContext := context.WithValue(context.Background(), visualContextKey{}, "look")
	delegated := &MediaStream{look: func(ctx context.Context) (VisualObservation, error) {
		if ctx != delegatedContext {
			t.Fatalf("look callback context = %v, want caller context", ctx)
		}
		return available, nil
	}}
	if observation, err := delegated.Observe(delegatedContext); err != nil || !observation.Available() {
		t.Fatalf("delegated stream observe = %#v, error = %v", observation, err)
	}

	visualContext, cancel := boundedVisualContext(nilContext)
	if visualContext == nil {
		t.Fatal("nil visual context was not replaced")
	}
	cancel()
	if err := callerContextError(nilContext); err != nil {
		t.Fatalf("nil caller context error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(callerContextError(canceled), context.Canceled) {
		t.Fatalf("canceled caller context error = %v", callerContextError(canceled))
	}

	for name, look := range map[string]func(context.Context, string) (VisualObservation, error){
		"look media source":    LookMediaSource,
		"look source":          LookSource,
		"observe media source": ObserveMediaSource,
		"observe source":       ObserveSource,
	} {
		observation, err := look(context.Background(), "bad://source")
		if !errors.Is(err, ErrMalformedSource) || observation.Source != "" || observation.Status != "" || observation.Reason != "" || observation.MediaType != "" || len(observation.Bytes) != 0 {
			t.Fatalf("%s = %#v/%v", name, observation, err)
		}
	}
	invalid := MediaSource{identity: "invalid"}
	if _, err := invalid.Look(context.Background()); !errors.Is(err, ErrMalformedSource) {
		t.Fatalf("invalid source look error = %v", err)
	}
	if _, err := invalid.Observe(context.Background()); !errors.Is(err, ErrMalformedSource) {
		t.Fatalf("invalid source observe error = %v", err)
	}
}

func TestPionInboundLookHandlesNilTrackStates(t *testing.T) {
	var nilContext context.Context
	noTrack := newPionInbound(nil, "no-track")
	observation, err := noTrack.Look(nilContext)
	if err != nil || observation.Source != "no-track" || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack {
		t.Fatalf("nil-context no-track look = %#v, error = %v", observation, err)
	}
	noTrack.Close()

	waiting := newPionInbound(nil, "waiting")
	waiting.setVideoNegotiated(true)
	waiting.mu.Lock()
	waiting.videoMediaType = "video/H264"
	waiting.mu.Unlock()
	close(waiting.videoReady)
	waiting.visuals <- pionVisualFrame{}
	waiting.visuals <- pionVisualFrame{bytes: []byte{1, 2, 3}}
	observation, err = waiting.Look(context.Background())
	if err != nil || !observation.Available() || observation.Source != "waiting" || observation.MediaType != "video/H264" || !bytes.Equal(observation.Bytes, []byte{1, 2, 3}) {
		t.Fatalf("ready visual look = %#v, error = %v", observation, err)
	}
	waiting.Close()

	canceled := newPionInbound(nil, "canceled")
	canceled.setVideoNegotiated(true)
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := canceled.Look(canceledContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled visual look error = %v", err)
	}
	canceled.Close()

	closedBeforeAttach := newPionInbound(nil, "closed-before-attach")
	closedBeforeAttach.setVideoNegotiated(true)
	closedBeforeAttach.Close()
	if observation, err := closedBeforeAttach.Look(context.Background()); err != nil || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack {
		t.Fatalf("closed pre-attach visual look = %#v, error = %v", observation, err)
	}

	closedAfterAttach := newPionInbound(nil, "closed-after-attach")
	closedAfterAttach.mu.Lock()
	closedAfterAttach.videoNegotiated = true
	closedAfterAttach.videoSeen = true
	closedAfterAttach.mu.Unlock()
	closedAfterAttach.Close()
	if observation, err := closedAfterAttach.Look(context.Background()); err != nil || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack {
		t.Fatalf("closed attached visual look = %#v, error = %v", observation, err)
	}

	brokenRTSP := &rtspInbound{videoChannel: 2}
	if observation, err := brokenRTSP.Look(context.Background()); err != nil || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack {
		t.Fatalf("uninitialized RTSP visual look = %#v, error = %v", observation, err)
	}
}

func TestPionInboundLookReturnsUnavailableAfterObservationTimeout(t *testing.T) {
	inbound := newPionInbound(nil, "timeout")
	inbound.setVideoNegotiated(true)
	defer inbound.Close()
	started := time.Now()
	observation, err := inbound.Look(context.Background())
	if err != nil || observation.Source != "timeout" || observation.Status != VisualObservationUnavailable || observation.Reason != VisualObservationReasonNoVideoTrack || len(observation.Bytes) != 0 {
		t.Fatalf("timed-out visual look = %#v, error = %v", observation, err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("visual look exceeded its bound: %v", time.Since(started))
	}
}

func TestRTSPReadQueuesVideoForTheFollowingLook(t *testing.T) {
	audioPacket, err := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 1}, Payload: []byte{0x80, 0x00, 0x55}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	videoPacket, err := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: 2}, Payload: []byte{0x65, 1, 2, 3}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	interleaved := append([]byte{'$', 0, byte(len(audioPacket) >> 8), byte(len(audioPacket))}, audioPacket...)
	interleaved = append(interleaved, append([]byte{'$', 2, byte(len(videoPacket) >> 8), byte(len(videoPacket))}, videoPacket...)...)
	inbound := &rtspInbound{
		client:         &rtspClient{reader: bufio.NewReader(bytes.NewReader(interleaved))},
		audioChannel:   0,
		videoChannel:   2,
		codec:          "PCMU",
		videoMediaType: "video/H264",
		source:         "rtsp://fixture/camera",
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := inbound.ReadFrame(ctx)
	if err != nil || len(frame.Samples) == 0 {
		t.Fatalf("initial RTSP audio frame = %#v, error = %v", frame, err)
	}
	if _, err := inbound.ReadFrame(ctx); err == nil {
		t.Fatal("RTSP read after video unexpectedly returned a frame")
	}
	observation, err := inbound.Look(ctx)
	if err != nil || observation.Status != VisualObservationAvailable || observation.MediaType != "video/H264" || !bytes.Equal(observation.Bytes, []byte{0x65, 1, 2, 3}) {
		t.Fatalf("queued RTSP visual observation = %#v, error = %v", observation, err)
	}
}

func TestRTSPLookHonorsCancellationAndSkipsNonVideoPackets(t *testing.T) {
	canceled := &rtspInbound{videoChannel: 2}
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := canceled.Look(canceledContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled RTSP visual look error = %v", err)
	}

	emptyPacket, err := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 1}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	videoPacket, err := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: 2}, Payload: []byte{0x65, 4, 5, 6}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	interleaved := append([]byte{'$', 1, byte(len(emptyPacket) >> 8), byte(len(emptyPacket))}, emptyPacket...)
	interleaved = append(interleaved, append([]byte{'$', 2, byte(len(videoPacket) >> 8), byte(len(videoPacket))}, videoPacket...)...)
	inbound := &rtspInbound{
		client:         &rtspClient{reader: bufio.NewReader(bytes.NewReader(interleaved))},
		videoChannel:   2,
		videoMediaType: "video/H264",
		source:         "rtsp://fixture/camera",
	}
	observation, err := inbound.Look(context.Background())
	if err != nil || !observation.Available() || !bytes.Equal(observation.Bytes, []byte{0x65, 4, 5, 6}) {
		t.Fatalf("RTSP visual look after non-video packet = %#v, error = %v", observation, err)
	}
}

func TestPionInboundAttachIgnoresNilAndDuplicateAudio(t *testing.T) {
	inbound := newPionInbound(nil)
	inbound.attach(nil)
	inbound.attachAudio(nil)
	inbound.attachVideo(nil)
	inbound.mu.Lock()
	inbound.audioSeen = true
	inbound.mu.Unlock()
	inbound.attach(&webrtc.TrackRemote{})
	inbound.Close()
}

func startGo2RTCVisualFixture(t *testing.T, withVideo bool) (string, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	handlerDone := make(chan struct{})
	fixtureContext, cancelFixture := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		handlerContext, cancelHandler := context.WithCancel(r.Context())
		defer cancelHandler()
		go func() {
			select {
			case <-fixtureContext.Done():
				cancelHandler()
			case <-handlerContext.Done():
			}
		}()
		if r.URL.Path != "/api/ws" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var offer go2rtcMessage
		if err := json.Unmarshal(data, &offer); err != nil || offer.Type != "webrtc/offer" {
			return
		}
		mediaEngine := &webrtc.MediaEngine{}
		if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1}, PayloadType: 0}, webrtc.RTPCodecTypeAudio); err != nil {
			return
		}
		if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, PayloadType: 96}, webrtc.RTPCodecTypeVideo); err != nil {
			return
		}
		pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine)).NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			return
		}
		defer pc.Close()
		connected := make(chan struct{})
		var connectedOnce sync.Once
		pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
			if state == webrtc.PeerConnectionStateConnected {
				connectedOnce.Do(func() { close(connected) })
			}
		})
		audio, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1}, "audio", "visual-fixture")
		if err != nil {
			return
		}
		if _, err := pc.AddTrack(audio); err != nil {
			return
		}
		var video *webrtc.TrackLocalStaticRTP
		if withVideo {
			video, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "video", "visual-fixture")
			if err != nil {
				return
			}
			if _, err := pc.AddTrack(video); err != nil {
				return
			}
		}
		if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.Value}); err != nil {
			return
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			return
		}
		if err := pc.SetLocalDescription(answer); err != nil {
			return
		}
		select {
		case <-webrtc.GatheringCompletePromise(pc):
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
			return
		}
		local := pc.LocalDescription()
		if err := conn.WriteJSON(go2rtcMessage{Type: "webrtc/answer", Value: local.SDP}); err != nil {
			return
		}
		select {
		case <-connected:
		case <-handlerContext.Done():
			return
		case <-time.After(time.Second):
			return
		}
		for i := 0; i < 4; i++ {
			if err := audio.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: uint16(i + 1), Timestamp: uint32(i * 160)}, Payload: []byte{0xff, 0x00, 0x7f}}); err != nil {
				return
			}
			if video != nil {
				if err := video.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: uint16(i + 1), Timestamp: uint32(i * 3000)}, Payload: []byte{0x65, 1, 2, 3}}); err != nil {
					return
				}
			}
		}
		<-handlerContext.Done()
	}))
	u, err := url.Parse(server.URL)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	rawURL := "go2rtc://" + u.Host + "/api/ws?src=visual-fixture"
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cancelFixture()
			server.CloseClientConnections()
			server.Close()
			select {
			case <-handlerDone:
			case <-time.After(time.Second):
				t.Errorf("visual go2rtc fixture handler did not close")
			}
		})
	}
	return rawURL, cleanup
}

func TestDecodeAudioProducesNonEmptySamples(t *testing.T) {
	for _, codec := range []string{"PCMU", "PCMA", "L16", "opus"} {
		if got := decodeAudio(codec, []byte{0, 1, 2, 3}); len(got) == 0 {
			t.Errorf("decodeAudio(%q) returned no samples", codec)
		}
	}
}

func TestRTSPReadPacketStripsRTPHeaders(t *testing.T) {
	rtpBytes := []byte{
		0xb1, 0x00, 0x00, 0x01, // version, padding, extension, one CSRC; payload type and sequence
		0x00, 0x00, 0x00, 0x01, // timestamp
		0x00, 0x00, 0x00, 0x02, // SSRC
		0x00, 0x00, 0x00, 0x03, // CSRC
		0xbe, 0xde, 0x00, 0x01, // one four-byte extension word
		0x10, 0x01, 0x00, 0x00, // extension payload
		0x80, 0x00, 0x55, // distinctive PCMU payload
		0x00, 0x02, // two bytes of RTP padding
	}
	interleaved := append([]byte{'$', 0, byte(len(rtpBytes) >> 8), byte(len(rtpBytes))}, rtpBytes...)
	inbound := &rtspInbound{client: &rtspClient{reader: bufio.NewReader(bytes.NewReader(interleaved))}}
	channel, payload, err := inbound.readPacket()
	if err != nil {
		t.Fatal(err)
	}
	if channel != 0 || !bytes.Equal(payload, []byte{0x80, 0x00, 0x55}) {
		t.Fatalf("channel/payload = %d/%x", channel, payload)
	}
}

func TestMediaSourceAPIAndProtocolEdgeContracts(t *testing.T) {
	var nilContext context.Context
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

	inbound := newPionInbound(nil)
	ctx, cancelRead := context.WithCancel(context.Background())
	cancelRead()
	if _, err := inbound.ReadFrame(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Pion read = %v", err)
	}
	inbound.Close()
	if _, err := inbound.ReadFrame(nilContext); !errors.Is(err, io.EOF) {
		t.Fatalf("closed Pion read = %v", err)
	}
	pipeReader, pipeWriter := net.Pipe()
	defer pipeReader.Close()
	defer pipeWriter.Close()
	inboundStream := &rtspInbound{client: &rtspClient{conn: pipeReader, reader: bufio.NewReader(pipeReader)}}
	canceled, cancelRTSP := context.WithCancel(context.Background())
	cancelRTSP()
	if _, err := inboundStream.ReadFrame(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled RTSP read = %v", err)
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
	if got := decodeAudio("PCMU", nil); got != nil {
		t.Fatalf("empty audio decode = %v", got)
	}
	if got := decodeAudio("PCMA", []byte{0xd4}); len(got) != 1 || got[0] <= 0 {
		t.Fatalf("positive A-law decode = %v", got)
	}
}

func equalSamples(got, want []int16) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
