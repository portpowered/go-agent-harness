package rtc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
	audiocodec "github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

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

func TestDecodeAudioProducesNonEmptySamples(t *testing.T) {
	for _, codecName := range []string{"PCMU", "PCMA", "L16", "opus"} {
		if got := audiocodec.DecodeRTPAudioPayload(codecName, []byte{0, 1, 2, 3}); len(got) == 0 {
			t.Errorf("DecodeRTPAudioPayload(%q) returned no samples", codecName)
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
	if got := audiocodec.DecodeRTPAudioPayload("PCMU", nil); got != nil {
		t.Fatalf("empty audio decode = %v", got)
	}
	if got := audiocodec.DecodeRTPAudioPayload("PCMA", []byte{0xd4}); len(got) != 1 || got[0] <= 0 {
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
