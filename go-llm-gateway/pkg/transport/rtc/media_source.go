package rtc

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const (
	DefaultMediaSourceTimeout       = 5 * time.Second
	DefaultVisualObservationTimeout = 500 * time.Millisecond
	RedactionMarker                 = "<redacted>"
)

type SourceKind string

const (
	SourceKindGo2RTC SourceKind = "go2rtc"
	SourceKindRTSP   SourceKind = "rtsp"
)

type SourceErrorKind string

const (
	SourceErrorMalformed      SourceErrorKind = "malformed source"
	SourceErrorUnreachable    SourceErrorKind = "source unreachable"
	SourceErrorWrongPort      SourceErrorKind = "source port refused"
	SourceErrorAuthentication SourceErrorKind = "source authentication failed"
	SourceErrorUnknown        SourceErrorKind = "unknown source"
	SourceErrorNoAudio        SourceErrorKind = "source has no audio"
	SourceErrorMalformedURL   SourceErrorKind = SourceErrorMalformed
	SourceErrorBadCredentials SourceErrorKind = SourceErrorAuthentication
	SourceErrorUnavailable    SourceErrorKind = SourceErrorUnreachable
)

var (
	ErrMalformedSource           = errors.New(string(SourceErrorMalformed))
	ErrSourceUnreachable         = errors.New(string(SourceErrorUnreachable))
	ErrSourceWrongPort           = errors.New(string(SourceErrorWrongPort))
	ErrSourceAuthentication      = errors.New(string(SourceErrorAuthentication))
	ErrUnknownSource             = errors.New(string(SourceErrorUnknown))
	ErrNoAudio                   = errors.New(string(SourceErrorNoAudio))
	ErrMediaSourceMalformed      = ErrMalformedSource
	ErrMediaSourceUnreachable    = ErrSourceUnreachable
	ErrMediaSourceWrongPort      = ErrSourceWrongPort
	ErrMediaSourceAuthentication = ErrSourceAuthentication
	ErrMediaSourceUnknown        = ErrUnknownSource
	ErrMediaSourceNoAudio        = ErrNoAudio
	ErrBadCredentials            = ErrSourceAuthentication
	ErrWrongPort                 = ErrSourceWrongPort
)

// MediaSourceError is a safe, typed source failure. Cause is a safe adapter:
// it preserves errors.Is/errors.As without retaining a raw URL in the error's
// public text or fields.
type MediaSourceError struct {
	Kind             SourceErrorKind
	Source, Identity string
	Cause, cause     error
}

func (e *MediaSourceError) Error() string {
	if e == nil {
		return "media source error"
	}
	source := e.Source
	action := map[SourceErrorKind]string{
		SourceErrorMalformed: "check the supported go2rtc or RTSP URL form", SourceErrorUnreachable: "check the host, port, and camera availability", SourceErrorWrongPort: "check that the source port is listening",
		SourceErrorAuthentication: "check the source credentials", SourceErrorUnknown: "check the source name or stream path", SourceErrorNoAudio: "choose a source that offers an audio track",
	}[e.Kind]
	if source == "" {
		source = string(e.Identity)
	}
	if action == "" {
		action = "check the source"
	}
	return fmt.Sprintf("media source %s: %s; %s", source, e.Kind, action)
}
func (e *MediaSourceError) Unwrap() error { return e.cause }
func (e *MediaSourceError) Is(target error) bool {
	if e == nil {
		return false
	}
	want := map[SourceErrorKind]error{SourceErrorMalformed: ErrMalformedSource, SourceErrorUnreachable: ErrSourceUnreachable, SourceErrorWrongPort: ErrSourceWrongPort, SourceErrorAuthentication: ErrSourceAuthentication, SourceErrorUnknown: ErrUnknownSource, SourceErrorNoAudio: ErrNoAudio}[e.Kind]
	if target == want {
		return true
	}
	if e.Kind == SourceErrorWrongPort && target == ErrSourceUnreachable {
		return true
	}
	other, ok := target.(*MediaSourceError)
	return ok && other.Kind == e.Kind && other.Source == e.Source
}

type safeCause struct{ err error }

