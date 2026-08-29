package rtc

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
)

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
