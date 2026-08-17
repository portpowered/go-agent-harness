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
	DefaultMediaSourceTimeout = 5 * time.Second
	RedactionMarker           = "<redacted>"
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

func operationCause(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
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
	inbound := newPionInbound(func() error { _ = ws.Close(); return pc.Close() })
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Kind() == webrtc.RTPCodecTypeAudio {
			inbound.attach(track)
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
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		_ = inbound.Close()
		return nil, sourceError(SourceErrorUnreachable, s.identity, err)
	}
	caps := (MediaCapabilities{Source: s.identity, AudioCodec: codec, SampleRate: rate, Channels: channels, Video: video}).normalized()
	return &MediaStream{Inbound: inbound, Media: inbound, Capabilities: caps, close: inbound.Close}, nil
}

func parseSDP(sdp string) (audio, video bool, codec string, rate, channels int) {
	section := ""
	for _, raw := range strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "m=audio "):
			audio, section = true, "audio"
		case strings.HasPrefix(line, "m=video "):
			video, section = true, "video"
		case strings.HasPrefix(line, "m="):
			section = ""
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
	if audio && codec == "" {
		codec, rate, channels = "PCMU", 8000, 1
	}
	if channels <= 0 {
		channels = 1
	}
	return
}

type pionInbound struct {
	frames chan PCMFrame
	done   chan struct{}
	once   sync.Once
	close  func() error
	mu     sync.Mutex
	seen   bool
}

func newPionInbound(closeFn func() error) *pionInbound {
	return &pionInbound{frames: make(chan PCMFrame, 8), done: make(chan struct{}), close: closeFn}
}
func (m *pionInbound) attach(track *webrtc.TrackRemote) {
	m.mu.Lock()
	if m.seen {
		m.mu.Unlock()
		return
	}
	m.seen = true
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
	control string
	audio   bool
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
	audioChannel := -1
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
	inbound := &rtspInbound{client: c, audioChannel: audioChannel, codec: codec}
	caps := (MediaCapabilities{Source: s.identity, AudioCodec: codec, SampleRate: rate, Channels: channels, Video: video}).normalized()
	return &MediaStream{Inbound: inbound, Media: inbound, Capabilities: caps, close: inbound.Close}, nil
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
	section := ""
	tracks := []rtspTrack{}
	for _, raw := range strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "m=audio "):
			section = "audio"
		case strings.HasPrefix(line, "m=video "):
			section = "video"
		case strings.HasPrefix(line, "m="):
			section = ""
		case strings.HasPrefix(line, "a=control:") && section != "":
			control := strings.TrimPrefix(line, "a=control:")
			if control != "" && control != "*" {
				tracks = append(tracks, rtspTrack{control: joinRTSPControl(base, fallback, control), audio: section == "audio"})
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
	client       *rtspClient
	audioChannel int
	codec        string
	once         sync.Once
}

func (r *rtspInbound) ReadFrame(ctx context.Context) (PCMFrame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = r.client.conn.SetReadDeadline(time.Now())
		case <-stop:
		}
	}()
	defer close(stop)
	for {
		channel, payload, err := r.readPacket()
		if err != nil {
			if ctx.Err() != nil {
				return PCMFrame{}, ctx.Err()
			}
			return PCMFrame{}, err
		}
		if channel != r.audioChannel {
			continue
		}
		samples := decodeAudio(r.codec, payload)
		if len(samples) > 0 {
			return PCMFrame{Samples: samples}, nil
		}
	}
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
func (r *rtspInbound) Close() error { r.once.Do(func() { _ = r.client.conn.Close() }); return nil }

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