func (e safeCause) Error() string        { return "source operation failed" }
func (e safeCause) Is(target error) bool { return e.err != nil && errors.Is(e.err, target) }
func (e safeCause) As(target any) bool   { return e.err != nil && errors.As(e.err, target) }
func sourceError(kind SourceErrorKind, source string, cause error) error {
	var safe error
	if cause != nil {
		safe = safeCause{cause}
	}
	return &MediaSourceError{Kind: kind, Source: source, Identity: source, Cause: safe, cause: safe}
}

// operationCause attributes a failed source operation to its context once the
// context knows it expired. A connection deadline set from that same context
// can fire moments before the context timer itself is serviced; a socket
// timeout observed after the deadline verifiably passed therefore still
// carries the context's DeadlineExceeded identity instead of raw i/o timeout.
func operationCause(ctx context.Context, err error) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if deadline, ok := ctx.Deadline(); ok && errors.Is(err, os.ErrDeadlineExceeded) && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
	}
	return err
}

func classifyDialError(err error) SourceErrorKind {
	message := strings.ToLower(errString(err))
	if errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(message, "connection refused") || strings.Contains(message, "no connection could be made") {
		return SourceErrorWrongPort
	}
	return SourceErrorUnreachable
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type MediaSource struct {
	kind                                SourceKind
	identity, dialURL, requestURI, host string
	username, password                  string
}

// ParseMediaSource accepts only go2rtc://host/api/ws?src=name and
// rtsp://user:pass@host/path. Private dial/auth state is never returned by a
// public formatter or copied into capabilities.
func ParseMediaSource(raw string) (MediaSource, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return MediaSource{}, sourceError(SourceErrorMalformed, safeIdentity(raw), err)
	}
	scheme := strings.ToLower(u.Scheme)
	identity := publicURL(u, scheme)
	if u.Host == "" || u.Fragment != "" || strings.ContainsAny(raw, "\r\n") {
		return MediaSource{}, sourceError(SourceErrorMalformed, identity, nil)
	}
	switch scheme {
	case string(SourceKindGo2RTC):
		q, queryErr := url.ParseQuery(u.RawQuery)
		if queryErr != nil || u.Path != "/api/ws" || u.Hostname() == "" || len(q["src"]) != 1 || strings.TrimSpace(q.Get("src")) == "" {
			return MediaSource{}, sourceError(SourceErrorMalformed, identity, queryErr)
		}
		private := *u
		private.Scheme = "ws"
		s := MediaSource{kind: SourceKindGo2RTC, identity: identity, dialURL: private.String(), requestURI: u.Path, host: u.Host}
		if u.User != nil {
			s.username, s.password = u.User.Username(), passwordOf(u.User)
		}
		return s, nil
	case string(SourceKindRTSP):
		if u.Hostname() == "" || u.Path == "" || u.Path == "/" || u.Opaque != "" || (u.User != nil && u.User.Username() == "") {
			return MediaSource{}, sourceError(SourceErrorMalformed, identity, nil)
		}
		request := *u
		request.User = nil
		s := MediaSource{kind: SourceKindRTSP, identity: identity, dialURL: u.String(), requestURI: request.String(), host: u.Host}
		if u.User != nil {
			s.username, s.password = u.User.Username(), passwordOf(u.User)
		}
		return s, nil
	default:
		return MediaSource{}, sourceError(SourceErrorMalformed, identity, nil)
	}
}
func passwordOf(user *url.Userinfo) string           { password, _ := user.Password(); return password }
func NewMediaSource(raw string) (MediaSource, error) { return ParseMediaSource(raw) }
func ParseSource(raw string) (MediaSource, error)    { return ParseMediaSource(raw) }
func (s MediaSource) String() string                 { return s.identity }
func (s MediaSource) Identity() string               { return s.identity }
func (s MediaSource) Kind() SourceKind               { return s.kind }
func (s MediaSource) URL() string                    { return s.identity }

type ExternalMediaSource = MediaSource

