package rtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const (
	go2rtcFixtureSignalingPath   = "/api/ws"
	go2rtcFixturePCMUClockRate   = 8000
	go2rtcFixtureH264ClockRate   = 90000
	go2rtcFixtureH264PayloadType = 96
	go2rtcFixtureAudioTicks      = 160
	go2rtcFixtureVideoTicks      = 3000
	go2rtcFixtureStepTimeout     = time.Second
	go2rtcNegotiatedAudioPackets = 5
	go2rtcVisualFixturePackets   = 4
)

func TestGo2RTCMediaSourceStubNegotiatesAndStreams(t *testing.T) {
	fixture := startGo2RTCFixture(t, go2rtcFixtureOptions{source: "tuya-main", trackStream: "fixture", packets: go2rtcNegotiatedAudioPackets})
	defer fixture.cleanup()
	source, err := ParseMediaSource(fixture.rawURL)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer requireClosed(t, "stream", stream)
	frame, err := stream.ReadFrame(context.Background())
	if err != nil || len(frame.Samples) == 0 {
		t.Fatalf("frame = %#v, error = %v", frame, err)
	}
	sourceName := fixture.requestedSource()
	if sourceName != "tuya-main" || stream.Capabilities.AudioCodec != go2rtcCodecPCMU || stream.Capabilities.SampleRate != 8000 || stream.Capabilities.Channels != 1 || stream.Capabilities.Video || !equalSamples(frame.Samples, []int16{0, -32124, 0}) {
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
	defer requireClosed(t, "stream", stream)
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
	if observation.Source != source.Identity() || observation.Status != VisualObservationAvailable || observation.Reason != "" || observation.MediaType != testVideoH264MimeType || !bytes.Equal(observation.Bytes, []byte{0x65, 1, 2, 3}) {
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
	defer requireClosed(t, "stream", stream)
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

func startGo2RTCVisualFixture(t *testing.T, withVideo bool) (string, func()) {
	t.Helper()
	fixture := startGo2RTCFixture(t, go2rtcFixtureOptions{source: "visual-fixture", trackStream: "visual-fixture", withVideo: withVideo, packets: go2rtcVisualFixturePackets})
	return fixture.rawURL, fixture.cleanup
}

// go2rtcFixtureOptions shapes the answering go2rtc stub: the src query it is
// reached through, the stream id of its local tracks, whether it offers H264
// video beside PCMU audio, and how many RTP packets it sends per track.
type go2rtcFixtureOptions struct {
	source      string
	trackStream string
	withVideo   bool
	packets     int
}

// go2rtcFixture is a single-request go2rtc signaling stub backed by a Pion
// peer connection. Handler-side close failures are collected and reported by
// cleanup after the handler has returned.
type go2rtcFixture struct {
	t              *testing.T
	options        go2rtcFixtureOptions
	rawURL         string
	server         *httptest.Server
	upgrader       websocket.Upgrader
	handlerDone    chan struct{}
	fixtureContext context.Context
	cancelFixture  context.CancelFunc
	cleanupOnce    sync.Once

	mu         sync.Mutex
	source     string
	closeFails []error
}

// go2rtcFixturePeer is the answering peer connection and its local tracks.
type go2rtcFixturePeer struct {
	pc        *webrtc.PeerConnection
	audio     *webrtc.TrackLocalStaticRTP
	video     *webrtc.TrackLocalStaticRTP
	connected chan struct{}
}

func startGo2RTCFixture(t *testing.T, options go2rtcFixtureOptions) *go2rtcFixture {
	t.Helper()
	fixtureContext, cancelFixture := context.WithCancel(context.Background())
	fixture := &go2rtcFixture{
		t:              t,
		options:        options,
		upgrader:       websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		handlerDone:    make(chan struct{}),
		fixtureContext: fixtureContext,
		cancelFixture:  cancelFixture,
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serve))
	u, err := url.Parse(fixture.server.URL)
	if err != nil {
		fixture.cleanup()
		t.Fatal(err)
	}
	fixture.rawURL = "go2rtc://" + u.Host + "/api/ws?src=" + options.source
	return fixture
}

func (f *go2rtcFixture) cleanup() {
	f.cleanupOnce.Do(func() {
		f.cancelFixture()
		f.server.CloseClientConnections()
		f.server.Close()
		select {
		case <-f.handlerDone:
		case <-time.After(time.Second):
			f.t.Errorf("go2rtc fixture handler did not close")
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, err := range f.closeFails {
			f.t.Errorf("go2rtc fixture close: %v", err)
		}
	})
}

func (f *go2rtcFixture) requestedSource() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.source
}

// recordClose closes a handler-owned resource. The handler cannot fail the
// test directly, so unexpected close failures are reported by cleanup.
func (f *go2rtcFixture) recordClose(name string, closer io.Closer) {
	if err := closer.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		f.mu.Lock()
		f.closeFails = append(f.closeFails, fmt.Errorf("%s: %w", name, err))
		f.mu.Unlock()
	}
}

func (f *go2rtcFixture) serve(w http.ResponseWriter, r *http.Request) {
	defer close(f.handlerDone)
	handlerContext, cancelHandler := context.WithCancel(r.Context())
	defer cancelHandler()
	go func() {
		select {
		case <-f.fixtureContext.Done():
			cancelHandler()
		case <-handlerContext.Done():
		}
	}()
	if r.URL.Path != go2rtcFixtureSignalingPath {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	f.source = r.URL.Query().Get("src")
	f.mu.Unlock()
	conn, err := f.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer f.recordClose("websocket", conn)
	peer, err := f.answer(r.Context(), conn)
	if err != nil {
		return
	}
	defer f.recordClose("peer connection", peer.pc)
	f.stream(handlerContext, peer)
}

// answer reads the client offer and replies with the stub's SDP answer once
// ICE gathering completes.
func (f *go2rtcFixture) answer(ctx context.Context, conn *websocket.Conn) (*go2rtcFixturePeer, error) {
	_, data, err := conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	var offer go2rtcMessage
	if err := json.Unmarshal(data, &offer); err != nil {
		return nil, err
	}
	if offer.Type != "webrtc/offer" {
		return nil, fmt.Errorf("unexpected go2rtc message %q", offer.Type)
	}
	peer, err := f.newPeer()
	if err != nil {
		return nil, err
	}
	if err := peer.negotiate(ctx, conn, offer.Value); err != nil {
		f.recordClose("peer connection", peer.pc)
		return nil, err
	}
	return peer, nil
}

func (f *go2rtcFixture) newPeer() (*go2rtcFixturePeer, error) {
	pcmu := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: go2rtcFixturePCMUClockRate, Channels: 1}
	h264 := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: go2rtcFixtureH264ClockRate}
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: pcmu, PayloadType: 0}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: h264, PayloadType: go2rtcFixtureH264PayloadType}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}
	peer := &go2rtcFixturePeer{pc: pc, connected: make(chan struct{})}
	if err := peer.addTracks(f.options, pcmu, h264); err != nil {
		f.recordClose("peer connection", pc)
		return nil, err
	}
	return peer, nil
}

