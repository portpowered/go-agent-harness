//go:build live

package chrome

// Shared plumbing for the opted-in, credentialed live proofs, plus the raw
// provider-capture inspection they validate. Every proof in this build tag
// drives the shipped agent binary against a pinned Chrome and a real Realtime
// session; these helpers keep that plumbing identical across proofs.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	liveProviderOpenAI = "openai"

	liveEventSessionUpdate             = "session.update"
	liveEventConversationItemCreate    = "conversation.item.create"
	liveEventResponseCreate            = "response.create"
	liveEventResponseDone              = "response.done"
	liveEventOutputItemAdded           = "response.output_item.added"
	liveEventFunctionCallArgumentsDone = "response.function_call_arguments.done"
	liveEventOutputAudioTranscriptDone = "response.output_audio_transcript.done"
	liveEventProviderError             = "error"

	liveItemFunctionCall       = "function_call"
	liveItemFunctionCallOutput = "function_call_output"

	liveStatusCancelled        = "cancelled"
	liveReasonDeadlineExceeded = "deadline_exceeded"
	liveMIMETypePNG            = "image/png"
	liveFixtureStatePath       = "/__test/state"
	liveConfigFileName         = "config.yaml"
	liveOpenAIKeyEnvironment   = "AGENT_MODEL__OPENAI__API_KEY="
)

// liveRecordPayload returns a capture record's frame bytes, preferring the
// decoded payload over the raw data.
func liveRecordPayload(record gwtesting.CapturedSessionEvent) []byte {
	if len(record.Payload) == 0 {
		return record.Data
	}
	return record.Payload
}

// latestUncorrelatedCall returns the newest call index that has not received
// its arguments yet and matches callID (any such call when callID is empty),
// or -1 when none does.
func latestUncorrelatedCall(count int, call func(int) (argumentsIndex int, id string), callID string) int {
	for candidate := count - 1; candidate >= 0; candidate-- {
		argumentsIndex, id := call(candidate)
		if argumentsIndex >= 0 {
			continue
		}
		if callID == "" || id == callID {
			return candidate
		}
	}
	return -1
}

// liveTerminalStatus returns the response.done status, which providers put
// either on the response object or on the event itself.
func liveTerminalStatus(responseStatus, eventStatus string) string {
	if responseStatus == "" {
		return eventStatus
	}
	return responseStatus
}

// Gate I2 provider-capture inspection. The measurement in
// gate_i2_realtime_test.go validates the resulting observation.

type gateI2Observation struct {
	Provider              string
	Model                 string
	Instructions          string
	AdvertisedTools       []string
	SessionUpdateCount    int
	SessionUpdateIndex    int
	FirstInputIndex       int
	Calls                 []gateI2Call
	Outputs               []gateI2Output
	ResponseCreates       []int
	AudioDeltas           []gateI2AudioDelta
	SpokenTranscript      string
	SpokenTranscriptIndex int
	AudioBytesAfterInvoke int
	TerminalStatus        string
	TerminalIndex         int
	ProviderErrors        int
}

type gateI2Call struct {
	Index          int
	ArgumentsIndex int
	Name           string
	CallID         string
	Arguments      string
}

type gateI2Output struct {
	Index  int
	CallID string
	Output string
}

type gateI2AudioDelta struct {
	Index int
	Bytes int
}

type gateI2Validation struct {
	ListToolRef   string
	InvokeToolRef string
	RawInputJSON  string
	Reason        string
}

func inspectGateI2Capture(capture gwtesting.SessionCapture) (gateI2Observation, error) {
	observation := gateI2Observation{
		Provider:              capture.Provider.Name,
		Model:                 capture.Provider.Model,
		SessionUpdateIndex:    -1,
		FirstInputIndex:       -1,
		SpokenTranscriptIndex: -1,
		TerminalIndex:         -1,
	}
	for index, record := range capture.Records {
		payload := liveRecordPayload(record)
		if len(payload) == 0 {
			return observation, fmt.Errorf("record %d (%s) has an empty payload", index, record.Type)
		}
		var err error
		switch record.Direction {
		case gwtesting.DirectionClientToServer:
			err = observation.observeClient(index, record.Type, payload)
		case gwtesting.DirectionServerToClient:
			err = observation.observeServer(index, record.Type, payload)
		}
		if err != nil {
			return observation, err
		}
	}
	observation.AudioBytesAfterInvoke = gateI2AudioBytesAfterInvoke(observation)
	return observation, nil
}

