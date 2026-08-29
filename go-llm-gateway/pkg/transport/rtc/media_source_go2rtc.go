package rtc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

type go2rtcMessage struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func (s MediaSource) openGo2RTC(ctx context.Context) (*MediaStream, error) {
	ws, response, err := websocket.DefaultDialer.DialContext(ctx, s.dialURL, http.Header{})
	if err != nil {
		kind := classifyDialError(err)
		if response != nil && (response.StatusCode == 401 || response.StatusCode == 403) {
			kind = SourceErrorAuthentication
		}
		if response != nil && response.StatusCode == 404 {
			kind = SourceErrorUnknown
		}
		return nil, sourceError(kind, s.identity, operationCause(ctx, err))
	}
	pc, err := webrtc.NewAPI().NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		_ = ws.Close()
		return nil, sourceError(SourceErrorUnreachable, s.identity, err)
	}
	inbound := newPionInbound(func() error { _ = ws.Close(); return pc.Close() }, s.identity)
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		switch track.Kind() {
		case webrtc.RTPCodecTypeAudio:
			inbound.attach(track)
		case webrtc.RTPCodecTypeVideo:
			inbound.attachVideo(track)
		}
	})
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeAudio, webrtc.RTPCodecTypeVideo} {
		if _, err = pc.AddTransceiverFromKind(kind, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			_ = inbound.Close()
			return nil, sourceError(SourceErrorUnreachable, s.identity, err)
		}
	}
	offer, err := pc.CreateOffer(nil)
	if err == nil {
		err = pc.SetLocalDescription(offer)
	}
	if err == nil {
		select {
		case <-webrtc.GatheringCompletePromise(pc):
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err != nil {
		_ = inbound.Close()
		return nil, sourceError(SourceErrorUnreachable, s.identity, err)
	}
	if err = ws.WriteJSON(go2rtcMessage{Type: "webrtc/offer", Value: pc.LocalDescription().SDP}); err != nil {
		_ = inbound.Close()
		return nil, sourceError(SourceErrorUnreachable, s.identity, err)
	}
	answer := ""
	for answer == "" {
		if deadline, ok := ctx.Deadline(); ok {
			_ = ws.SetReadDeadline(deadline)
		}
		_, data, readErr := ws.ReadMessage()
		if readErr != nil {
			_ = inbound.Close()
			return nil, sourceError(SourceErrorUnreachable, s.identity, operationCause(ctx, readErr))
		}
		var message go2rtcMessage
		if json.Unmarshal(data, &message) != nil {
			continue
		}
		switch strings.ToLower(message.Type) {
		case "webrtc/answer", "answer":
			answer = message.Value
		case "error", "webrtc/error":
			_ = inbound.Close()
			return nil, sourceError(SourceErrorUnknown, s.identity, errors.New("go2rtc rejected source"))
		}
	}
	audio, video, codec, rate, channels := parseSDP(answer)
	if !audio {
		_ = inbound.Close()
		return nil, sourceError(SourceErrorNoAudio, s.identity, nil)
	}
	inbound.setVideoNegotiated(video)
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		_ = inbound.Close()
		return nil, sourceError(SourceErrorUnreachable, s.identity, err)
	}
	caps := (MediaCapabilities{Source: s.identity, AudioCodec: codec, SampleRate: rate, Channels: channels, Video: video}).normalized()
	return &MediaStream{Inbound: inbound, Media: inbound, Capabilities: caps, close: inbound.Close, look: inbound.Look}, nil
}

func parseSDP(sdp string) (audio, video bool, codec string, rate, channels int) {
	section, audioDirection, videoDirection := "", "", ""
	videoSenderEvidence := false
	for _, raw := range strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "m=audio "):
			audio, section = true, "audio"
		case strings.HasPrefix(line, "m=video "):
			video, section = true, "video"
		case strings.HasPrefix(line, "m="):
			section = ""
		case section == "audio" && isSDPMediaDirection(line):
			audioDirection = strings.TrimPrefix(line, "a=")
		case section == "video" && isSDPMediaDirection(line):
			videoDirection = strings.TrimPrefix(line, "a=")
		case section == "video" && (strings.HasPrefix(line, "a=ssrc:") || strings.HasPrefix(line, "a=msid:")):
			videoSenderEvidence = true
		case section == "audio" && strings.HasPrefix(line, "a=rtpmap:"):
			parts := strings.Fields(strings.TrimPrefix(line, "a=rtpmap:"))
			if len(parts) != 2 {
				continue
			}
			values := strings.Split(parts[1], "/")
			if len(values) < 2 || strings.EqualFold(values[0], "telephone-event") || codec != "" {
				continue
			}
			codec = strings.ToUpper(values[0])
			rate, _ = strconv.Atoi(values[1])
			channels = 1
			if len(values) > 2 {
				channels, _ = strconv.Atoi(values[2])
			}
		}
	}
	if audioDirection == "recvonly" || audioDirection == "inactive" {
		audio = false
	}
	if videoDirection == "recvonly" || videoDirection == "inactive" {
		video = false
	} else if videoDirection != "" && !videoSenderEvidence {
		// Pion can retain a video m-line for a recv-only transceiver even when
		// the source attached no video track. Sender metadata is the
		// negotiated-track evidence that distinguishes that shape from a real
		// camera sender.
		video = false
	}
	if audio && codec == "" {
		codec, rate, channels = "PCMU", 8000, 1
	}
	if channels <= 0 {
		channels = 1
	}
	return
}

func isSDPMediaDirection(line string) bool {
	switch line {
	case "a=sendrecv", "a=sendonly", "a=recvonly", "a=inactive":
		return true
	default:
		return false
	}
}