func safeIdentity(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid source>"
	}
	return publicURL(u, strings.ToLower(u.Scheme))
}
func publicURL(u *url.URL, scheme string) string {
	v := *u
	v.Scheme, v.User = scheme, nil
	if u.User == nil {
		return v.String()
	}
	user := url.QueryEscape(u.User.Username())
	if _, ok := u.User.Password(); ok {
		user += ":" + RedactionMarker
	}
	return scheme + "://" + user + "@" + strings.TrimPrefix(v.String(), scheme+"://")
}

type MediaCapabilities struct {
	Source                                       string
	AudioCodec, Codec                            string
	SampleRate, AudioSampleRate                  int
	Channels, AudioChannels                      int
	Video, HasVideo, VideoPresent, VideoPresence bool
}

func (c MediaCapabilities) normalized() MediaCapabilities {
	if c.AudioCodec == "" {
		c.AudioCodec = c.Codec
	}
	if c.Codec == "" {
		c.Codec = c.AudioCodec
	}
	if c.SampleRate == 0 {
		c.SampleRate = c.AudioSampleRate
	}
	if c.AudioSampleRate == 0 {
		c.AudioSampleRate = c.SampleRate
	}
	if c.Channels == 0 {
		c.Channels = c.AudioChannels
	}
	if c.AudioChannels == 0 {
		c.AudioChannels = c.Channels
	}
	c.HasVideo, c.VideoPresent, c.VideoPresence = c.Video, c.Video, c.Video
	return c
}

type SourceCapabilities = MediaCapabilities
type MediaSourceCapabilities = MediaCapabilities

type MediaStream struct {
	Inbound, Media InboundMedia
	Capabilities   MediaCapabilities
	close          func() error
	look           func(context.Context) (VisualObservation, error)
	once           sync.Once
	closeErr       error
}
type MediaSourceStream = MediaStream

func (s *MediaStream) ReadFrame(ctx context.Context) (PCMFrame, error) {
	if s == nil || s.Inbound == nil {
		return PCMFrame{}, ErrPeerNotConnected
	}
	return s.Inbound.ReadFrame(ctx)
}

// Look returns one caller-owned visual observation from the stream. A source
// without an attached video track returns an unavailable result with no error;
// it does not invalidate the stream's audio endpoint. Implementations must
// return the caller's context error unchanged when the observation is
// blocked and that context is cancelled or reaches its deadline.
func (s *MediaStream) Look(ctx context.Context) (VisualObservation, error) {
	if s == nil || s.look == nil {
		return VisualObservation{Source: sourceOfStream(s), Status: VisualObservationUnavailable, Reason: VisualObservationReasonNoVideoTrack}, nil
	}
	return s.look(ctx)
}

// Observe is an equivalent descriptive name for Look.
func (s *MediaStream) Observe(ctx context.Context) (VisualObservation, error) {
	return s.Look(ctx)
}

func sourceOfStream(s *MediaStream) string {
	if s == nil {
		return ""
	}
	return s.Capabilities.Source
}

func (s *MediaStream) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.close != nil {
			s.closeErr = s.close()
		} else if s.Inbound != nil {
			s.closeErr = s.Inbound.Close()
		}
	})
	return s.closeErr
}
func boundedSourceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, DefaultMediaSourceTimeout)
}

func boundedVisualContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, DefaultVisualObservationTimeout)
}

func callerContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func unavailableVisual(source string) VisualObservation {
	return VisualObservation{Source: source, Status: VisualObservationUnavailable, Reason: VisualObservationReasonNoVideoTrack}
}

func (s MediaSource) Open(ctx context.Context) (*MediaStream, error) {
	ctx, cancel := boundedSourceContext(ctx)
	defer cancel()
	switch s.kind {
	case SourceKindGo2RTC:
		return s.openGo2RTC(ctx)
	case SourceKindRTSP:
		return s.openRTSP(ctx)
	default:
		return nil, sourceError(SourceErrorMalformed, s.identity, nil)
	}
}
func (s MediaSource) Probe(ctx context.Context) (MediaCapabilities, error) {
	ctx, cancel := boundedSourceContext(ctx)
	defer cancel()
	stream, err := s.Open(ctx)
	if err != nil {
		return MediaCapabilities{}, err
	}
	defer stream.Close()
	frame, err := stream.ReadFrame(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return MediaCapabilities{}, sourceError(SourceErrorUnreachable, s.identity, ctx.Err())
		}
		return MediaCapabilities{}, sourceError(SourceErrorUnreachable, s.identity, err)
	}
	if len(frame.Samples) == 0 {
		return MediaCapabilities{}, sourceError(SourceErrorUnreachable, s.identity, nil)
	}
	return stream.Capabilities.normalized(), nil
}

