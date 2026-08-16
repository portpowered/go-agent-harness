package localai

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// realtimeOverallTimeout bounds one complete live protocol proof. The
	// endpoint helper has a shorter readiness timeout; a running local model may
	// still need several seconds to generate its first response.
	realtimeOverallTimeout   = 30 * time.Second
	realtimeOperationTimeout = 5 * time.Second

	pcmSampleRate       = 16000
	pcmOutputSampleRate = 22050
	pcmChunkSamples     = pcmSampleRate / 10 // 100 ms per append event.

	// pcmSilenceRMSThreshold is the normalized int16 RMS floor used by the
	// live proof. A response at or below this level is treated as silence.
	pcmSilenceRMSThreshold = 0.01
)

// realtimeAudioProof contains observable evidence from one raw realtime
// protocol exchange.
type realtimeAudioProof struct {
	AudioDeltaCount   int
	AudioBytes        int
	AudioRMS          float64
	FirstAudioLatency time.Duration
	TotalDuration     time.Duration
}

// verifyRealtimeAudio performs one bounded raw WebSocket audio round trip.
// It is intentionally separate from Endpoint so negative-control tests can
// exercise the protocol verifier without the endpoint absence cache.
func verifyRealtimeAudio(endpoint string) (realtimeAudioProof, error) {
	ctx, cancel := context.WithTimeout(context.Background(), realtimeOverallTimeout)
	defer cancel()
	return verifyRealtimeAudioContext(ctx, endpoint)
}

func verifyRealtimeAudioContext(ctx context.Context, endpoint string) (realtimeAudioProof, error) {
	started := time.Now()
	var proof realtimeAudioProof

	dialer := websocket.Dialer{HandshakeTimeout: realtimeOperationTimeout}
	conn, response, err := dialer.DialContext(ctx, endpoint, http.Header{})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return proof, fmt.Errorf("dial realtime endpoint %s: %w", endpoint, err)
	}
	if conn == nil {
		return proof, errors.New("realtime WebSocket dial returned a nil connection")
	}
	defer func() { _ = conn.Close() }()

	if err := waitForRealtimeEvent(ctx, conn, "session.created"); err != nil {
		return proof, fmt.Errorf("wait for session.created: %w", err)
	}

	if err := writeRealtimeEvent(ctx, conn, map[string]any{
		"type": "session.update",
		"session": map[string]any{
			"type":              "realtime",
			"output_modalities": []string{"audio"},
			"instructions":      "Reply with one short spoken sentence.",
			"audio": map[string]any{
				"input": map[string]any{
					"format":         map[string]any{"type": "audio/pcm", "rate": pcmSampleRate},
					"turn_detection": nil,
				},
				"output": map[string]any{
					"format": map[string]any{"type": "audio/pcm", "rate": pcmOutputSampleRate},
				},
			},
		},
	}); err != nil {
		return proof, fmt.Errorf("send session.update: %w", err)
	}
	if err := waitForRealtimeEvent(ctx, conn, "session.updated"); err != nil {
		return proof, fmt.Errorf("wait for session.updated: %w", err)
	}

	audio := deterministicPCM16Utterance()
	for start := 0; start < len(audio); start += pcmChunkSamples * 2 {
		end := start + pcmChunkSamples*2
		if end > len(audio) {
			end = len(audio)
		}
		if err := writeRealtimeEvent(ctx, conn, map[string]any{
			"type":  "input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(audio[start:end]),
		}); err != nil {
			return proof, fmt.Errorf("append PCM16 audio at byte %d: %w", start, err)
		}
	}

	if err := writeRealtimeEvent(ctx, conn, map[string]any{
		"type": "input_audio_buffer.commit",
	}); err != nil {
		return proof, fmt.Errorf("commit PCM16 audio: %w", err)
	}

	turnCommittedAt := time.Now()

	var decodedAudio []byte
	for {
		event, messageType, err := readRealtimeEvent(ctx, conn)
		if err != nil {
			return proof, fmt.Errorf("read realtime response: %w", err)
		}
		if messageType != websocket.TextMessage {
			continue
		}

		switch event.Type {
		case "error":
			return proof, fmt.Errorf("server error: %s", eventErrorMessage(event.Payload))
		case "response.output_audio.delta", "response.audio.delta":
			if event.Delta == "" {
				return proof, errors.New("server sent an empty audio delta")
			}
			chunk, err := base64.StdEncoding.DecodeString(event.Delta)
			if err != nil {
				return proof, fmt.Errorf("decode audio delta: %w", err)
			}
			if len(chunk) == 0 {
				return proof, errors.New("server sent a zero-byte audio delta")
			}
			if len(chunk)%2 != 0 {
				return proof, fmt.Errorf("audio delta has odd PCM16 byte count %d", len(chunk))
			}
			if proof.AudioDeltaCount == 0 {
				proof.FirstAudioLatency = time.Since(turnCommittedAt)
			}
			proof.AudioDeltaCount++
			decodedAudio = append(decodedAudio, chunk...)
		case "response.output_audio.done", "response.audio.done", "response.done":
			if len(decodedAudio) == 0 {
				return proof, fmt.Errorf("%s arrived without an audio delta", event.Type)
			}
			proof.AudioBytes = len(decodedAudio)
			proof.AudioRMS, err = pcm16RMS(decodedAudio)
			if err != nil {
				return proof, fmt.Errorf("calculate decoded PCM16 RMS: %w", err)
			}
			if proof.AudioRMS <= pcmSilenceRMSThreshold {
				return proof, fmt.Errorf("decoded PCM16 RMS %.6f is at or below silence threshold %.6f", proof.AudioRMS, pcmSilenceRMSThreshold)
			}
			proof.TotalDuration = time.Since(started)
			return proof, nil
		}
	}
}