func (o *gateI2Observation) observeClient(index int, eventType string, payload []byte) error {
	switch eventType {
	case liveEventSessionUpdate:
		return o.observeSessionUpdate(index, payload)
	case "input_audio_buffer.append":
		if o.FirstInputIndex < 0 {
			o.FirstInputIndex = index
		}
	case liveEventConversationItemCreate:
		var event struct {
			Item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
			} `json:"item"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode conversation.item.create: %w", err)
		}
		if event.Item.Type == liveItemFunctionCallOutput {
			o.Outputs = append(o.Outputs, gateI2Output{Index: index, CallID: event.Item.CallID, Output: event.Item.Output})
		}
	case liveEventResponseCreate:
		o.ResponseCreates = append(o.ResponseCreates, index)
	}
	return nil
}

func (o *gateI2Observation) observeSessionUpdate(index int, payload []byte) error {
	o.SessionUpdateCount++
	var event struct {
		Session struct {
			Instructions string `json:"instructions"`
			Tools        []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"session"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode session.update: %w", err)
	}
	if o.SessionUpdateIndex < 0 {
		o.SessionUpdateIndex = index
	}
	o.Instructions = event.Session.Instructions
	o.AdvertisedTools = o.AdvertisedTools[:0]
	for _, tool := range event.Session.Tools {
		o.AdvertisedTools = append(o.AdvertisedTools, tool.Name)
	}
	return nil
}

func (o *gateI2Observation) observeServer(index int, eventType string, payload []byte) error {
	switch eventType {
	case liveEventOutputItemAdded:
		return o.observeOutputItem(index, payload)
	case liveEventFunctionCallArgumentsDone:
		return o.observeCallArguments(index, payload)
	case liveEventOutputAudioTranscriptDone:
		return o.observeTranscript(index, payload)
	case "response.output_audio.delta", "response.audio.delta":
		return o.observeAudioDelta(index, payload)
	case liveEventResponseDone:
		return o.observeResponseDone(index, payload)
	case liveEventProviderError:
		o.ProviderErrors++
	}
	return nil
}

func (o *gateI2Observation) observeOutputItem(index int, payload []byte) error {
	var event struct {
		Item struct {
			Type   string `json:"type"`
			Name   string `json:"name"`
			CallID string `json:"call_id"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode response.output_item.added: %w", err)
	}
	if event.Item.Type == liveItemFunctionCall {
		o.Calls = append(o.Calls, gateI2Call{Index: index, ArgumentsIndex: -1, Name: event.Item.Name, CallID: event.Item.CallID})
	}
	return nil
}

func (o *gateI2Observation) observeCallArguments(index int, payload []byte) error {
	var event struct {
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode response.function_call_arguments.done: %w", err)
	}
	callIndex := latestUncorrelatedCall(len(o.Calls), func(candidate int) (int, string) {
		return o.Calls[candidate].ArgumentsIndex, o.Calls[candidate].CallID
	}, event.CallID)
	if callIndex < 0 {
		return fmt.Errorf("function-call arguments have no correlating output item call_id=%q", event.CallID)
	}
	call := &o.Calls[callIndex]
	call.ArgumentsIndex = index
	call.Arguments = event.Arguments
	if call.CallID == "" {
		call.CallID = event.CallID
	}
	if call.Name == "" {
		call.Name = event.Name
	}
	return nil
}

func (o *gateI2Observation) observeTranscript(index int, payload []byte) error {
	var event struct {
		Transcript string `json:"transcript"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode output transcript: %w", err)
	}
	text := strings.TrimSpace(event.Transcript)
	if text == "" {
		return nil
	}
	if o.SpokenTranscriptIndex < 0 {
		o.SpokenTranscriptIndex = index
	}
	if o.SpokenTranscript != "" {
		o.SpokenTranscript += " "
	}
	o.SpokenTranscript += text
	return nil
}

func (o *gateI2Observation) observeAudioDelta(index int, payload []byte) error {
	var event struct {
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode output audio delta: %w", err)
	}
	if event.Delta == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(event.Delta)
	if err != nil {
		return fmt.Errorf("decode output audio delta: %w", err)
	}
	o.AudioDeltas = append(o.AudioDeltas, gateI2AudioDelta{Index: index, Bytes: len(decoded)})
	return nil
}

func (o *gateI2Observation) observeResponseDone(index int, payload []byte) error {
	var event struct {
		Status   string `json:"status"`
		Response struct {
			Status string `json:"status"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode response.done: %w", err)
	}
	status := liveTerminalStatus(event.Response.Status, event.Status)
	if o.TerminalIndex < 0 && o.SpokenTranscriptIndex >= 0 && index > o.SpokenTranscriptIndex {
		o.TerminalIndex = index
		o.TerminalStatus = status
	}
	return nil
}