// Look opens the source, returns one visual observation, and closes every
// resource owned by the temporary stream before returning. An audio-only
// source is represented by a successful unavailable result rather than a
// source error.
func (s MediaSource) Look(ctx context.Context) (VisualObservation, error) {
	stream, err := s.Open(ctx)
	if err != nil {
		return VisualObservation{Source: s.identity}, err
	}
	defer stream.Close()
	return stream.Look(ctx)
}

// Observe is an equivalent descriptive name for Look.
func (s MediaSource) Observe(ctx context.Context) (VisualObservation, error) {
	return s.Look(ctx)
}

func ProbeMediaSource(ctx context.Context, raw string) (MediaCapabilities, error) {
	s, err := ParseMediaSource(raw)
	if err != nil {
		return MediaCapabilities{}, err
	}
	return s.Probe(ctx)
}
func ProbeSource(ctx context.Context, raw string) (MediaCapabilities, error) {
	return ProbeMediaSource(ctx, raw)
}

// LookMediaSource parses, opens, observes, and closes one external media
// source. The returned observation contains only the source's safe identity.
func LookMediaSource(ctx context.Context, raw string) (VisualObservation, error) {
	s, err := ParseMediaSource(raw)
	if err != nil {
		return VisualObservation{}, err
	}
	return s.Look(ctx)
}

// LookSource is a concise alias for LookMediaSource.
func LookSource(ctx context.Context, raw string) (VisualObservation, error) {
	return LookMediaSource(ctx, raw)
}

// ObserveMediaSource is an equivalent descriptive alias for LookMediaSource.
func ObserveMediaSource(ctx context.Context, raw string) (VisualObservation, error) {
	return LookMediaSource(ctx, raw)
}

// ObserveSource is an equivalent descriptive alias for ObserveMediaSource.
func ObserveSource(ctx context.Context, raw string) (VisualObservation, error) {
	return LookMediaSource(ctx, raw)
}
func OpenMediaSource(ctx context.Context, raw string) (*MediaStream, error) {
	s, err := ParseMediaSource(raw)
	if err != nil {
		return nil, err
	}
	return s.Open(ctx)
}
func OpenSource(ctx context.Context, raw string) (*MediaStream, error) {
	return OpenMediaSource(ctx, raw)
}

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

type pionInbound struct {
	frames          chan PCMFrame
	visuals         chan pionVisualFrame
	done            chan struct{}
	once            sync.Once
	close           func() error
	mu              sync.Mutex
	audioSeen       bool
	videoSeen       bool
	videoNegotiated bool
	videoMediaType  string
	videoReady      chan struct{}
	videoReadyOnce  sync.Once
	source          string
}

type pionVisualFrame struct {
	mediaType string
	bytes     []byte
}

func newPionInbound(closeFn func() error, source ...string) *pionInbound {
	identity := ""
	if len(source) > 0 {
		identity = source[0]
	}
	return &pionInbound{
		frames:     make(chan PCMFrame, 8),
		visuals:    make(chan pionVisualFrame, 8),
		done:       make(chan struct{}),
		close:      closeFn,
		videoReady: make(chan struct{}),
		source:     identity,
	}
}

func (m *pionInbound) setVideoNegotiated(negotiated bool) {
	m.mu.Lock()
	m.videoNegotiated = negotiated
	m.mu.Unlock()
}

func (m *pionInbound) attach(track *webrtc.TrackRemote) {
	m.attachAudio(track)
}

func (m *pionInbound) attachAudio(track *webrtc.TrackRemote) {
	if track == nil {
		return
	}
	m.mu.Lock()
	if m.audioSeen {
		m.mu.Unlock()
		return
	}
	m.audioSeen = true
	m.mu.Unlock()
	go func() {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			samples := decodeAudio(track.Codec().MimeType, packet.Payload)
			if len(samples) == 0 {
				continue
			}
			select {
			case m.frames <- PCMFrame{Samples: samples}:
			case <-m.done:
				return
			}
		}
	}()
}

