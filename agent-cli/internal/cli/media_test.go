package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestMediaProbeCommandRendersDeterministicCapabilityReport(t *testing.T) {
	var gotContext context.Context
	probe := func(ctx context.Context, raw string) (rtc.MediaCapabilities, error) {
		gotContext = ctx
		if raw != "rtsp://camera:secret@host:554/main" {
			t.Fatalf("probe URL = %q", raw)
		}
		return rtc.MediaCapabilities{Source: "rtsp://camera:<redacted>@host:554/main", AudioCodec: "PCMU", SampleRate: 8000, Channels: 1, Video: true}, nil
	}
	command := NewMediaProbeCommand(probe)
	command.Timeout = time.Second
	var out bytes.Buffer
	if err := command.Run(context.Background(), &out, "rtsp://camera:secret@host:554/main"); err != nil {
		t.Fatal(err)
	}
	if gotContext == nil {
		t.Fatal("probe did not receive a context")
	}
	want := "Source: rtsp://camera:<redacted>@host:554/main\nAudio codec: PCMU\nSample rate: 8000\nChannels: 1\nVideo presence: true\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
	if strings.Contains(out.String(), "secret") {
		t.Fatal("probe output leaked the credential")
	}
}

func TestMediaCommandRegistersProbeSubcommand(t *testing.T) {
	command := NewMediaCommand(func(context.Context, string) (rtc.MediaCapabilities, error) {
		return rtc.MediaCapabilities{}, nil
	}).Generate()
	probe, _, err := command.Find([]string{"probe"})
	if err != nil || probe == nil || probe.Use != "probe <url>" {
		t.Fatalf("probe command = %#v, error = %v", probe, err)
	}
}

func TestMediaProbeCommandPreservesTypedSourceError(t *testing.T) {
	want := &rtc.MediaSourceError{Kind: rtc.SourceErrorAuthentication, Source: "rtsp://camera:<redacted>@host:554/main"}
	command := NewMediaProbeCommand(func(context.Context, string) (rtc.MediaCapabilities, error) {
		return rtc.MediaCapabilities{}, want
	})
	err := command.Run(context.Background(), &bytes.Buffer{}, "rtsp://camera:secret@host:554/main")
	if !errors.Is(err, rtc.ErrSourceAuthentication) {
		t.Fatalf("error = %v, want authentication identity", err)
	}
	if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), rtc.RedactionMarker) {
		t.Fatalf("safe error = %v", err)
	}
}

func TestMediaProbeCommandRejectsIncompleteEvidence(t *testing.T) {
	command := NewMediaProbeCommand(func(context.Context, string) (rtc.MediaCapabilities, error) {
		return rtc.MediaCapabilities{Source: "stub"}, nil
	})
	if err := command.Run(context.Background(), &bytes.Buffer{}, "stub"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("error = %v, want incomplete evidence failure", err)
	}
}

