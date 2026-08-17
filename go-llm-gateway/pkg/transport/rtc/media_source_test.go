package rtc

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	if !errors.Is(err, ErrSourceUnreachable) || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("unreachable error = %v", err)
	}
}

func TestRTSPMediaSourceStubNegotiatesAndStreams(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	secret := "rtsp-" + "marker"
	var observed struct {
		sync.Mutex
		methods []string
		path    string
		auth    string
	}
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
	if err != nil || len(frame.Samples) == 0 || frame.Samples[0] == 0 {
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

func serveRTSPFixture(listener net.Listener, observed *struct {
	sync.Mutex
	methods []string
	path    string
	auth    string
}, secret string) error {
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
		observed.path = path
		observed.auth = headers["authorization"]
		observed.Unlock()
		switch method {
		case "DESCRIBE":
			if !challenge {
				challenge = true
				fmt.Fprint(conn, "RTSP/1.0 401 Unauthorized\r\nCSeq: 1\r\nWWW-Authenticate: Basic realm=fixture\r\nContent-Length: 0\r\n\r\n")
				continue
			}
			if headers["authorization"] != "Basic "+base64.StdEncoding.EncodeToString([]byte("camera:"+secret)) {
				return errors.New("fixture did not receive expected authorization")
			}
			body := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=fixture\r\nt=0 0\r\nm=audio 0 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000/1\r\na=control:trackID=0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=control:trackID=1\r\n"
			fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nContent-Base: %s\r\nContent-Length: %d\r\n\r\n%s", headers["cseq"], path, len(body), body)
		case "SETUP":
			fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: fixture-session\r\nTransport: RTP/AVP/TCP;unicast;interleaved=%s\r\nContent-Length: 0\r\n\r\n", headers["cseq"], interleavedForSetup(headers["transport"]))
		case "PLAY":
			fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: fixture-session\r\nContent-Length: 0\r\n\r\n", headers["cseq"])
			packet, err := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 1, Timestamp: 1}, Payload: []byte{0x00, 0x7f, 0xff}}).Marshal()
			if err != nil {
				return err
			}
			if _, err := conn.Write([]byte{'$', 0, byte(len(packet) >> 8), byte(len(packet))}); err != nil {
				return err
			}
			if _, err := conn.Write(packet); err != nil {
				return err
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
		api := webrtc.NewAPI()
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
	if sourceName != "tuya-main" || stream.Capabilities.AudioCodec == "" || stream.Capabilities.SampleRate <= 0 || stream.Capabilities.Channels <= 0 {
		t.Fatalf("source/capabilities = %q/%#v", sourceName, stream.Capabilities)
	}
}

func TestDecodeAudioProducesNonEmptySamples(t *testing.T) {
	for _, codec := range []string{"PCMU", "PCMA", "L16", "opus"} {
		if got := decodeAudio(codec, []byte{0, 1, 2, 3}); len(got) == 0 {
			t.Errorf("decodeAudio(%q) returned no samples", codec)
		}
	}
}