func (p *go2rtcFixturePeer) addTracks(options go2rtcFixtureOptions, pcmu, h264 webrtc.RTPCodecCapability) error {
	var connectedOnce sync.Once
	p.pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			connectedOnce.Do(func() { close(p.connected) })
		}
	})
	audio, err := webrtc.NewTrackLocalStaticRTP(pcmu, sdpMediaAudio, options.trackStream)
	if err != nil {
		return err
	}
	if _, err := p.pc.AddTrack(audio); err != nil {
		return err
	}
	p.audio = audio
	if !options.withVideo {
		return nil
	}
	video, err := webrtc.NewTrackLocalStaticRTP(h264, sdpMediaVideo, options.trackStream)
	if err != nil {
		return err
	}
	if _, err := p.pc.AddTrack(video); err != nil {
		return err
	}
	p.video = video
	return nil
}

func (p *go2rtcFixturePeer) negotiate(ctx context.Context, conn *websocket.Conn, offerSDP string) error {
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		return err
	}
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return err
	}
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return err
	}
	select {
	case <-webrtc.GatheringCompletePromise(p.pc):
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(go2rtcFixtureStepTimeout):
		return errors.New("go2rtc fixture ICE gathering timed out")
	}
	return conn.WriteJSON(go2rtcMessage{Type: "webrtc/answer", Value: p.pc.LocalDescription().SDP})
}

// stream waits for the peer connection, sends the configured RTP packets,
// and then holds the connection open until the fixture or request ends.
func (f *go2rtcFixture) stream(ctx context.Context, peer *go2rtcFixturePeer) {
	select {
	case <-peer.connected:
	case <-ctx.Done():
		return
	case <-time.After(go2rtcFixtureStepTimeout):
		return
	}
	for i := 0; i < f.options.packets; i++ {
		if err := peer.audio.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: uint16(i + 1), Timestamp: uint32(i * go2rtcFixtureAudioTicks)}, Payload: []byte{0xff, 0x00, 0x7f}}); err != nil {
			return
		}
		if peer.video == nil {
			continue
		}
		if err := peer.video.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: go2rtcFixtureH264PayloadType, SequenceNumber: uint16(i + 1), Timestamp: uint32(i * go2rtcFixtureVideoTicks)}, Payload: []byte{0x65, 1, 2, 3}}); err != nil {
			return
		}
	}
	<-ctx.Done()
}