func (m *pionInbound) attachVideo(track *webrtc.TrackRemote) {
	if track == nil {
		return
	}
	mediaType := track.Codec().MimeType
	m.mu.Lock()
	if m.videoSeen {
		m.mu.Unlock()
		return
	}
	m.videoSeen = true
	m.videoMediaType = mediaType
	m.videoReadyOnce.Do(func() { close(m.videoReady) })
	m.mu.Unlock()
	go func() {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			if packet == nil || len(packet.Payload) == 0 {
				continue
			}
			payload := append([]byte(nil), packet.Payload...)
			select {
			case m.visuals <- pionVisualFrame{mediaType: mediaType, bytes: payload}:
			case <-m.done:
				return
			}
		}
	}()
}

func (m *pionInbound) Look(ctx context.Context) (VisualObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	negotiated, attached, mediaType, ready := m.videoNegotiated, m.videoSeen, m.videoMediaType, m.videoReady
	m.mu.Unlock()
	if !negotiated && !attached {
		return VisualObservation{Source: m.source, Status: VisualObservationUnavailable, Reason: VisualObservationReasonNoVideoTrack}, nil
	}
	if err := callerContextError(ctx); err != nil {
		return VisualObservation{}, err
	}
	lookCtx, cancel := context.WithTimeout(ctx, DefaultVisualObservationTimeout)
	defer cancel()
	if !attached {
		select {
		case <-ready:
			m.mu.Lock()
			mediaType = m.videoMediaType
			m.mu.Unlock()
		case <-m.done:
			if err := ctx.Err(); err != nil {
				return VisualObservation{}, err
			}
			return VisualObservation{Source: m.source, Status: VisualObservationUnavailable, Reason: VisualObservationReasonNoVideoTrack}, nil
		case <-lookCtx.Done():
			if err := ctx.Err(); err != nil {
				return VisualObservation{}, err
			}
			return VisualObservation{Source: m.source, Status: VisualObservationUnavailable, Reason: VisualObservationReasonNoVideoTrack}, nil
		}
	}
	for {
		select {
		case frame := <-m.visuals:
			if len(frame.bytes) == 0 {
				continue
			}
			if mediaType == "" {
				m.mu.Lock()
				mediaType = m.videoMediaType
				m.mu.Unlock()
			}
			return VisualObservation{Source: m.source, Status: VisualObservationAvailable, MediaType: mediaType, Bytes: append([]byte(nil), frame.bytes...)}, nil
		case <-m.done:
			if err := ctx.Err(); err != nil {
				return VisualObservation{}, err
			}
			return VisualObservation{Source: m.source, Status: VisualObservationUnavailable, Reason: VisualObservationReasonNoVideoTrack}, nil
		case <-lookCtx.Done():
			if err := ctx.Err(); err != nil {
				return VisualObservation{}, err
			}
			return VisualObservation{Source: m.source, Status: VisualObservationUnavailable, Reason: VisualObservationReasonNoVideoTrack}, nil
		}
	}
}

func (m *pionInbound) ReadFrame(ctx context.Context) (PCMFrame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case frame := <-m.frames:
		return frame, nil
	case <-m.done:
		return PCMFrame{}, io.EOF
	case <-ctx.Done():
		return PCMFrame{}, ctx.Err()
	}
}
func (m *pionInbound) Close() error {
	m.once.Do(func() {
		close(m.done)
		if m.close != nil {
			_ = m.close()
		}
	})
	return nil
}

type rtspTrack struct {
	control   string
	audio     bool
	mediaType string
}
type rtspClient struct {
	conn                     net.Conn
	reader                   *bufio.Reader
	uri, user, pass, session string
	cseq                     int
	auth                     bool
}
type rtspResponse struct {
	code    int
	headers map[string]string
	body    []byte
}