func gateI2AudioBytesAfterInvoke(observation gateI2Observation) int {
	invokeOutputIndex := -1
	for _, call := range observation.Calls {
		if call.Name != webmcp.InvokeToolName || call.CallID == "" {
			continue
		}
		for _, output := range observation.Outputs {
			if output.CallID == call.CallID && output.Index > invokeOutputIndex {
				invokeOutputIndex = output.Index
			}
		}
	}
	if invokeOutputIndex < 0 {
		return 0
	}

	bytes := 0
	for _, delta := range observation.AudioDeltas {
		if delta.Index <= invokeOutputIndex || (observation.TerminalIndex >= 0 && delta.Index >= observation.TerminalIndex) {
			continue
		}
		bytes += delta.Bytes
	}
	return bytes
}

// Cubecade production-session capture inspection. The late-catalog proof in
// cubecade_live_test.go validates the resulting observation.

type cubecadeCaptureObservation struct {
	Provider        string
	Model           string
	SessionUpdates  int
	AdvertisedTools []string
	Calls           []cubecadeCall
	Outputs         []cubecadeOutput
}

type cubecadeCall struct {
	Index          int
	TimestampMS    int64
	ArgumentsIndex int
	Name           string
	CallID         string
	Arguments      string
}

type cubecadeOutput struct {
	Index       int
	TimestampMS int64
	CallID      string
	Envelope    webmcp.ToolResultEnvelope
}

func inspectCubecadeCapture(capture gwtesting.SessionCapture) (cubecadeCaptureObservation, error) {
	observation := cubecadeCaptureObservation{
		Provider: capture.Provider.Name,
		Model:    capture.Provider.Model,
	}
	for index, record := range capture.Records {
		payload := liveRecordPayload(record)
		if len(payload) == 0 {
			return observation, fmt.Errorf("record %d (%s) has an empty payload", index, record.Type)
		}
		var err error
		switch record.Direction {
		case gwtesting.DirectionClientToServer:
			err = observation.observeClient(index, record, payload)
		case gwtesting.DirectionServerToClient:
			err = observation.observeServer(index, record, payload)
		}
		if err != nil {
			return observation, err
		}
	}
	return observation, nil
}