func TestMediaProbeCommandRunsAgainstBothProtocolStubs(t *testing.T) {
	rtspURL, rtspObserved, rtspCleanup := startCLIRTSPFixture(t, "s14-rtsp-password")
	defer rtspCleanup()
	go2rtcURL, go2rtcObserved, go2rtcCleanup := startCLIGo2RTCFixture(t)
	defer go2rtcCleanup()

	cases := []struct {
		name     string
		raw      string
		identity string
	}{
		{name: "rtsp", raw: rtspURL},
		{name: "go2rtc", raw: go2rtcURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, err := rtc.ParseMediaSource(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			command := NewMediaProbeCommand(rtc.ProbeMediaSource).Generate()
			command.SetOut(&out)
			command.SetArgs([]string{tc.raw})
			if err := command.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("Source: %s\nAudio codec: PCMU\nSample rate: 8000\nChannels: 1\nVideo presence: true\n", source.Identity())
			if out.String() != want {
				t.Fatalf("probe output = %q, want %q", out.String(), want)
			}
		})
	}
	go2rtcObserved.waitFor(t, go2rtcObserved.frameDelivered, "go2rtc audio frame delivery")

	rtspObserved.Lock()
	gotMethods := append([]string(nil), rtspObserved.methods...)
	gotPaths := append([]string(nil), rtspObserved.paths...)
	frameSent := rtspObserved.frameSent
	rtspObserved.Unlock()
	if strings.Join(gotMethods, ",") != "DESCRIBE,DESCRIBE,SETUP,SETUP,PLAY" {
		t.Fatalf("RTSP methods = %v", gotMethods)
	}
	parsedRTSP, _ := url.Parse(rtspURL)
	parsedRTSP.User = nil
	baseURI := parsedRTSP.String()
	audioURI := *parsedRTSP
	audioURI.Path += "/trackID=0"
	audioURI.RawQuery = ""
	videoURI := *parsedRTSP
	videoURI.Path += "/trackID=1"
	videoURI.RawQuery = ""
	wantPaths := []string{baseURI, baseURI, audioURI.String(), videoURI.String(), baseURI}
	if len(gotPaths) != len(wantPaths) || !equalStringSlices(gotPaths, wantPaths) || !frameSent {
		t.Fatalf("RTSP paths/frame = %v/%t, want exact request URI and frame", gotPaths, frameSent)
	}
	go2rtcObserved.Lock()
	gotPath, gotSource, goFrameCount := go2rtcObserved.path, go2rtcObserved.source, go2rtcObserved.frameCount
	go2rtcObserved.Unlock()
	if gotPath != "/api/ws" || gotSource != "cli-tuya-main" || goFrameCount == 0 {
		t.Fatalf("go2rtc path/source/frame count = %q/%q/%d", gotPath, gotSource, goFrameCount)
	}
}

func TestMediaProbeCommandRejectsFrameLessGo2RTC(t *testing.T) {
	go2rtcURL, observed, cleanup := startCLIGo2RTCFixture(t, false)
	defer cleanup()

	command := NewMediaProbeCommand(rtc.ProbeMediaSource)
	command.Timeout = 250 * time.Millisecond
	var out bytes.Buffer
	err := command.Run(context.Background(), &out, go2rtcURL)
	if err == nil || !errors.Is(err, rtc.ErrSourceUnreachable) {
		t.Fatalf("frame-less probe error = %v, want typed unreachable error", err)
	}
	var typed *rtc.MediaSourceError
	if !errors.As(err, &typed) || typed.Kind != rtc.SourceErrorUnreachable {
		t.Fatalf("frame-less probe error = %v, want *MediaSourceError with unreachable kind", err)
	}
	if out.Len() != 0 {
		t.Fatalf("frame-less probe report = %q, want no successful capability report", out.String())
	}
	observed.waitFor(t, observed.negotiated, "go2rtc negotiation")

	observed.Lock()
	gotPath, gotSource, frameCount := observed.path, observed.source, observed.frameCount
	observed.Unlock()
	if gotPath != "/api/ws" || gotSource != "cli-tuya-main" || frameCount != 0 {
		t.Fatalf("frame-less path/source/frame count = %q/%q/%d", gotPath, gotSource, frameCount)
	}
}

func TestMediaLookRootArgvReportsCameraObservation(t *testing.T) {
	rawURL, observed, cleanup := startCLIGo2RTCFixture(t)
	defer cleanup()

	root := newTestRootCommand()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"media", "look", rawURL})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("root media look error = %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("root media look stderr = %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Source: "+rawURL) || !strings.Contains(stdout.String(), "Look status: available") || !strings.Contains(stdout.String(), "Media type: video/H264") || !strings.Contains(stdout.String(), "Observation bytes: 4") {
		t.Fatalf("camera look output = %q", stdout.String())
	}
	observed.waitFor(t, observed.videoFrameDelivered, "go2rtc video frame delivery")
}