type realtimeEvent struct {
	Type    string
	Delta   string
	Payload json.RawMessage
}

func waitForRealtimeEvent(ctx context.Context, conn *websocket.Conn, want string) error {
	for {
		event, messageType, err := readRealtimeEvent(ctx, conn)
		if err != nil {
			return err
		}
		if messageType != websocket.TextMessage {
			continue
		}
		if event.Type == "error" {
			return fmt.Errorf("server error: %s", eventErrorMessage(event.Payload))
		}
		if event.Type == want {
			return nil
		}
	}
}

func readRealtimeEvent(ctx context.Context, conn *websocket.Conn) (realtimeEvent, int, error) {
	if err := setReadDeadline(ctx, conn); err != nil {
		return realtimeEvent{}, 0, err
	}
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		return realtimeEvent{}, messageType, err
	}
	if messageType != websocket.TextMessage {
		return realtimeEvent{}, messageType, nil
	}

	event := realtimeEvent{Payload: append(json.RawMessage(nil), payload...)}
	var envelope struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return realtimeEvent{}, messageType, fmt.Errorf("decode JSON event: %w", err)
	}
	if envelope.Type == "" {
		return realtimeEvent{}, messageType, errors.New("realtime event is missing type")
	}
	event.Type = envelope.Type
	event.Delta = envelope.Delta
	return event, messageType, nil
}

func writeRealtimeEvent(ctx context.Context, conn *websocket.Conn, event map[string]any) error {
	if err := setWriteDeadline(ctx, conn); err != nil {
		return err
	}
	return conn.WriteJSON(event)
}

func setReadDeadline(ctx context.Context, conn *websocket.Conn) error {
	return setConnectionDeadline(ctx, conn.SetReadDeadline)
}

func setWriteDeadline(ctx context.Context, conn *websocket.Conn) error {
	return setConnectionDeadline(ctx, conn.SetWriteDeadline)
}

func setConnectionDeadline(ctx context.Context, setDeadline func(time.Time) error) error {
	deadline := time.Now().Add(realtimeOperationTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if !deadline.After(time.Now()) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return context.DeadlineExceeded
	}
	return setDeadline(deadline)
}

func eventErrorMessage(payload json.RawMessage) string {
	var event struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return string(payload)
	}
	if event.Error.Message != "" {
		return event.Error.Message
	}
	if event.Message != "" {
		return event.Message
	}
	if event.Error.Type != "" || event.Error.Code != "" {
		return stringsJoinNonEmpty(event.Error.Type, event.Error.Code)
	}
	return string(payload)
}

func stringsJoinNonEmpty(values ...string) string {
	result := ""
	for _, value := range values {
		if value == "" {
			continue
		}
		if result != "" {
			result += ": "
		}
		result += value
	}
	return result
}

func pcm16RMS(audio []byte) (float64, error) {
	if len(audio) == 0 {
		return 0, errors.New("PCM16 audio is empty")
	}
	if len(audio)%2 != 0 {
		return 0, fmt.Errorf("PCM16 audio has odd byte count %d", len(audio))
	}

	var sumSquares float64
	for offset := 0; offset < len(audio); offset += 2 {
		sample := float64(int16(binary.LittleEndian.Uint16(audio[offset:]))) / math.MaxInt16
		sumSquares += sample * sample
	}
	return math.Sqrt(sumSquares / float64(len(audio)/2)), nil
}
