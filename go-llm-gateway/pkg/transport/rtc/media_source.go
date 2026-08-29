package rtc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
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
