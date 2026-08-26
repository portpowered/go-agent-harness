package integration

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// The v10 WebRTC external-source vertical speaks the go2rtc signaling dialect
// (`GET /api/ws?src=<name>`, JSON {"type":"webrtc/offer"} / "webrtc/answer")
// and presents deterministic G.711 μ-law audio plus, for the camera shape, an
// H.264 video track. Source audio is derived from the committed corpus
// utterance utt_short_16k.wav so both the media-observation leg and the
// session leg consume the same voiced content.
const (
	// externalSourceHardDeadline bounds every v10 leg: negotiation, media
	// observation, and the replay-backed session run must all finish inside
	// this parent deadline or the test fails on the deadline condition.
	externalSourceHardDeadline = 45 * time.Second

	// externalSourceRMSThreshold is the minimum PCM16 RMS energy for audio to
	// count as non-silent; digital silence measures 0 and voiced corpus
	// windows measure well above it (the committed utterance averages ~809).
	externalSourceRMSThreshold = 500.0

	// externalSourcePacketSamples is one 20 ms PCMU packet at 8 kHz.
	externalSourcePacketSamples = 160

	// externalSourcePrefixSamples is the length of the committed 16 kHz
	// utterance window that feeds the source stream (~1.6 s), keeping the
	// paced session leg fast enough for the PR-tier budget. The window is
	// the loudest one in the file so the source carries genuinely voiced
	// content rather than the leading silence padding.
	externalSourcePrefixSamples = 25600

	// externalSourceDecimation reduces the 16 kHz corpus to the source's
	// 8 kHz PCMU rate by taking every fourth sample.
	externalSourceDecimation = 4

	externalSourceSessionID = "sess_v10_webrtc_external_source"

	cameraReplyTranscript     = "The camera feed looks clear today."
	audioOnlyReplyTranscript  = "I heard you, but there is no camera to inspect."
	cameraTranscriptTail      = " Clear."
	audioOnlyTranscriptTail   = " No video."
	externalSourceReplyWindow = 9600
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
	path            string
	source          string
	frameCount      int
	videoFrameCount int

	negotiated     chan struct{}
	frameDelivered chan struct{}

	negotiatedOnce, frameOnce sync.Once
}

func (o *webrtcSourceObservation) recordVideoFrame() {
	o.Lock()
	o.videoFrameCount++
	o.Unlock()
}