func (s MediaSource) openRTSP(ctx context.Context) (*MediaStream, error) {
	address := s.host
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(s.host, "554")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, sourceError(classifyDialError(err), s.identity, operationCause(ctx, err))
	}
	c := &rtspClient{conn: conn, reader: bufio.NewReader(conn), uri: s.requestURI, user: s.username, pass: s.password}
	response, err := c.request(ctx, "DESCRIBE", c.uri, map[string]string{"Accept": "application/sdp"})
	if err != nil {
		_ = conn.Close()
		return nil, sourceError(SourceErrorUnreachable, s.identity, operationCause(ctx, err))
	}
	if response.code == 401 {
		if c.user == "" {
			_ = conn.Close()
			return nil, sourceError(SourceErrorAuthentication, s.identity, nil)
		}
		c.auth = true
		response, err = c.request(ctx, "DESCRIBE", c.uri, map[string]string{"Accept": "application/sdp"})
	}
	if err != nil {
		_ = conn.Close()
		return nil, sourceError(SourceErrorUnreachable, s.identity, operationCause(ctx, err))
	}
	if response.code == 401 {
		_ = conn.Close()
		return nil, sourceError(SourceErrorAuthentication, s.identity, nil)
	}
	if response.code == 404 {
		_ = conn.Close()
		return nil, sourceError(SourceErrorUnknown, s.identity, nil)
	}
	if response.code < 200 || response.code >= 300 {
		_ = conn.Close()
		return nil, sourceError(SourceErrorUnreachable, s.identity, fmt.Errorf("RTSP status %d", response.code))
	}
	audio, video, codec, rate, channels := parseSDP(string(response.body))
	if !audio {
		_ = conn.Close()
		return nil, sourceError(SourceErrorNoAudio, s.identity, nil)
	}
	tracks := parseRTSPTracks(string(response.body), response.headers["content-base"], c.uri)
	if len(tracks) == 0 {
		tracks = []rtspTrack{{control: c.uri, audio: true}}
	}
	audioChannel, videoChannel := -1, -1
	videoMediaType := ""
	for i := range tracks {
		setup, setupErr := c.request(ctx, "SETUP", tracks[i].control, map[string]string{"Transport": fmt.Sprintf("RTP/AVP/TCP;unicast;interleaved=%d-%d", i*2, i*2+1)})
		if setupErr != nil || setup.code < 200 || setup.code >= 300 {
			_ = conn.Close()
			return nil, sourceError(SourceErrorUnreachable, s.identity, operationCause(ctx, setupErr))
		}
		if c.session == "" {
			c.session = strings.Split(setup.headers["session"], ";")[0]
		}
		if tracks[i].audio {
			audioChannel = i * 2
		} else if video {
			videoChannel = i * 2
			videoMediaType = tracks[i].mediaType
		}
	}
	if audioChannel < 0 {
		_ = conn.Close()
		return nil, sourceError(SourceErrorNoAudio, s.identity, nil)
	}
	play, err := c.request(ctx, "PLAY", c.uri, map[string]string{"Session": c.session})
	if err != nil || play.code < 200 || play.code >= 300 {
		_ = conn.Close()
		return nil, sourceError(SourceErrorUnreachable, s.identity, operationCause(ctx, err))
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, sourceError(SourceErrorUnreachable, s.identity, err)
	}
	inbound := &rtspInbound{client: c, audioChannel: audioChannel, codec: codec, videoChannel: videoChannel, videoMediaType: videoMediaType, source: s.identity}
	caps := (MediaCapabilities{Source: s.identity, AudioCodec: codec, SampleRate: rate, Channels: channels, Video: video}).normalized()
	return &MediaStream{Inbound: inbound, Media: inbound, Capabilities: caps, close: inbound.Close, look: inbound.Look}, nil
}

