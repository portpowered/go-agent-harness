// This file contains session capture replay execution and inspection helpers used to select and validate replay behavior.
package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	sessionClosedEventType          = "session.closed"
	sessionUpdateEventType          = "session.update"
	conversationItemCreateEventType = "conversation.item.create"
	responseCreateEventType         = "response.create"
	inputAudioBufferAppendEventType = "input_audio_buffer.append"
	inputAudioBufferCommitEventType = "input_audio_buffer.commit"
)

// replaySessionConfiguration is the provider-facing configuration captured in
// the first outbound session.update. The raw payload is deliberately retained
// instead of projected into the live workspace's SessionConfig: provider wire
// versions may carry fields (for example GA audio or output_modalities) that
// the shared config does not model yet.
type replaySessionConfiguration struct {
	payload               []byte
	model                 string
	inputAudioSampleRate  int
	outputAudioSampleRate int
}

// loadReplaySessionConfiguration validates and extracts the authoritative
// initial provider handshake before replay starts. A replay can therefore
// fail with a capture-specific setup error rather than opening a session and
// later reporting a misleading outbound mismatch.
func loadReplaySessionConfiguration(path string) (replaySessionConfiguration, error) {
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return replaySessionConfiguration{}, fmt.Errorf("load replay session capture %s: %w", path, err)
	}
	capture := loaded.Capture

	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer || record.Type != sessionUpdateEventType {
			continue
		}

		payload := replayCaptureRecordPayload(record)
		if len(payload) == 0 {
			return replaySessionConfiguration{}, fmt.Errorf(
				"replay session capture %s: initial outbound %s at sequence %d has no payload",
				path, sessionUpdateEventType, record.Sequence,
			)
		}

		var envelope struct {
			Type    string          `json:"type"`
			Session json.RawMessage `json:"session"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return replaySessionConfiguration{}, fmt.Errorf(
				"replay session capture %s: decode initial outbound %s at sequence %d: %w",
				path, sessionUpdateEventType, record.Sequence, err,
			)
		}
		if envelope.Type != sessionUpdateEventType {
			return replaySessionConfiguration{}, fmt.Errorf(
				"replay session capture %s: initial outbound %s at sequence %d has payload type %q",
				path, sessionUpdateEventType, record.Sequence, envelope.Type,
			)
		}
		if len(envelope.Session) == 0 || string(envelope.Session) == "null" {
			return replaySessionConfiguration{}, fmt.Errorf(
				"replay session capture %s: initial outbound %s at sequence %d is missing the session configuration",
				path, sessionUpdateEventType, record.Sequence,
			)
		}

		var session map[string]json.RawMessage
		if err := json.Unmarshal(envelope.Session, &session); err != nil {
			return replaySessionConfiguration{}, fmt.Errorf(
				"replay session capture %s: decode session configuration at sequence %d: %w",
				path, record.Sequence, err,
			)
		}
		if len(session) == 0 {
			return replaySessionConfiguration{}, fmt.Errorf(
				"replay session capture %s: initial outbound %s at sequence %d has an empty session configuration",
				path, sessionUpdateEventType, record.Sequence,
			)
		}

		model := ""
		if rawModel, ok := session["model"]; ok {
			if err := json.Unmarshal(rawModel, &model); err != nil {
				return replaySessionConfiguration{}, fmt.Errorf(
					"replay session capture %s: session.model at sequence %d must be a string: %w",
					path, record.Sequence, err,
				)
			}
			model = strings.TrimSpace(model)
		}

		inputRate, outputRate := replaySessionAudioSampleRates(session)
		if inputRate > 0 && outputRate > 0 && inputRate != outputRate {
			return replaySessionConfiguration{}, fmt.Errorf(
				"replay session capture %s: %w: input=%d Hz output=%d Hz",
				path, ErrSessionAudioSampleRateConflict, inputRate, outputRate,
			)
		}

		return replaySessionConfiguration{
			payload:               append([]byte(nil), payload...),
			model:                 model,
			inputAudioSampleRate:  inputRate,
			outputAudioSampleRate: outputRate,
		}, nil
	}

	return replaySessionConfiguration{}, fmt.Errorf(
		"replay session capture %s: missing initial outbound %s configuration",
		path, sessionUpdateEventType,
	)
}

// replaySessionAudioSampleRates extracts both provider-declared directions
// from the current GA audio format objects or provider extensions carrying the
// same objects under the legacy field names. Missing rates remain unspecified.
func replaySessionAudioSampleRates(session map[string]json.RawMessage) (int, int) {
	var audio struct {
		Input struct {
			Format struct {
				Rate int `json:"rate"`
			} `json:"format"`
		} `json:"input"`
		Output struct {
			Format struct {
				Rate int `json:"rate"`
			} `json:"format"`
		} `json:"output"`
	}
	if raw, ok := session["audio"]; ok {
		_ = json.Unmarshal(raw, &audio)
	}

	inputRate := audio.Input.Format.Rate
	outputRate := audio.Output.Format.Rate
	var format struct {
		Rate int `json:"rate"`
	}
	if inputRate <= 0 {
		if raw, ok := session["input_audio_format"]; ok && json.Unmarshal(raw, &format) == nil {
			inputRate = format.Rate
		}
	}
	format.Rate = 0
	if outputRate <= 0 {
		if raw, ok := session["output_audio_format"]; ok && json.Unmarshal(raw, &format) == nil {
			outputRate = format.Rate
		}
	}
	return inputRate, outputRate
}

// replayCapturedPrompt is the one prompt shape that the ordinary OpenAI
// session loop can reproduce without inventing a wire event: a single user
// input_text item followed by one response.create. Keeping this as an
// explicit, narrow shape preserves the established behavior for audio,
// image, tool, and multi-turn captures.
type replayCapturedPrompt struct {
	text string
}

// loadReplaySessionPrompt finds the initial text turn in a raw capture. It is
// intentionally called only for a bare replay. Explicit prompts must continue
// to reach the strict replay dialer so changed, missing, or extra outbound
// frames retain their existing mismatch errors.
func loadReplaySessionPrompt(path string) (*replayCapturedPrompt, error) {
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return nil, fmt.Errorf("load replay session capture %s: %w", path, err)
	}
	capture := loaded.Capture

	clientActions := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records))
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer || record.Type == sessionUpdateEventType {
			continue
		}
		clientActions = append(clientActions, record)
	}

	var prompt *replayCapturedPrompt
	promptPosition := -1
	for position, record := range clientActions {
		if record.Type != conversationItemCreateEventType {
			continue
		}
		text, isTextPrompt, err := parseReplayTextPrompt(path, record)
		if err != nil {
			return nil, err
		}
		if !isTextPrompt {
			continue
		}
		if prompt != nil {
			return nil, fmt.Errorf(
				"replay session capture %s has ambiguous recorded text prompts at sequences %d and %d",
				path, clientActions[promptPosition].Sequence, record.Sequence,
			)
		}
		prompt = &replayCapturedPrompt{text: text}
		promptPosition = position
	}
	if prompt == nil {
		return nil, nil
	}
	if promptPosition != 0 {
		return nil, fmt.Errorf(
			"replay session capture %s has an ambiguous recorded prompt at sequence %d: it must be the first client action after session.update",
			path, clientActions[promptPosition].Sequence,
		)
	}
	if promptPosition+1 >= len(clientActions) || clientActions[promptPosition+1].Type != responseCreateEventType {
		return nil, fmt.Errorf(
			"replay session capture %s has an incomplete recorded prompt at sequence %d: expected the next client action to be %s",
			path, clientActions[promptPosition].Sequence, responseCreateEventType,
		)
	}
	if promptPosition+2 != len(clientActions) {
		return nil, fmt.Errorf(
			"replay session capture %s has an ambiguous recorded prompt at sequence %d: expected only %s after it",
			path, clientActions[promptPosition].Sequence, responseCreateEventType,
		)
	}
	if err := validateReplayEventPayload(path, clientActions[promptPosition+1], responseCreateEventType); err != nil {
		return nil, err
	}
	return prompt, nil
}

// loadReplaySessionAudioTurns finds the recorded scheduled-audio-turn shape
// used by --audio-in-turn/--record-dir captures: one or more recorded
// input_audio_buffer.append events, an input_audio_buffer.commit, and a
// response.create, repeated once per spoken turn. It reconstructs each turn's
// raw PCM directly from the capture so a bare replay never needs the caller
// to re-supply the original audio files: the recorded client frames alone
// fully drive the replay, per the same reuse-recorded-frames principle
// loadReplaySessionPrompt already applies to the single-text-prompt shape.
//
// It is intentionally called only for a bare replay with no user-supplied
// audio turns. An explicit --audio-in-turn re-supply continues to reach the
// strict replay dialer for its own outbound validation instead of this
// reconstruction.
func loadReplaySessionAudioTurns(path string) ([]ScheduledAudioInput, error) {
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return nil, fmt.Errorf("load replay session capture %s: %w", path, err)
	}
	capture := loaded.Capture

	clientActions := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records))
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer || record.Type == sessionUpdateEventType {
			continue
		}
		clientActions = append(clientActions, record)
	}
	if len(clientActions) == 0 || clientActions[0].Type != inputAudioBufferAppendEventType {
		// Not the scheduled-audio-turn shape; let the caller fall back to the
		// text-prompt shape or explicit replay validation.
		return nil, nil
	}

	var turns []ScheduledAudioInput
	position := 0
	for position < len(clientActions) {
		turnIndex := len(turns) + 1
		record := clientActions[position]
		if record.Type != inputAudioBufferAppendEventType {
			return nil, fmt.Errorf(
				"replay session capture %s has an ambiguous recorded client action at sequence %d: expected %s to begin audio turn %d",
				path, record.Sequence, inputAudioBufferAppendEventType, turnIndex,
			)
		}
		var pcm bytes.Buffer
		for position < len(clientActions) && clientActions[position].Type == inputAudioBufferAppendEventType {
			chunk, chunkErr := parseReplayAudioAppendPCM(path, clientActions[position])
			if chunkErr != nil {
				return nil, chunkErr
			}
			pcm.Write(chunk)
			position++
		}
		if position >= len(clientActions) {
			return nil, fmt.Errorf(
				"replay session capture %s has an incomplete recorded audio turn %d: expected %s after its recorded %s event(s)",
				path, turnIndex, inputAudioBufferCommitEventType, inputAudioBufferAppendEventType,
			)
		}
		if err := validateReplayEventPayload(path, clientActions[position], inputAudioBufferCommitEventType); err != nil {
			return nil, err
		}
		position++
		if position >= len(clientActions) {
			return nil, fmt.Errorf(
				"replay session capture %s has an incomplete recorded audio turn %d: expected %s after its recorded %s",
				path, turnIndex, responseCreateEventType, inputAudioBufferCommitEventType,
			)
		}
		if err := validateReplayEventPayload(path, clientActions[position], responseCreateEventType); err != nil {
			return nil, err
		}
		position++
		if pcm.Len() == 0 {
			return nil, fmt.Errorf("replay session capture %s has an empty recorded audio turn %d", path, turnIndex)
		}
		turns = append(turns, ScheduledAudioInput{
			AfterCompletedTurns: len(turns),
			PCM:                 pcm.Bytes(),
			EndOfTurn:           true,
		})
	}
	return turns, nil
}

// parseReplayAudioAppendPCM decodes the raw PCM carried by one recorded
// input_audio_buffer.append event.
func parseReplayAudioAppendPCM(path string, record gwtesting.CapturedSessionEvent) ([]byte, error) {
	payload := replayCaptureRecordPayload(record)
	if len(payload) == 0 {
		return nil, fmt.Errorf(
			"replay session capture %s: recorded %s at sequence %d has no payload",
			path, inputAudioBufferAppendEventType, record.Sequence,
		)
	}
	var envelope struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf(
			"replay session capture %s: decode recorded %s at sequence %d: %w",
			path, inputAudioBufferAppendEventType, record.Sequence, err,
		)
	}
	if envelope.Type != inputAudioBufferAppendEventType {
		return nil, fmt.Errorf(
			"replay session capture %s: recorded %s at sequence %d has payload type %q",
			path, inputAudioBufferAppendEventType, record.Sequence, envelope.Type,
		)
	}
	if envelope.Audio == "" {
		return nil, fmt.Errorf(
			"replay session capture %s: recorded %s at sequence %d is missing its audio payload",
			path, inputAudioBufferAppendEventType, record.Sequence,
		)
	}
	pcm, err := codec.DecodeBase64(envelope.Audio)
	if err != nil {
		return nil, fmt.Errorf(
			"replay session capture %s: decode base64 audio at sequence %d: %w",
			path, record.Sequence, err,
		)
	}
	return pcm, nil
}

func parseReplayTextPrompt(path string, record gwtesting.CapturedSessionEvent) (string, bool, error) {
	payload := replayCaptureRecordPayload(record)
	if len(payload) == 0 {
		return "", false, fmt.Errorf(
			"replay session capture %s: recorded %s at sequence %d has no payload",
			path, conversationItemCreateEventType, record.Sequence,
		)
	}

	var envelope struct {
		Type string          `json:"type"`
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", false, fmt.Errorf(
			"replay session capture %s: decode recorded %s at sequence %d: %w",
			path, conversationItemCreateEventType, record.Sequence, err,
		)
	}
	if envelope.Type != conversationItemCreateEventType {
		return "", false, fmt.Errorf(
			"replay session capture %s: recorded %s at sequence %d has payload type %q",
			path, conversationItemCreateEventType, record.Sequence, envelope.Type,
		)
	}
	if len(envelope.Item) == 0 || string(envelope.Item) == "null" {
		return "", false, fmt.Errorf(
			"replay session capture %s: recorded %s at sequence %d is missing its item",
			path, conversationItemCreateEventType, record.Sequence,
		)
	}

	var item struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(envelope.Item, &item); err != nil {
		return "", false, fmt.Errorf(
			"replay session capture %s: decode recorded conversation item at sequence %d: %w",
			path, record.Sequence, err,
		)
	}
	// Function-call outputs and other user content are valid session actions,
	// but they are not reproducible from the plain text prompt loop.
	if item.Type != "message" || item.Role != "user" {
		return "", false, nil
	}
	if len(item.Content) == 0 {
		return "", false, fmt.Errorf(
			"replay session capture %s: recorded user message at sequence %d has no content",
			path, record.Sequence,
		)
	}
	for _, part := range item.Content {
		if part.Type != "input_text" {
			return "", false, nil
		}
	}
	if len(item.Content) != 1 || item.Content[0].Text == nil {
		return "", false, fmt.Errorf(
			"replay session capture %s: recorded user message at sequence %d must contain exactly one input_text part with text",
			path, record.Sequence,
		)
	}
	return *item.Content[0].Text, true, nil
}

func validateReplayEventPayload(path string, record gwtesting.CapturedSessionEvent, eventType string) error {
	payload := replayCaptureRecordPayload(record)
	if len(payload) == 0 {
		return fmt.Errorf(
			"replay session capture %s: recorded %s at sequence %d has no payload",
			path, eventType, record.Sequence,
		)
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf(
			"replay session capture %s: decode recorded %s at sequence %d: %w",
			path, eventType, record.Sequence, err,
		)
	}
	if envelope.Type != eventType {
		return fmt.Errorf(
			"replay session capture %s: recorded %s at sequence %d has payload type %q",
			path, eventType, record.Sequence, envelope.Type,
		)
	}
	return nil
}

func replayCaptureRecordPayload(record gwtesting.CapturedSessionEvent) []byte {
	if len(record.Payload) > 0 {
		return record.Payload
	}
	return record.Data
}

func replayLoopMaxDuration(path, timing string) time.Duration {
	const completionGrace = 3 * time.Second
	if normalizedSessionReplayTiming(timing) != sessionReplayTimingRecorded {
		return completionGrace
	}
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil || len(loaded.Capture.Records) < 2 {
		return completionGrace
	}
	first := loaded.Capture.Records[0].TimestampMs
	last := loaded.Capture.Records[len(loaded.Capture.Records)-1].TimestampMs
	if last <= first {
		return completionGrace
	}
	return time.Duration(last-first)*time.Millisecond + completionGrace
}

// replayInitialSessionUpdateDialer lets the provider keep its normal session
// implementation while replacing only the first generated handshake with the
// capture's raw configuration. The wrapped replay dialer still strictly
// validates that replacement and every later outbound frame.
type replayInitialSessionUpdateDialer struct {
	inner               transport.Dialer
	payload             []byte
	waitForNextOutbound func() error
}

var _ transport.Dialer = (*replayInitialSessionUpdateDialer)(nil)

func newReplayInitialSessionUpdateDialer(inner transport.Dialer, configuration replaySessionConfiguration, paceOutbound ...bool) transport.Dialer {
	var waitForNextOutbound func() error
	if len(paceOutbound) > 0 && paceOutbound[0] {
		if pacer, ok := inner.(gwtesting.ReplayOutboundPacer); ok {
			waitForNextOutbound = pacer.WaitForNextOutbound
		}
	}
	return &replayInitialSessionUpdateDialer{
		inner:               inner,
		payload:             append([]byte(nil), configuration.payload...),
		waitForNextOutbound: waitForNextOutbound,
	}
}

func (d *replayInitialSessionUpdateDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.inner == nil {
		return nil, fmt.Errorf("replay initial session update dialer requires an inner dialer")
	}
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		return nil, err
	}
	return &replayInitialSessionUpdateConn{
		inner:               conn,
		payload:             append([]byte(nil), d.payload...),
		waitForNextOutbound: d.waitForNextOutbound,
	}, nil
}

type replayInitialSessionUpdateConn struct {
	inner               transport.Conn
	payload             []byte
	waitForNextOutbound func() error
	writeMu             sync.Mutex
	handshakeOn         bool
}

var _ transport.Conn = (*replayInitialSessionUpdateConn)(nil)

func (c *replayInitialSessionUpdateConn) ReadMessage() (int, []byte, error) {
	return c.inner.ReadMessage()
}

func (c *replayInitialSessionUpdateConn) WriteMessage(messageType int, payload []byte) error {
	// ConnectSession sends the provider-specific initial session.update before
	// starting any provider write loop. Serializing writes here also preserves
	// that guarantee for custom test transports.
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.waitForNextOutbound != nil {
		if err := c.waitForNextOutbound(); err != nil {
			return err
		}
	}

	if !c.handshakeOn {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return fmt.Errorf("replay provider initial event is not valid JSON: %w", err)
		}
		if envelope.Type != sessionUpdateEventType {
			return fmt.Errorf("replay provider expected initial %s, got %q", sessionUpdateEventType, envelope.Type)
		}
		payload = append([]byte(nil), c.payload...)
		c.handshakeOn = true
	}
	return c.inner.WriteMessage(messageType, payload)
}

func (c *replayInitialSessionUpdateConn) Close() error {
	return c.inner.Close()
}

func replaySessionCapture(ctx context.Context, out io.Writer, path string) error {
	renderer := newSessionReplayRenderer(out, sessionTerminalReporterFromContext(ctx))
	replayer, err := gwtesting.NewSessionReplayer(path, gwtesting.WithReplayOutboundValidation(false), gwtesting.WithReplayContext(ctx))
	if err != nil {
		return fmt.Errorf("replay session capture %s: %w", path, err)
	}
	for {
		select {
		case <-ctx.Done():
			_ = replayer.Close()
			return ctx.Err()
		case <-replayer.Done():
			return drainSessionReplayMessages(renderer, replayer)
		case msg, ok := <-replayer.Receive().Chan():
			if !ok {
				continue
			}
			if err := writeSessionReplayMessage(renderer, msg); err != nil {
				return err
			}
		}
	}
}

func drainSessionReplayMessages(out io.Writer, replayer *gwtesting.SessionReplayer) error {
	for {
		msg, ok := replayer.Receive().Read()
		if !ok {
			return nil
		}
		if err := writeSessionReplayMessage(out, msg); err != nil {
			return err
		}
	}
}

func grokReplayCaptureHasSessionClose(path string) bool {
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return false
	}
	capture := loaded.Capture
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionServerToClient && record.Type == "session.closed" {
			return true
		}
	}
	return false
}

func usesWebSocketCapture(path string) bool {
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return false
	}
	capture := loaded.Capture
	for _, record := range capture.Records {
		if record.PayloadType == gwtesting.SessionPayloadTypeWebSocketMessage {
			return true
		}
	}
	return false
}

func usesOpenAIWebSocketCapture(path string) bool {
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return false
	}
	capture := loaded.Capture
	return strings.EqualFold(capture.Provider.Name, sessionProviderOpenAI)
}

func captureHasEvent(path string, eventType string) bool {
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return false
	}
	capture := loaded.Capture
	for _, record := range capture.Records {
		if record.Type == eventType {
			return true
		}
	}
	return false
}
