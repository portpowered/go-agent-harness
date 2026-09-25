package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// webrtcSourceOptions shapes one loopback go2rtc-compatible fixture instance.
type webrtcSourceOptions struct {
	// withVideo negotiates an H.264 video m-line and track alongside the
	// audio track (the camera shape). When false the answer carries no video
	// m-line at all, so a negotiated-but-frame-less video declaration cannot
	// be confused with genuine absence.
	withVideo bool

	// sendFrames streams the deterministic PCMU packets after ICE connects.
	// A false value models a dead source whose negotiation succeeds without
	// any media activity.
	sendFrames bool

	// packets are the precomputed PCMU payloads streamed when sendFrames is
	// set. They are returned unchanged by cameraSourceRTPPackets so the test
	// can derive the exact decoded PCM stream independently.
	packets [][]byte

	// videoFirst writes the video burst before the audio packets. A client
	// that disconnects once it holds the audio it waits for (media probe, the
	// audio bridge) would otherwise race the later video writes, which then
	// fail on the closed connection and are never recorded.
	videoFirst bool
}

// webrtcSourceObservation independently records what the fixture saw:
// signaling path and requested source name, answer completion, and per-track
// media activity. This is the declared-but-unused guard: the CLI report alone
// cannot pass without the fixture having actually delivered frames.
type webrtcSourceObservation struct {
	sync.Mutex
	path, source string

	offerAudioTracks, offerVideoTracks   int
	answerAudioTracks, answerVideoTracks int
	frameCount, videoFrameCount          int
	connections                          int
	videoWriteErr                        string

	negotiated          chan struct{}
	frameDelivered      chan struct{}
	videoFrameDelivered chan struct{}
	// streamed closes once every requested packet has been written and
	// recorded. A client can hold the last frame before the fixture records
	// it, so counts are complete only after streamed.
	streamed chan struct{}

	negotiatedOnce, frameOnce, videoFrameOnce sync.Once
}

type webrtcSourceObservationSnapshot struct {
	path, source                         string
	offerAudioTracks, offerVideoTracks   int
	answerAudioTracks, answerVideoTracks int
	frameCount, videoFrameCount          int
	connections                          int
	videoWriteErr                        string
}

func (o *webrtcSourceObservation) snapshot() webrtcSourceObservationSnapshot {
	o.Lock()
	defer o.Unlock()
	return webrtcSourceObservationSnapshot{
		path: o.path, source: o.source,
		offerAudioTracks: o.offerAudioTracks, offerVideoTracks: o.offerVideoTracks,
		answerAudioTracks: o.answerAudioTracks, answerVideoTracks: o.answerVideoTracks,
		frameCount: o.frameCount, videoFrameCount: o.videoFrameCount,
		connections: o.connections, videoWriteErr: o.videoWriteErr,
	}
}

// streamedSnapshot waits until the fixture finished streaming, so every
// written packet is counted, and then returns its evidence.
func (o *webrtcSourceObservation) streamedSnapshot(t *testing.T, name string) webrtcSourceObservationSnapshot {
	t.Helper()
	waitForExternalSourceEvent(t, o.streamed, name+" fixture stream completion")
	return o.snapshot()
}

func (o *webrtcSourceObservation) recordNegotiation(offerSDP, answerSDP string) {
	o.Lock()
	o.offerAudioTracks = countSDPMediaSections(offerSDP, "audio")
	o.offerVideoTracks = countSDPMediaSections(offerSDP, "video")
	o.answerAudioTracks = countSDPMediaSections(answerSDP, "audio")
	o.answerVideoTracks = countSDPMediaSections(answerSDP, "video")
	o.Unlock()
}

func (o *webrtcSourceObservation) recordAudioFrame() {
	o.Lock()
	o.frameCount++
	o.Unlock()
	o.frameOnce.Do(func() { close(o.frameDelivered) })
}

func (o *webrtcSourceObservation) recordVideoFrame() {
	o.Lock()
	o.videoFrameCount++
	videoFrames := o.videoFrameCount
	o.Unlock()
	if videoFrames >= externalSourceVideoPackets {
		o.videoFrameOnce.Do(func() { close(o.videoFrameDelivered) })
	}
}