func (c *rtspClient) request(ctx context.Context, method, uri string, headers map[string]string) (rtspResponse, error) {
	c.cseq++
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s RTSP/1.0\r\nCSeq: %d\r\n", method, uri, c.cseq)
	for key, value := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", key, value)
	}
	if c.session != "" && headers["Session"] == "" {
		fmt.Fprintf(&b, "Session: %s\r\n", c.session)
	}
	if c.auth && c.user != "" {
		fmt.Fprintf(&b, "Authorization: Basic %s\r\n", base64.StdEncoding.EncodeToString([]byte(c.user+":"+c.pass)))
	}
	fmt.Fprint(&b, "Content-Length: 0\r\n\r\n")
	if _, err := io.WriteString(c.conn, b.String()); err != nil {
		return rtspResponse{}, err
	}
	return c.readResponse()
}
func (c *rtspClient) readResponse() (rtspResponse, error) {
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return rtspResponse{}, err
	}
	parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
	if len(parts) < 2 {
		return rtspResponse{}, errors.New("invalid RTSP response")
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return rtspResponse{}, errors.New("invalid RTSP status")
	}
	r := rtspResponse{code: code, headers: map[string]string{}}
	for {
		line, err = c.reader.ReadString('\n')
		if err != nil {
			return rtspResponse{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			r.headers[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	if n, _ := strconv.Atoi(r.headers["content-length"]); n > 0 {
		r.body = make([]byte, n)
		_, err = io.ReadFull(c.reader, r.body)
	}
	return r, err
}
func parseRTSPTracks(sdp, base, fallback string) []rtspTrack {
	section, mediaType := "", ""
	tracks := []rtspTrack{}
	for _, raw := range strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "m=audio "):
			section, mediaType = "audio", ""
		case strings.HasPrefix(line, "m=video "):
			section, mediaType = "video", ""
		case strings.HasPrefix(line, "m="):
			section, mediaType = "", ""
		case section != "" && strings.HasPrefix(line, "a=rtpmap:"):
			parts := strings.Fields(strings.TrimPrefix(line, "a=rtpmap:"))
			if len(parts) != 2 {
				continue
			}
			values := strings.Split(parts[1], "/")
			if len(values) < 1 || strings.EqualFold(values[0], "telephone-event") {
				continue
			}
			mediaType = section + "/" + strings.ToUpper(values[0])
		case strings.HasPrefix(line, "a=control:") && section != "":
			control := strings.TrimPrefix(line, "a=control:")
			if control != "" && control != "*" {
				tracks = append(tracks, rtspTrack{control: joinRTSPControl(base, fallback, control), audio: section == "audio", mediaType: mediaType})
			}
		}
	}
	return tracks
}
func joinRTSPControl(base, fallback, control string) string {
	if strings.HasPrefix(control, "rtsp://") {
		return control
	}
	if base == "" {
		base = fallback
	}
	if strings.HasPrefix(control, "/") {
		if u, err := url.Parse(base); err == nil {
			u.Path, u.RawQuery = control, ""
			return u.String()
		}
	}
	if u, err := url.Parse(base); err == nil {
		u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(control, "/")
		u.RawQuery = ""
		return u.String()
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(control, "/")
}

type rtspInbound struct {
	client         *rtspClient
	audioChannel   int
	videoChannel   int
	codec          string
	videoMediaType string
	source         string
	mu             sync.Mutex
	pendingAudio   []PCMFrame
	pendingVisuals []VisualObservation
	once           sync.Once
}

func (r *rtspInbound) ReadFrame(ctx context.Context) (PCMFrame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := callerContextError(ctx); err != nil {
		return PCMFrame{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pendingAudio) > 0 {
		frame := r.pendingAudio[0]
		r.pendingAudio = r.pendingAudio[1:]
		return frame, nil
	}
	for {
		channel, payload, err := r.readPacketContext(ctx)
		if err != nil {
			if contextErr := callerContextError(ctx); contextErr != nil {
				return PCMFrame{}, contextErr
			}
			return PCMFrame{}, err
		}
		if channel != r.audioChannel {
			if channel == r.videoChannel && len(payload) > 0 {
				r.pendingVisuals = append(r.pendingVisuals, r.visualObservation(payload))
			}
			continue
		}
		samples := decodeAudio(r.codec, payload)
		if len(samples) > 0 {
			return PCMFrame{Samples: samples}, nil
		}
	}
}

func (r *rtspInbound) Look(ctx context.Context) (VisualObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r.videoChannel < 0 {
		return unavailableVisual(r.source), nil
	}
	if err := callerContextError(ctx); err != nil {
		return VisualObservation{}, err
	}
	lookCtx, cancel := boundedVisualContext(ctx)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pendingVisuals) > 0 {
		observation := r.pendingVisuals[0]
		r.pendingVisuals = r.pendingVisuals[1:]
		return observation, nil
	}
	for {
		channel, payload, err := r.readPacketContext(lookCtx)
		if err != nil {
			if contextErr := callerContextError(ctx); contextErr != nil {
				return VisualObservation{}, contextErr
			}
			return unavailableVisual(r.source), nil
		}
		if channel == r.audioChannel {
			samples := decodeAudio(r.codec, payload)
			if len(samples) > 0 {
				r.pendingAudio = append(r.pendingAudio, PCMFrame{Samples: samples})
			}
			continue
		}
		if channel != r.videoChannel || len(payload) == 0 {
			continue
		}
		return r.visualObservation(payload), nil
	}
}

func (r *rtspInbound) visualObservation(payload []byte) VisualObservation {
	return VisualObservation{Source: r.source, Status: VisualObservationAvailable, MediaType: r.videoMediaType, Bytes: append([]byte(nil), payload...)}
}

func (r *rtspInbound) readPacketContext(ctx context.Context) (int, []byte, error) {
	if err := callerContextError(ctx); err != nil {
		return 0, nil, err
	}
	if r.client == nil || r.client.reader == nil {
		return 0, nil, errors.New("RTSP client is not initialized")
	}
	stop := make(chan struct{})
	if r.client.conn != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = r.client.conn.SetReadDeadline(time.Now())
			case <-stop:
			}
		}()
	}
	defer close(stop)
	return r.readPacket()
}