func TestMediaLookRootArgvReportsAudioOnlyUnavailable(t *testing.T) {
	rawURL, _, cleanup := startCLIGo2RTCFixtureWithMedia(t, true, false)
	defer cleanup()

	root := newTestRootCommand()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"media", "look", rawURL})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("root audio-only media look error = %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("root audio-only media look stderr = %q", stderr.String())
	}
	want := "Look status: unavailable\nReason: no_video_track\n"
	if !strings.Contains(stdout.String(), want) || strings.Contains(stdout.String(), "Observation bytes:") || strings.Contains(stdout.String(), "Media type:") {
		t.Fatalf("audio-only look output = %q", stdout.String())
	}
}

func TestMediaProbeRootArgvReportsNegotiatedCameraCapabilities(t *testing.T) {
	rawURL, _, cleanup := startCLIGo2RTCFixture(t)
	defer cleanup()

	root := newTestRootCommand()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"media", "probe", rawURL})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("root media probe error = %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("root media probe stderr = %q", stderr.String())
	}
	want := "Source: " + rawURL + "\nAudio codec: PCMU\nSample rate: 8000\nChannels: 1\nVideo presence: true\n"
	if stdout.String() != want {
		t.Fatalf("root media probe output = %q, want %q", stdout.String(), want)
	}
}

func TestMediaProbeCredentialRedactionAcrossArtifacts(t *testing.T) {
	const password = "s6-distinctive-password"
	rawURL, _, cleanupCLI := startCLIRTSPFixture(t, password)
	var cliOutput bytes.Buffer
	if err := NewMediaProbeCommand(rtc.ProbeMediaSource).Run(context.Background(), &cliOutput, rawURL); err != nil {
		cleanupCLI()
		t.Fatal(err)
	}
	cleanupCLI()

	mediaURL, _, cleanupMedia := startCLIRTSPFixture(t, password)
	source, err := rtc.ParseMediaSource(mediaURL)
	if err != nil {
		cleanupMedia()
		t.Fatal(err)
	}
	stream, err := source.Open(context.Background())
	if err != nil {
		cleanupMedia()
		t.Fatal(err)
	}
	frame, err := stream.ReadFrame(context.Background())
	caps := stream.Capabilities
	_ = stream.Close()
	cleanupMedia()
	if err != nil || len(frame.Samples) == 0 {
		t.Fatalf("media frame = %#v, error = %v", frame, err)
	}

	_, safeError := rtc.ProbeMediaSource(context.Background(), "rtsp://camera:"+password+"@127.0.0.1:1/error")
	if safeError == nil || strings.Contains(safeError.Error(), password) || !strings.Contains(safeError.Error(), rtc.RedactionMarker) {
		t.Fatalf("captured source error = %v", safeError)
	}

	root := t.TempDir()
	logs := filepath.Join(root, "captured")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	signalText := fmt.Sprintf("source=%s codec=%s rate=%d channels=%d video=%t frame_samples=%v\n", caps.Source, caps.AudioCodec, caps.SampleRate, caps.Channels, caps.Video, frame.Samples)
	if err := os.WriteFile(filepath.Join(logs, "agent.log"), []byte(signalText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "cli.txt"), cliOutput.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "error.txt"), []byte(safeError.Error()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := transcript.WriteRecordingBundle(transcript.RecordingConfig{
		Destination:      filepath.Join(root, "recording"),
		ClientTranscript: []byte(fmt.Sprintf("{\"source\":%q,\"frame\":%v}\n", mediaURL, frame.Samples)),
		AgentTranscript:  []byte(fmt.Sprintf("{\"source\":%q,\"capability\":%q}\n", mediaURL, caps.AudioCodec)),
		InputSegments:    [][]byte{pcmBytes(frame.Samples)},
		OutputSegments:   [][]byte{{1, 2, 3}},
		Credentials:      []string{password},
		Metadata: transcript.RecordingMetadata{
			MediaSource:   &transcript.MediaSourceMetadata{URL: mediaURL, Protocol: "rtsp", Name: "front-door"},
			Configuration: map[string]string{"source": mediaURL, "codec": caps.AudioCodec},
		},
	}); err != nil {
		t.Fatal(err)
	}

	var sawMarker, sawHost, sawSignal bool
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(password)) {
			t.Errorf("%s contains the source password", path)
		}
		if bytes.Contains(data, []byte(rtc.RedactionMarker)) || bytes.Contains(data, []byte(transcript.RecordingRedactionMarker)) {
			sawMarker = true
		}
		if bytes.Contains(data, []byte("127.0.0.1")) {
			sawHost = true
		}
		if bytes.Contains(data, []byte("32124")) {
			sawSignal = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !sawMarker || !sawHost || !sawSignal {
		t.Fatalf("artifact evidence marker/host/signal = %t/%t/%t", sawMarker, sawHost, sawSignal)
	}
}

type cliRTSPObservation struct {
	sync.Mutex
	methods   []string
	paths     []string
	frameSent bool
}

func startCLIRTSPFixture(t *testing.T, password string) (string, *cliRTSPObservation, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	observed := &cliRTSPObservation{}
	done := make(chan error, 1)
	go func() { done <- serveCLIRTSPFixture(listener, observed, password) }()
	rawURL := fmt.Sprintf("rtsp://camera:%s@%s/camera/stream?profile=main", password, listener.Addr())
	cleanup := func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Errorf("RTSP fixture did not close")
		}
	}
	return rawURL, observed, cleanup
}