func (o *cubecadeCaptureObservation) observeClient(index int, record gwtesting.CapturedSessionEvent, payload []byte) error {
	switch record.Type {
	case liveEventSessionUpdate:
		o.SessionUpdates++
		var event struct {
			Session struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"session"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode session.update: %w", err)
		}
		o.AdvertisedTools = o.AdvertisedTools[:0]
		for _, tool := range event.Session.Tools {
			o.AdvertisedTools = append(o.AdvertisedTools, tool.Name)
		}
	case liveEventConversationItemCreate:
		var event struct {
			Item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
			} `json:"item"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode function_call_output: %w", err)
		}
		if event.Item.Type != liveItemFunctionCallOutput {
			return nil
		}
		envelope, err := webmcp.UnmarshalToolResult([]byte(event.Item.Output))
		if err != nil {
			return fmt.Errorf("decode tool result for call %q: %w", event.Item.CallID, err)
		}
		o.Outputs = append(o.Outputs, cubecadeOutput{Index: index, TimestampMS: record.TimestampMs, CallID: event.Item.CallID, Envelope: envelope})
	}
	return nil
}

func (o *cubecadeCaptureObservation) observeServer(index int, record gwtesting.CapturedSessionEvent, payload []byte) error {
	switch record.Type {
	case liveEventOutputItemAdded:
		var event struct {
			Item struct {
				Type   string `json:"type"`
				Name   string `json:"name"`
				CallID string `json:"call_id"`
			} `json:"item"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode response.output_item.added: %w", err)
		}
		if event.Item.Type == liveItemFunctionCall {
			o.Calls = append(o.Calls, cubecadeCall{Index: index, TimestampMS: record.TimestampMs, ArgumentsIndex: -1, Name: event.Item.Name, CallID: event.Item.CallID})
		}
	case liveEventFunctionCallArgumentsDone:
		var event struct {
			Name      string `json:"name"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode response.function_call_arguments.done: %w", err)
		}
		callIndex := latestUncorrelatedCall(len(o.Calls), func(candidate int) (int, string) {
			return o.Calls[candidate].ArgumentsIndex, o.Calls[candidate].CallID
		}, event.CallID)
		if callIndex < 0 {
			return fmt.Errorf("function-call arguments have no correlating call_id=%q", event.CallID)
		}
		call := &o.Calls[callIndex]
		call.ArgumentsIndex = index
		call.Arguments = event.Arguments
		if call.CallID == "" {
			call.CallID = event.CallID
		}
		if call.Name == "" {
			call.Name = event.Name
		}
	}
	return nil
}

// Screenshot pixel evidence shared by the selected-page screenshot proof and
// the spoken page-sight proof.

func assertCubecadeScreenshotMarker(t *testing.T, screenshot image.Image, oracle cubecadeSightOracle) int {
	t.Helper()
	want, ok := parseCSSRGB(oracle.MarkerBackground)
	if !ok {
		t.Fatalf("Cubecade marker background = %q, want an RGB color", oracle.MarkerBackground)
	}
	if oracle.ViewportWidth <= 0 || oracle.ViewportHeight <= 0 || oracle.MarkerRect.Width <= 0 || oracle.MarkerRect.Height <= 0 {
		t.Fatalf("Cubecade marker geometry = %+v viewport=%gx%g, want visible marker in viewport", oracle.MarkerRect, oracle.ViewportWidth, oracle.ViewportHeight)
	}
	bounds := screenshot.Bounds()
	scaleX := float64(bounds.Dx()) / oracle.ViewportWidth
	scaleY := float64(bounds.Dy()) / oracle.ViewportHeight
	left := bounds.Min.X + int(math.Floor(oracle.MarkerRect.X*scaleX))
	top := bounds.Min.Y + int(math.Floor(oracle.MarkerRect.Y*scaleY))
	right := bounds.Min.X + int(math.Ceil((oracle.MarkerRect.X+oracle.MarkerRect.Width)*scaleX))
	bottom := bounds.Min.Y + int(math.Ceil((oracle.MarkerRect.Y+oracle.MarkerRect.Height)*scaleY))
	left = max(left, bounds.Min.X)
	top = max(top, bounds.Min.Y)
	right = min(right, bounds.Max.X)
	bottom = min(bottom, bounds.Max.Y)
	if right <= left || bottom <= top {
		t.Fatalf("Cubecade marker rectangle %+v maps outside screenshot bounds %v", oracle.MarkerRect, bounds)
	}

	matches := countScreenshotMarkerPixels(screenshot, image.Rect(left, top, right, bottom), want)
	if matches == 0 {
		t.Fatalf("Cubecade screenshot marker region contained no pixels near computed %s color", oracle.MarkerBackground)
	}
	return matches
}

func parseCSSRGB(value string) ([3]uint8, bool) {
	value = strings.TrimSpace(value)
	open := strings.IndexByte(value, '(')
	close := strings.LastIndexByte(value, ')')
	if open < 0 || close <= open || (!strings.HasPrefix(value, "rgb(") && !strings.HasPrefix(value, "rgba(")) {
		return [3]uint8{}, false
	}
	components := strings.Split(value[open+1:close], ",")
	if len(components) < 3 {
		return [3]uint8{}, false
	}
	var rgb [3]uint8
	for index := range rgb {
		component, err := strconv.Atoi(strings.TrimSpace(components[index]))
		if err != nil || component < 0 || component > 255 {
			return [3]uint8{}, false
		}
		rgb[index] = uint8(component)
	}
	return rgb, true
}

// countScreenshotMarkerPixels counts the pixels in region whose color is
// within tolerance of the marker color.
func countScreenshotMarkerPixels(screenshot image.Image, region image.Rectangle, want [3]uint8) int {
	matches := 0
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			red, green, blue, _ := screenshot.At(x, y).RGBA()
			if closeScreenshotColor(uint8(red>>8), uint8(green>>8), uint8(blue>>8), want) {
				matches++
			}
		}
	}
	return matches
}

func closeScreenshotColor(red, green, blue uint8, want [3]uint8) bool {
	const tolerance = 16
	return absInt(int(red)-int(want[0])) <= tolerance && absInt(int(green)-int(want[1])) <= tolerance && absInt(int(blue)-int(want[2])) <= tolerance
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