func (r *rtspInbound) readPacket() (int, []byte, error) {
	marker, err := r.client.reader.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	if marker != '$' {
		return 0, nil, errors.New("invalid RTSP interleaved packet")
	}
	header := make([]byte, 3)
	if _, err = io.ReadFull(r.client.reader, header); err != nil {
		return 0, nil, err
	}
	rtpBytes := make([]byte, int(binary.BigEndian.Uint16(header[1:])))
	if _, err = io.ReadFull(r.client.reader, rtpBytes); err != nil {
		return 0, nil, err
	}
	var packet rtp.Packet
	if err = packet.Unmarshal(rtpBytes); err != nil {
		return 0, nil, fmt.Errorf("invalid interleaved RTP packet: %w", err)
	}
	return int(header[0]), packet.Payload, nil
}
func (r *rtspInbound) Close() error {
	r.once.Do(func() {
		if r.client != nil && r.client.conn != nil {
			_ = r.client.conn.Close()
		}
	})
	return nil
}

func decodeAudio(codec string, payload []byte) []int16 {
	if len(payload) == 0 {
		return nil
	}
	codec = strings.TrimPrefix(strings.ToUpper(codec), "AUDIO/")
	if codec == "L16" || codec == "PCM16" || codec == "RAW" {
		out := make([]int16, len(payload)/2)
		for i := range out {
			out[i] = int16(binary.BigEndian.Uint16(payload[i*2:]))
		}
		return out
	}
	out := make([]int16, len(payload))
	for i, value := range payload {
		if codec == "PCMA" || codec == "G711A" {
			out[i] = alaw(value)
		} else if codec == "PCMU" || codec == "G711U" {
			out[i] = ulaw(value)
		} else if i*2+1 < len(payload) {
			out[i] = int16(binary.BigEndian.Uint16(payload[i*2:]))
		} else {
			out[i] = int16(value) << 8
		}
	}
	return out
}
func ulaw(value byte) int16 {
	value = ^value
	sample := int16((value&0x0f)<<3 + 132)
	sample <<= (value & 0x70) >> 4
	if value&0x80 != 0 {
		return 132 - sample
	}
	return sample - 132
}
func alaw(value byte) int16 {
	value ^= 0x55
	sample := int16(value&0x0f) << 4
	if value&0x70 != 0 {
		sample += 0x100
		sample <<= (value&0x70)>>4 - 1
	}
	if value&0x80 != 0 {
		return sample
	}
	return -sample
}
