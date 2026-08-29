package integration

import (
	"context"
	"encoding/json"
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

	negotiated          chan struct{}
	frameDelivered      chan struct{}
	videoFrameDelivered chan struct{}

	negotiatedOnce, frameOnce, videoFrameOnce sync.Once
}

type webrtcSourceObservationSnapshot struct {
	path, source                         string
	offerAudioTracks, offerVideoTracks   int
	answerAudioTracks, answerVideoTracks int
	frameCount, videoFrameCount          int
}

func (o *webrtcSourceObservation) snapshot() webrtcSourceObservationSnapshot {
	o.Lock()
	defer o.Unlock()
	return webrtcSourceObservationSnapshot{
		path: o.path, source: o.source,
		offerAudioTracks: o.offerAudioTracks, offerVideoTracks: o.offerVideoTracks,
		answerAudioTracks: o.answerAudioTracks, answerVideoTracks: o.answerVideoTracks,
		frameCount: o.frameCount, videoFrameCount: o.videoFrameCount,
	}
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
		if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
			PayloadType:        0,
		}, webrtc.RTPCodecTypeAudio); err != nil {
			return
		}
		if opts.withVideo {
			if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
				PayloadType:        96,
			}, webrtc.RTPCodecTypeVideo); err != nil {
				return
			}
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

		audio, err := webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
			"audio", "v10-camera-fixture")
		if err != nil {
			return
		}
		if _, err = pc.AddTrack(audio); err != nil {
			return
		}
		var video *webrtc.TrackLocalStaticRTP
		if opts.withVideo {
			video, err = webrtc.NewTrackLocalStaticRTP(
				webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
				"video", "v10-camera-fixture")
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
		answerSDP := pc.LocalDescription().SDP
		if !opts.withVideo {
			// An audio-only source answers without any video m-line, exactly
			// as go2rtc fronts a camera that exposes no video stream. pion
			// always echoes rejected m-lines into JSEP answers, so the video
			// section is removed before the answer reaches the wire; the
			// production client accepts the reduced answer (verified against
			// pion v4.2.18) and parseSDP reports no negotiated video track.
			answerSDP = stripSDPMediaSection(answerSDP, "video")
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
		case <-handlerContext.Done():
			return
		}
		if opts.sendFrames {
			streamFixtureAudio(t, audio, video, opts.packets, observed)
		}
		<-handlerContext.Done()
	}))
	u, _ := url.Parse(server.URL)
	rawURL := "go2rtc://" + u.Host + "/api/ws?src=v10-tuya-main"
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
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
		})
	}
	t.Cleanup(cleanup)
	return rawURL, observed, cleanup
}

// streamFixtureAssets writes every precomputed audio packet once, then a small
// burst of H.264 packets on the video track when one is negotiated. Delivery
// is recorded per track so the tests can prove real media activity rather
// than a declared-but-unused capability.
func streamFixtureAudio(t *testing.T, audio, video *webrtc.TrackLocalStaticRTP, packets [][]byte, observed *webrtcSourceObservation) {
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
			return
		}
		observed.recordVideoFrame()
	}
}
