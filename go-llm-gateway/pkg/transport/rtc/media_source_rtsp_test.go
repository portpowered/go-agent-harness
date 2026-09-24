package rtc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
	audiocodec "github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func TestRTSPMediaSourceStubNegotiatesAndStreams(t *testing.T) {
	secret := "rtsp-" + "marker"
	var observed rtspFixtureObservation
	addr, serverDone := startRTSPFixture(t, &observed, secret)

	source, err := ParseMediaSource(fmt.Sprintf("rtsp://camera:%s@%s/camera/main", secret, addr.String()))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer requireClosed(t, "stream", stream)
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
	requireClosed(t, "stream", stream)
	awaitFixtureDone(t, serverDone, "RTSP fixture")
}

func TestRTSPReadRemainsUsableAfterSetupContextCompletes(t *testing.T) {
	frameGate := make(chan struct{})
	var observed rtspFixtureObservation
	addr, serverDone := startRTSPFixture(t, &observed, "deadline-password", rtspFixtureOptions{challengeAuth: true, frameGate: frameGate})

	source, err := ParseMediaSource(fmt.Sprintf("rtsp://camera:deadline-password@%s/camera/main", addr))
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
	defer requireClosed(t, "stream", stream)

	timer := time.NewTimer(time.Until(deadline) + 30*time.Millisecond)
	<-timer.C
	close(frameGate)
	frameCtx, frameCancel := context.WithTimeout(context.Background(), time.Second)
	defer frameCancel()
	frame, err := stream.ReadFrame(frameCtx)
	if err != nil || !equalSamples(frame.Samples, []int16{32124, -32124, -716}) {
		t.Fatalf("post-setup frame = %#v, error = %v", frame, err)
	}
	awaitFixtureDone(t, serverDone, "deadline regression fixture")
}

func TestRTSPVisualLookQueuesAudioAndReturnsCopiedVideo(t *testing.T) {
	var observed rtspFixtureObservation
	addr, serverDone := startRTSPFixture(t, &observed, "", rtspFixtureOptions{
		challengeAuth: false,
		videoPayload:  []byte{0x65, 4, 5, 6},
	})

	source, err := ParseMediaSource(fmt.Sprintf("rtsp://%s/camera/main", addr))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer requireClosed(t, "stream", stream)
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
	awaitFixtureDone(t, serverDone, "RTSP visual fixture")
}

func TestLookMediaSourceUsesPublicRTSPContract(t *testing.T) {
	var observed rtspFixtureObservation
	addr, serverDone := startRTSPFixture(t, &observed, "", rtspFixtureOptions{
		challengeAuth: false,
		videoPayload:  []byte{0x65, 7, 8, 9},
	})

	rawURL := fmt.Sprintf("rtsp://%s/camera/public-look", addr)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observation, err := LookMediaSource(ctx, rawURL)
	if err != nil || observation.Source != rawURL || !observation.Available() || observation.MediaType != "video/H264" || !bytes.Equal(observation.Bytes, []byte{0x65, 7, 8, 9}) {
		t.Fatalf("public RTSP look = %#v, error = %v", observation, err)
	}
	awaitFixtureDone(t, serverDone, "public RTSP look fixture")
}

func TestRTSPAudioOnlyVisualLookIsUnavailableAndAudioRemainsUsable(t *testing.T) {
	var observed rtspFixtureObservation
	addr, serverDone := startRTSPFixture(t, &observed, "", rtspFixtureOptions{
		body:          "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=audio-only\r\nt=0 0\r\nm=audio 0 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000/1\r\na=control:trackID=0\r\n",
		challengeAuth: false,
	})

	source, err := ParseMediaSource(fmt.Sprintf("rtsp://%s/audio-only", addr))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer requireClosed(t, "stream", stream)
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
	awaitFixtureDone(t, serverDone, "audio-only RTSP fixture")
}

// rtspLengthHighShift selects the high byte of an interleaved frame length.
const rtspLengthHighShift = 8

// rtspFixtureSession answers one client connection for serveRTSPFixture.
type rtspFixtureSession struct {
	conn       net.Conn
	config     rtspFixtureOptions
	secret     string
	challenged bool
}

func serveRTSPFixture(listener net.Listener, observed *rtspFixtureObservation, secret string, options ...rtspFixtureOptions) (err error) {
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
	defer func() {
		if closeErr := conn.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	session := &rtspFixtureSession{conn: conn, config: config, secret: secret}
	reader := bufio.NewReader(conn)
	for {
		method, path, headers, err := readRTSPRequest(reader)
		if err != nil {
			return err
		}
		observed.record(method, path, headers)
		switch method {
		case "DESCRIBE":
			err = session.describe(path, headers)
		case "SETUP":
			err = session.write("RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: fixture-session\r\nTransport: RTP/AVP/TCP;unicast;interleaved=%s\r\nContent-Length: 0\r\n\r\n", headers["cseq"], interleavedForSetup(headers["transport"]))
		case "PLAY":
			return session.play(headers)
		}
		if err != nil {
			return err
		}
	}
}

func (o *rtspFixtureObservation) record(method, path string, headers map[string]string) {
	o.Lock()
	defer o.Unlock()
	o.methods = append(o.methods, method)
	o.paths = append(o.paths, path)
	o.path = path
	o.auth = headers["authorization"]
}

func (f *rtspFixtureSession) write(format string, args ...any) error {
	_, err := fmt.Fprintf(f.conn, format, args...)
	return err
}

func (f *rtspFixtureSession) describe(path string, headers map[string]string) error {
	if f.config.challengeAuth && !f.challenged {
		f.challenged = true
		return f.write("RTSP/1.0 401 Unauthorized\r\nCSeq: 1\r\nWWW-Authenticate: Basic realm=fixture\r\nContent-Length: 0\r\n\r\n")
	}
	if f.config.challengeAuth && headers["authorization"] != "Basic "+base64.StdEncoding.EncodeToString([]byte("camera:"+f.secret)) {
		if f.config.requireAuth {
			return f.write("RTSP/1.0 401 Unauthorized\r\nCSeq: %s\r\nWWW-Authenticate: Basic realm=fixture\r\nContent-Length: 0\r\n\r\n", headers["cseq"])
		}
		return errors.New("fixture did not receive expected authorization")
	}
	return f.write("RTSP/1.0 200 OK\r\nCSeq: %s\r\nContent-Base: %s\r\nContent-Length: %d\r\n\r\n%s", headers["cseq"], path, len(f.config.body), f.config.body)
}

func (f *rtspFixtureSession) play(headers map[string]string) error {
	if err := f.write("RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: fixture-session\r\nContent-Length: 0\r\n\r\n", headers["cseq"]); err != nil {
		return err
	}
	if f.config.frameGate != nil {
		<-f.config.frameGate
	}
	audio := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: 1, Timestamp: 1}, Payload: []byte{0x80, 0x00, 0x55}}
	if err := f.writeInterleaved(0, audio); err != nil {
		return err
	}
	if len(f.config.videoPayload) == 0 {
		return nil
	}
	video := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: 2, Timestamp: 3000}, Payload: f.config.videoPayload}
	return f.writeInterleaved(2, video)
}

func (f *rtspFixtureSession) writeInterleaved(channel byte, packet *rtp.Packet) error {
	payload, err := packet.Marshal()
	if err != nil {
		return err
	}
	if _, err := f.conn.Write([]byte{'$', channel, byte(len(payload) >> rtspLengthHighShift), byte(len(payload))}); err != nil {
		return err
	}
	_, err = f.conn.Write(payload)
	return err
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