func startWebrtcSourceFixture(t *testing.T, opts webrtcSourceOptions) (string, *webrtcSourceObservation, func()) {
	t.Helper()
	observed := &webrtcSourceObservation{
		negotiated:          make(chan struct{}),
		streamed:            make(chan struct{}),
		frameDelivered:      make(chan struct{}),
		videoFrameDelivered: make(chan struct{}),
	}
	var handlers sync.WaitGroup
	fixtureContext, cancelFixture := context.WithCancel(context.Background())
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
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
		observed.connections++
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
		defer discardCloseError(conn)
		serveWebrtcSource(t, handlerContext, conn, opts, observed) //nolint:contextcheck // handlerContext derives from r.Context() and also ends with the fixture.
	}))
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse go2rtc fixture URL %q: %v", server.URL, err)
	}
	rawURL := "go2rtc://" + u.Host + "/api/ws?src=v10-tuya-main"
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() { closeWebrtcSourceFixture(t, cancelFixture, server, &handlers) })
	}
	t.Cleanup(cleanup)
	return rawURL, observed, cleanup
}

// serveWebrtcSource answers one go2rtc WebRTC offer with PCMU audio (and
// optional H.264 video) and streams fixture frames once connected.
func serveWebrtcSource(t *testing.T, ctx context.Context, conn *websocket.Conn, opts webrtcSourceOptions, observed *webrtcSourceObservation) {
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
	pc, audio, video, err := newWebrtcSourcePeer(opts.withVideo)
	if err != nil {
		return
	}
	defer discardCloseError(pc)
	connected := make(chan struct{})
	var connectedOnce sync.Once
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			connectedOnce.Do(func() { close(connected) })
		}
	})
	answerSDP, ok := answerWebrtcOffer(ctx, pc, offer.Value, opts.withVideo)
	if !ok {
		return
	}
	observed.recordNegotiation(offer.Value, answerSDP)
	if err = conn.WriteJSON(struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}{Type: "webrtc/answer", Value: answerSDP}); err != nil {
		return
	}
	observed.negotiatedOnce.Do(func() { close(observed.negotiated) })
	select {
	case <-connected:
	case <-ctx.Done():
		return
	}
	if opts.sendFrames {
		streamFixtureMedia(t, audio, video, opts, observed)
		close(observed.streamed)
	}
	<-ctx.Done()
}

// newWebrtcSourcePeer builds the camera-side peer connection with its PCMU
// audio track and, when requested, an H.264 video track.
func newWebrtcSourcePeer(withVideo bool) (*webrtc.PeerConnection, *webrtc.TrackLocalStaticRTP, *webrtc.TrackLocalStaticRTP, error) {
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
		PayloadType:        0,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, nil, nil, err
	}
	if withVideo {
		if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
			PayloadType:        96,
		}, webrtc.RTPCodecTypeVideo); err != nil {
			return nil, nil, nil, err
		}
	}
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, nil, nil, err
	}
	audio, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
		"audio", "v10-camera-fixture")
	if err == nil {
		_, err = pc.AddTrack(audio)
	}
	var video *webrtc.TrackLocalStaticRTP
	if err == nil && withVideo {
		video, err = webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
			"video", "v10-camera-fixture")
		if err == nil {
			_, err = pc.AddTrack(video)
		}
	}
	if err != nil {
		return nil, nil, nil, errors.Join(err, pc.Close())
	}
	return pc, audio, video, nil
}

// answerWebrtcOffer applies the offer and returns the gathered answer SDP.
// An audio-only source answers without any video m-line, exactly as go2rtc
// fronts a camera that exposes no video stream. pion always echoes rejected
// m-lines into JSEP answers, so the video section is removed before the
// answer reaches the wire; the production client accepts the reduced answer
// (verified against pion v4.2.18) and parseSDP reports no negotiated video
// track.
func answerWebrtcOffer(ctx context.Context, pc *webrtc.PeerConnection, offerSDP string, withVideo bool) (string, bool) {
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		return "", false
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", false
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return "", false
	}
	select {
	case <-webrtc.GatheringCompletePromise(pc):
	case <-ctx.Done():
		return "", false
	}
	answerSDP := pc.LocalDescription().SDP
	if !withVideo {
		answerSDP = stripSDPMediaSection(answerSDP, "video")
	}
	return answerSDP, true
}

// closeWebrtcSourceFixture cancels live handlers and closes the server, each
// within a one-second bound.
func closeWebrtcSourceFixture(t *testing.T, cancelFixture context.CancelFunc, server *httptest.Server, handlers *sync.WaitGroup) {
	cancelFixture()
	server.CloseClientConnections()
	handlersDone := make(chan struct{})
	go func() {
		handlers.Wait()
		close(handlersDone)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-handlersDone:
	case <-ctx.Done():
		t.Errorf("go2rtc fixture handlers did not close: %v", ctx.Err())
	}
	serverClosed := make(chan struct{})
	go func() {
		server.Close()
		close(serverClosed)
	}()
	select {
	case <-serverClosed:
	case <-ctx.Done():
		t.Errorf("go2rtc fixture server did not close: %v", ctx.Err())
	}
}

