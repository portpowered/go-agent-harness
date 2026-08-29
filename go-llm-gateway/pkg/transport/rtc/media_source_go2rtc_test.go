package rtc

import (
	"bytes"
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