func serveCLIRTSPFixture(listener net.Listener, observed *cliRTSPObservation, password string) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	challenged := false
	for {
		method, path, headers, err := readCLIRTSPRequest(reader)
		if err != nil {
			return err
		}
		observed.Lock()
		observed.methods = append(observed.methods, method)
		observed.paths = append(observed.paths, path)
		observed.Unlock()
		switch method {
		case "DESCRIBE":
			if !challenged {
				challenged = true
				fmt.Fprint(conn, "RTSP/1.0 401 Unauthorized\r\nCSeq: "+headers["cseq"]+"\r\nWWW-Authenticate: Basic realm=cli-fixture\r\nContent-Length: 0\r\n\r\n")
				continue
			}
			wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("camera:"+password))
			if headers["authorization"] != wantAuth {
				fmt.Fprint(conn, "RTSP/1.0 401 Unauthorized\r\nCSeq: "+headers["cseq"]+"\r\nContent-Length: 0\r\n\r\n")
				continue
			}
			body := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=cli-fixture\r\nt=0 0\r\nm=audio 0 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000/1\r\na=control:trackID=0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\na=control:trackID=1\r\n"
			fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nContent-Base: %s\r\nContent-Length: %d\r\n\r\n%s", headers["cseq"], path, len(body), body)
		case "SETUP":
			transport := strings.TrimPrefix(headers["transport"], "RTP/AVP/TCP;unicast;interleaved=")
			transport = strings.Split(transport, ";")[0]
			fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: cli-session\r\nTransport: RTP/AVP/TCP;unicast;interleaved=%s\r\nContent-Length: 0\r\n\r\n", headers["cseq"], transport)
		case "PLAY":
			fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: cli-session\r\nContent-Length: 0\r\n\r\n", headers["cseq"])
			packet, err := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 1, Timestamp: 1}, Payload: []byte{0x80, 0x00, 0x55}}).Marshal()
			if err != nil {
				return err
			}
			observed.Lock()
			observed.frameSent = true
			observed.Unlock()
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

func readCLIRTSPRequest(reader *bufio.Reader) (string, string, map[string]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", "", nil, err
	}
	parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
	if len(parts) < 2 {
		return "", "", nil, errors.New("invalid CLI fixture request")
	}
	headers := map[string]string{}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			return "", "", nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return parts[0], parts[1], headers, nil
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			headers[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
}

type cliGo2RTCObservation struct {
	sync.Mutex
	path, source                        string
	frameCount, videoFrameCount         int
	negotiated                          chan struct{}
	frameDelivered, videoFrameDelivered chan struct{}
	negotiatedOnce, frameOnce           sync.Once
	videoFrameOnce                      sync.Once
}