func startWebrtcSourceFixture(t *testing.T, opts webrtcSourceOptions) (string, *webrtcSourceObservation, func()) {
	t.Helper()
	observed := &webrtcSourceObservation{
		negotiated:     make(chan struct{}),
		frameDelivered: make(chan struct{}),
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
		observed.Lock()
		observed.frameCount++
		observed.Unlock()
		observed.frameOnce.Do(func() { close(observed.frameDelivered) })
	}
	for i := 0; i < 3 && video != nil; i++ {
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

// stripSDPMediaSection removes one media section (its m-line and every
// following attribute line) from an SDP body while keeping the trailing CRLF
// the SDP parser requires.
func stripSDPMediaSection(sdp, media string) string {
	lines := splitSDPLines(sdp)
	out := make([]string, 0, len(lines))
	inTarget := false
	for _, line := range lines {
		if len(line) >= 2 && line[:2] == "m=" {
			fields := fieldsOf(line[2:])
			inTarget = len(fields) > 0 && fields[0] == media
		}
		if !inTarget {
			out = append(out, line)
		}
	}
	joined := joinSDPLines(out)
	return joined
}

func splitSDPLines(sdp string) []string {
	lines := []string{}
	start := 0
	for i := 0; i < len(sdp); i++ {
		if sdp[i] == '\n' {
			end := i
			if end > start && sdp[end-1] == '\r' {
				end--
			}
			lines = append(lines, sdp[start:end])
			start = i + 1
		}
	}
	if start < len(sdp) {
		lines = append(lines, sdp[start:])
	}
	return lines
}

func joinSDPLines(lines []string) string {
	joined := ""
	for _, line := range lines {
		joined += line + "\r\n"
	}
	return joined
}

func fieldsOf(value string) []string {
	fields := []string{}
	current := ""
	for i := 0; i <= len(value); i++ {
		if i == len(value) || value[i] == ' ' || value[i] == '\t' {
			if current != "" {
				fields = append(fields, current)
				current = ""
			}
			continue
		}
		current += string(value[i])
	}
	return fields
}

// committedCorpusWAVPath locates a committed corpus WAV under
// go-agent-loop/testdata/audio so the source stream reuses the existing
// fixture instead of adding new binary assets.
func committedCorpusWAVPath(t *testing.T, name string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve corpus fixture path: runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "go-agent-loop", "testdata", "audio", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("committed corpus WAV %s not found: %v", name, err)
	}
	return path
}

// ulawEncodeByte compresses one linear PCM16 sample to G.711 μ-law exactly as
// a camera encoder would; the production decoder under test expands it back.
func ulawEncodeByte(sample int16) byte {
	const bias = 0x84
	const clip = 32635
	sign := byte(0x00)
	value := int(sample)
	if value < 0 {
		sign = 0x80
		value = -value
	}
	if value > clip {
		value = clip
	}
	value += bias
	exponent := 7
	for mask := 0x4000; exponent > 0 && value&mask == 0; mask >>= 1 {
		exponent--
	}
	mantissa := byte((value >> uint(exponent+3)) & 0x0f)
	return ^(sign | byte(exponent)<<4 | mantissa)
}

// loudestSampleWindow returns the highest-energy contiguous sample window of
// the given length (scanned at a fixed stride), so fixture streams derived
// from the committed corpus carry voiced content deterministically.
func loudestSampleWindow(samples []int16, window int) []int16 {
	if window >= len(samples) {
		return samples
	}
	bestStart, bestEnergy := 0, -1.0
	for start := 0; start+window <= len(samples); start += 800 {
		var energy float64
		for _, sample := range samples[start : start+window] {
			energy += float64(sample) * float64(sample)
		}
		if energy > bestEnergy {
			bestEnergy = energy
			bestStart = start
		}
	}
	out := make([]int16, window)
	copy(out, samples[bestStart:bestStart+window])
	return out
}

// cameraSourceRTPPackets derives the deterministic PCMU packet stream from the
// committed utt_short_16k.wav corpus utterance: its loudest fixed 1.6 s
// window is decimated to the source's 8 kHz rate and μ-law encoded into
// 20 ms packets.
func cameraSourceRTPPackets(t *testing.T) [][]byte {
	t.Helper()
	wavBytes, err := os.ReadFile(committedCorpusWAVPath(t, "utt_short_16k.wav"))
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	if rate != 16000 {
		t.Fatalf("committed corpus WAV rate = %d, want 16000", rate)
	}
	window := loudestSampleWindow(samples, externalSourcePrefixSamples)
	downsampled := make([]int16, 0, len(window)/externalSourceDecimation+1)
	for i := 0; i < len(window); i += externalSourceDecimation {
		downsampled = append(downsampled, window[i])
	}
	packets := make([][]byte, 0, (len(downsampled)+externalSourcePacketSamples-1)/externalSourcePacketSamples)
	payload := make([]byte, 0, len(downsampled))
	for _, sample := range downsampled {
		payload = append(payload, ulawEncodeByte(sample))
	}
	for start := 0; start < len(payload); start += externalSourcePacketSamples {
		end := start + externalSourcePacketSamples
		if end > len(payload) {
			end = len(payload)
		}
		chunk := make([]byte, end-start)
		copy(chunk, payload[start:end])
		packets = append(packets, chunk)
	}
	return packets
}

// rawPCMAppendFrames chunks a raw PCM16 byte stream into the FrameSize-sample
// frames the session audio source emits over the wire, zero-padding the final
// short frame exactly as documented for raw stdin input.
func rawPCMAppendFrames(pcm []byte, frameBytes int) [][]byte {
	frames := make([][]byte, 0, (len(pcm)+frameBytes-1)/frameBytes)
	for start := 0; start < len(pcm); start += frameBytes {
		frame := make([]byte, frameBytes)
		copy(frame, pcm[start:])
		frames = append(frames, frame)
	}
	return frames
}

// pcmRMSEnergy computes the linear RMS energy of a PCM16 little-endian stream.
func pcmRMSEnergy(pcm []byte) float64 {
	count := len(pcm) / 2
	if count == 0 {
		return 0
	}
	var sum float64
	for i := 0; i+1 < len(pcm); i += 2 {
		sample := int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8)
		sum += float64(sample) * float64(sample)
	}
	return math.Sqrt(sum / float64(count))
}

// loudestUtteranceWindow returns the highest-energy contiguous window of the
// committed utterance so the scripted reply mirrors genuinely voiced content.
func loudestUtteranceWindow(t *testing.T, window int) []int16 {
	t.Helper()
	wavBytes, err := os.ReadFile(committedCorpusWAVPath(t, "utt_short_16k.wav"))
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	if rate != 16000 {
		t.Fatalf("committed corpus WAV rate = %d, want 16000", rate)
	}
	if window <= 0 || window > len(samples) {
		t.Fatalf("reply window %d out of range for %d samples", window, len(samples))
	}
	bestStart, bestEnergy := 0, -1.0
	for start := 0; start+window <= len(samples); start += 800 {
		var energy float64
		for _, sample := range samples[start : start+window] {
			energy += float64(sample) * float64(sample)
		}
		if energy > bestEnergy {
			bestEnergy = energy
			bestStart = start
		}
	}
	reply := make([]int16, window)
	copy(reply, samples[bestStart:bestStart+window])
	return reply
}

func pcm16LEBytesOf(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(sample))
	}
	return data
}