// streamFixtureMedia writes every precomputed audio packet once and a small
// burst of H.264 packets on the video track when one is negotiated, in the
// order opts asks for. Delivery is recorded per track so the tests can prove
// real media activity rather than a declared-but-unused capability.
func streamFixtureMedia(t *testing.T, audio, video *webrtc.TrackLocalStaticRTP, opts webrtcSourceOptions, observed *webrtcSourceObservation) {
	t.Helper()
	if opts.videoFirst {
		streamFixtureVideo(video, observed)
		streamFixtureAudio(t, audio, opts.packets, observed)
		return
	}
	streamFixtureAudio(t, audio, opts.packets, observed)
	streamFixtureVideo(video, observed)
}

func streamFixtureAudio(t *testing.T, audio *webrtc.TrackLocalStaticRTP, packets [][]byte, observed *webrtcSourceObservation) {
	t.Helper()
	for i, payload := range packets {
		packet := &rtp.Packet{Header: rtp.Header{
			Version:        2,
			PayloadType:    0,
			SequenceNumber: uint16(i + 1),
			Timestamp:      uint32(i * externalSourcePacketSamples),
		}, Payload: payload}
		data, err := packet.Marshal()
		if err != nil {
			t.Errorf("marshal fixture RTP: %v", err)
			return
		}
		if _, err := audio.Write(data); err != nil {
			t.Errorf("write fixture audio packet: %v", err)
			return
		}
		observed.recordAudioFrame()
	}
}

func streamFixtureVideo(video *webrtc.TrackLocalStaticRTP, observed *webrtcSourceObservation) {
	for i := 0; i < externalSourceVideoPackets && video != nil; i++ {
		packet := &rtp.Packet{Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: uint16(i + 1),
			Timestamp:      uint32(i * 3000),
			Marker:         i == 2,
		}, Payload: []byte{0x67, 0x42, 0xc0, 0x1f}} // H.264 SPS-shaped bytes
		data, err := packet.Marshal()
		if err != nil {
			return
		}
		if _, err := video.Write(data); err != nil {
			observed.Lock()
			observed.videoWriteErr = err.Error()
			observed.Unlock()
			return
		}
		observed.recordVideoFrame()
	}
}

// mediaObservationParser accumulates the public media CLI report fields and
// which required ones were present.
type mediaObservationParser struct {
	observation                                                  mediaObservation
	sourceSet, codecSet, rateSet, channelsSet, videoSet, lookSet bool
}

func (p *mediaObservationParser) field(key, value string) error {
	var err error
	switch key {
	case "Source":
		if !p.sourceSet {
			p.observation.source = value
		}
		p.sourceSet = true
	case "Audio codec":
		p.observation.codec, p.codecSet = value, true
	case "Sample rate":
		p.observation.sampleRate, err = strconv.Atoi(value)
		p.rateSet = true
		err = wrapMediaFieldError("sample rate", value, err)
	case "Channels":
		p.observation.channels, err = strconv.Atoi(value)
		p.channelsSet = true
		err = wrapMediaFieldError("channels", value, err)
	case "Video presence":
		p.observation.videoPresence, err = strconv.ParseBool(value)
		p.videoSet = true
		err = wrapMediaFieldError("video presence", value, err)
	case "Look status":
		p.observation.lookStatus, p.lookSet = value, true
	case "Reason":
		p.observation.lookReason = value
	case "Media type":
		p.observation.mediaType = value
	case "Observation bytes":
		p.observation.observationBytes, err = strconv.Atoi(value)
		err = wrapMediaFieldError("observation bytes", value, err)
	}
	return err
}

func wrapMediaFieldError(name, value string, err error) error {
	if err != nil {
		return fmt.Errorf("parse %s %q: %w", name, value, err)
	}
	return nil
}

// waitForVideoDelivery bounds the video delivery observation like
// waitForExternalSourceEvent and reports what the fixture saw on timeout.
func waitForVideoDelivery(t *testing.T, observed *webrtcSourceObservation, name string) {
	t.Helper()
	select {
	case <-observed.videoFrameDelivered:
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s\n%s", name, sourceObservationDiagnostics(observed.snapshot()))
	}
}
