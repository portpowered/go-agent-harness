package rtc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	ws, err := s.dialGo2RTC(ctx)
	if err != nil {
		return nil, err
	}
	pc, err := webrtc.NewAPI().NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, closeAfterFailure(ws, sourceError(SourceErrorUnreachable, s.identity, err))
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
	answer, err := s.negotiateGo2RTC(ctx, ws, pc)
	if err != nil {
		return nil, closeAfterFailure(inbound, err)
	}
	audio, video, codec, rate, channels := parseSDP(answer)
	if !audio {
		return nil, closeAfterFailure(inbound, sourceError(SourceErrorNoAudio, s.identity, nil))
	}
	inbound.setVideoNegotiated(video)
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		return nil, closeAfterFailure(inbound, sourceError(SourceErrorUnreachable, s.identity, err))
	}
	caps := (MediaCapabilities{Source: s.identity, AudioCodec: codec, SampleRate: rate, Channels: channels, Video: video}).normalized()
	return &MediaStream{Inbound: inbound, Media: inbound, Capabilities: caps, close: inbound.Close, look: inbound.Look}, nil
}

// closeAfterFailure releases resources owned by a failed open. The open
// failure stays the primary, classified error; a release failure is joined
// so it is still reported without hiding the source error identity.
func closeAfterFailure(owned io.Closer, failure error) error {
	if closeErr := owned.Close(); closeErr != nil {
		return errors.Join(failure, closeErr)
	}
	return failure
}

// releaseTemporaryStream closes a stream opened only for one Probe or Look
// call. A completed probe or observation stays authoritative when the release
// fails, matching the historical success contract; when the call already
// failed, the release failure is joined onto the classified source error.
func releaseTemporaryStream(stream *MediaStream, result error) error {
	if closeErr := stream.Close(); closeErr != nil && result != nil {
		return errors.Join(result, closeErr)
	}
	return result
}

func (s MediaSource) dialGo2RTC(ctx context.Context) (*websocket.Conn, error) {
	ws, response, err := websocket.DefaultDialer.DialContext(ctx, s.dialURL, http.Header{})
	if err == nil {
		// The upgrade response body carries no media and is released here; the
		// socket is only handed out when that release succeeds.
		if closeErr := response.Body.Close(); closeErr != nil {
			return nil, closeAfterFailure(ws, sourceError(SourceErrorUnreachable, s.identity, closeErr))
		}
		return ws, nil
	}
	if response == nil {
		return nil, sourceError(classifyDialError(err), s.identity, operationCause(ctx, err))
	}
	kind := classifyDialError(err)
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		kind = SourceErrorAuthentication
	}
	if response.StatusCode == http.StatusNotFound {
		kind = SourceErrorUnknown
	}
	return nil, closeAfterFailure(response.Body, sourceError(kind, s.identity, operationCause(ctx, err)))
}

// negotiateGo2RTC offers receive-only audio and video transceivers over the
// go2rtc signaling socket and returns the source's SDP answer. Failures are
// classified source errors; the caller owns releasing ws and pc.
func (s MediaSource) negotiateGo2RTC(ctx context.Context, ws *websocket.Conn, pc *webrtc.PeerConnection) (string, error) {
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeAudio, webrtc.RTPCodecTypeVideo} {
		if _, err := pc.AddTransceiverFromKind(kind, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			return "", sourceError(SourceErrorUnreachable, s.identity, err)
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
		return "", sourceError(SourceErrorUnreachable, s.identity, err)
	}
	if err = ws.WriteJSON(go2rtcMessage{Type: "webrtc/offer", Value: pc.LocalDescription().SDP}); err != nil {
		return "", sourceError(SourceErrorUnreachable, s.identity, err)
	}
	return s.awaitGo2RTCAnswer(ctx, ws)
}

// awaitGo2RTCAnswer reads signaling messages until go2rtc answers or rejects
// the offer. Unparseable and unrelated messages are skipped.
func (s MediaSource) awaitGo2RTCAnswer(ctx context.Context, ws *websocket.Conn) (string, error) {
	answer := ""
	for answer == "" {
		if deadline, ok := ctx.Deadline(); ok {
			if err := ws.SetReadDeadline(deadline); err != nil {
				return "", sourceError(SourceErrorUnreachable, s.identity, operationCause(ctx, err))
			}
		}
		_, data, readErr := ws.ReadMessage()
		if readErr != nil {
			return "", sourceError(SourceErrorUnreachable, s.identity, operationCause(ctx, readErr))
		}
		var message go2rtcMessage
		if json.Unmarshal(data, &message) != nil {
			continue
		}
		switch strings.ToLower(message.Type) {
		case "webrtc/answer", "answer":
			answer = message.Value
		case "error", "webrtc/error":
			return "", sourceError(SourceErrorUnknown, s.identity, errors.New("go2rtc rejected source"))
		}
	}
	return answer, nil
}

func parseSDP(sdp string) (audio, video bool, codec string, rate, channels int) {
	section, audioDirection, videoDirection := "", "", ""
	videoSenderEvidence := false
	for _, raw := range strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "m=audio "):
			audio, section = true, sdpMediaAudio
		case strings.HasPrefix(line, "m=video "):
			video, section = true, sdpMediaVideo
		case strings.HasPrefix(line, "m="):
			section = ""
		case section == sdpMediaAudio && isSDPMediaDirection(line):
			audioDirection = strings.TrimPrefix(line, "a=")
		case section == sdpMediaVideo && isSDPMediaDirection(line):
			videoDirection = strings.TrimPrefix(line, "a=")
		case section == sdpMediaVideo && (strings.HasPrefix(line, "a=ssrc:") || strings.HasPrefix(line, "a=msid:")):
			videoSenderEvidence = true
		case section == sdpMediaAudio && strings.HasPrefix(line, "a=rtpmap:"):
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
		codec, rate, channels = go2rtcCodecPCMU, 8000, 1
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

// go2rtcCodecPCMU is the G.711 mu-law codec name used in SDP.
const go2rtcCodecPCMU = "PCMU"

// sdpMediaAudio is the SDP media and track kind for audio.
const sdpMediaAudio = "audio"

// sdpMediaVideo is the SDP media and track kind for video.
const sdpMediaVideo = "video"