func startCLIGo2RTCFixture(t *testing.T, sendFrames ...bool) (string, *cliGo2RTCObservation, func()) {
	t.Helper()
	frames := true
	if len(sendFrames) > 0 {
		frames = sendFrames[0]
	}
	return startCLIGo2RTCFixtureWithMedia(t, frames, frames)
}

func startCLIGo2RTCFixtureWithMedia(t *testing.T, sendAudio, sendVideo bool) (string, *cliGo2RTCObservation, func()) {
	t.Helper()
	observed := &cliGo2RTCObservation{
		negotiated:          make(chan struct{}),
		frameDelivered:      make(chan struct{}),
		videoFrameDelivered: make(chan struct{}),
	}
	handlerDone := make(chan struct{})
	fixtureContext, cancelFixture := context.WithCancel(context.Background())
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
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
		observed.Lock()
		observed.path = r.URL.Path
		observed.source = r.URL.Query().Get("src")
		observed.Unlock()
		if r.URL.Path != "/api/ws" {
			w.WriteHeader(http.StatusNotFound)
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
		var offer struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}
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
		var once sync.Once
		pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
			if state == webrtc.PeerConnectionStateConnected {
				once.Do(func() { close(connected) })
			}
		})
		audio, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1}, "audio", "cli-fixture")
		if err != nil {
			return
		}
		if _, err = pc.AddTrack(audio); err != nil {
			return
		}
		var video *webrtc.TrackLocalStaticRTP
		if sendVideo {
			video, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "video", "cli-fixture")
			if err != nil {
				return
			}
			if _, err = pc.AddTrack(video); err != nil {
				return
			}
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
		case <-handlerContext.Done():
			return
		}
		if err = conn.WriteJSON(struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}{Type: "webrtc/answer", Value: pc.LocalDescription().SDP}); err != nil {
			return
		}
		observed.negotiatedOnce.Do(func() { close(observed.negotiated) })
		select {
		case <-connected:
		case <-handlerContext.Done():
			return
		}
		for i := 0; i < 3; i++ {
			if sendAudio {
				packet := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: uint16(i + 1), Timestamp: uint32(i * 160)}, Payload: []byte{0xff, 0x00, 0x7f}}
				if err = audio.WriteRTP(packet); err != nil {
					return
				}
				observed.recordFrame(len(packet.Payload))
			}
			if sendVideo {
				packet := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: uint16(i + 1), Timestamp: uint32(i * 3000)}, Payload: []byte{0x65, byte(i + 1), 0x01, 0x02}}
				if err = video.WriteRTP(packet); err != nil {
					return
				}
				observed.recordVideoFrame(len(packet.Payload))
			}
		}
		<-handlerContext.Done()
	}))
	u, _ := url.Parse(server.URL)
	rawURL := "go2rtc://" + u.Host + "/api/ws?src=cli-tuya-main"
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cancelFixture()
			server.CloseClientConnections()
			closed := make(chan struct{})
			go func() {
				server.Close()
				close(closed)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			select {
			case <-closed:
			case <-ctx.Done():
				t.Errorf("go2rtc fixture server did not close: %v", ctx.Err())
			}
			select {
			case <-handlerDone:
			case <-ctx.Done():
				t.Errorf("go2rtc fixture handler did not close: %v", ctx.Err())
			}
		})
	}
	return rawURL, observed, cleanup
}

func (o *cliGo2RTCObservation) recordFrame(payloadSize int) {
	if payloadSize == 0 {
		return
	}
	o.Lock()
	o.frameCount++
	o.Unlock()
	o.frameOnce.Do(func() { close(o.frameDelivered) })
}

func (o *cliGo2RTCObservation) recordVideoFrame(payloadSize int) {
	if payloadSize == 0 {
		return
	}
	o.Lock()
	o.videoFrameCount++
	o.Unlock()
	o.videoFrameOnce.Do(func() { close(o.videoFrameDelivered) })
}

func (o *cliGo2RTCObservation) waitFor(t *testing.T, event <-chan struct{}, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-event:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s: %v", name, ctx.Err())
	}
}

func pcmBytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(sample))
	}
	return data
}

func equalStringSlices(got, want []string) bool {
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
