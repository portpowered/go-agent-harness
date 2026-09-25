//go:build live

package chrome

// Provider-capture inspection for the spoken page-sight proof in
// cubecade_live_voice_test.go: one show_page call, one metadata output, one
// image projection, then exactly one spoken continuation grounded in the
// returned pixels.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type cubecadeLiveVoiceObservation struct {
	BrowserID         string
	TargetID          string
	MIMEType          string
	ByteLength        int
	Width             int
	Height            int
	SHA256            string
	ShowPageCalls     int
	FunctionOutput    int
	InputImageCount   int
	EncodedImageUses  int
	ContinuationCount int
	CaptureElapsedMS  int64
	TerminalStatus    string
	SpokenTranscript  string
	SpokenFacts       []string
	EventOrder        []string
	Image             []byte
}

type cubecadeLiveVoiceFunctionOutput struct {
	Index  int
	CallID string
	Result webmcpTools.ShowPageResult
	Image  []byte
}

type cubecadeLiveVoiceIndexedText struct {
	Index int
	Text  string
}

// cubecadeLiveVoiceInspector walks one spoken page-sight capture and records
// where each step of the show_page round trip happened.
type cubecadeLiveVoiceInspector struct {
	capture     gatewaytesting.SessionCapture
	oracle      cubecadeSightOracle
	observation cubecadeLiveVoiceObservation
	output      cubecadeLiveVoiceFunctionOutput

	showCallIndex       int
	showCallID          string
	showArgumentsIndex  int
	functionOutputIndex int
	imageIndex          int
	advertisedShowPage  bool
	transcripts         []cubecadeLiveVoiceIndexedText
	outputAudioIndices  []int
	responseDone        []cubecadeLiveVoiceIndexedText
	responseCreates     []int
}

func inspectCubecadeLiveVoiceCapture(capture gatewaytesting.SessionCapture, expectedBrowserID, expectedTargetID string, oracle cubecadeSightOracle) (cubecadeLiveVoiceObservation, error) {
	inspector := &cubecadeLiveVoiceInspector{
		capture:             capture,
		oracle:              oracle,
		observation:         cubecadeLiveVoiceObservation{EventOrder: make([]string, 0, len(capture.Records))},
		showCallIndex:       -1,
		showArgumentsIndex:  -1,
		functionOutputIndex: -1,
		imageIndex:          -1,
	}
	if capture.Provider.Name != liveProviderOpenAI || capture.Provider.Model != cubecadeModel {
		return inspector.observation, fmt.Errorf("provider=(%q,%q), want (openai,%q)", capture.Provider.Name, capture.Provider.Model, cubecadeModel)
	}
	for index, record := range capture.Records {
		if err := inspector.observe(index, record); err != nil {
			return inspector.observation, err
		}
	}
	if err := inspector.validateRoundTrip(expectedBrowserID, expectedTargetID); err != nil {
		return inspector.observation, err
	}
	err := inspector.validateSpokenContinuation()
	return inspector.observation, err
}

func (i *cubecadeLiveVoiceInspector) observe(index int, record gatewaytesting.CapturedSessionEvent) error {
	prefix := "S"
	if record.Direction == gatewaytesting.DirectionClientToServer {
		prefix = "C"
	}
	i.observation.EventOrder = append(i.observation.EventOrder, prefix+":"+record.Type)
	payload := liveRecordPayload(record)
	if len(payload) == 0 {
		return fmt.Errorf("record %d (%s) has an empty payload", index, record.Type)
	}
	switch record.Direction {
	case gatewaytesting.DirectionServerToClient:
		return i.observeServer(index, record.Type, payload)
	case gatewaytesting.DirectionClientToServer:
		return i.observeClient(index, record.Type, payload)
	}
	return nil
}

func (i *cubecadeLiveVoiceInspector) observeServer(index int, eventType string, payload []byte) error {
	switch eventType {
	case liveEventOutputItemAdded:
		return i.observeShowPageCall(index, payload)
	case liveEventFunctionCallArgumentsDone:
		return i.observeShowPageArguments(index, payload)
	case liveEventOutputAudioTranscriptDone:
		var event struct {
			Transcript string `json:"transcript"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode spoken transcript: %w", err)
		}
		if strings.TrimSpace(event.Transcript) != "" {
			i.transcripts = append(i.transcripts, cubecadeLiveVoiceIndexedText{Index: index, Text: strings.TrimSpace(event.Transcript)})
		}
	case "response.output_audio.delta":
		i.outputAudioIndices = append(i.outputAudioIndices, index)
	case liveEventResponseDone:
		var event struct {
			Response struct {
				Status string `json:"status"`
			} `json:"response"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode response.done: %w", err)
		}
		i.responseDone = append(i.responseDone, cubecadeLiveVoiceIndexedText{Index: index, Text: liveTerminalStatus(event.Response.Status, event.Status)})
	case liveEventProviderError:
		return errors.New("provider emitted an error event")
	}
	return nil
}

func (i *cubecadeLiveVoiceInspector) observeShowPageCall(index int, payload []byte) error {
	var event struct {
		Item struct {
			Type   string `json:"type"`
			Name   string `json:"name"`
			CallID string `json:"call_id"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode show_page output item: %w", err)
	}
	if event.Item.Type == liveItemFunctionCall && event.Item.Name == webmcp.ShowPageToolName {
		i.observation.ShowPageCalls++
		i.showCallIndex = index
		i.showCallID = event.Item.CallID
	}
	return nil
}

func (i *cubecadeLiveVoiceInspector) observeShowPageArguments(index int, payload []byte) error {
	var event struct {
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode show_page arguments: %w", err)
	}
	if event.Name != webmcp.ShowPageToolName {
		return nil
	}
	if event.CallID != i.showCallID {
		return fmt.Errorf("show_page arguments call ID %q, want %q", event.CallID, i.showCallID)
	}
	if strings.TrimSpace(event.Arguments) != "{}" {
		return fmt.Errorf("show_page arguments=%q, want an empty object", event.Arguments)
	}
	i.showArgumentsIndex = index
	return nil
}

func (i *cubecadeLiveVoiceInspector) observeClient(index int, eventType string, payload []byte) error {
	switch eventType {
	case liveEventSessionUpdate:
		return i.observeSessionUpdate(payload)
	case liveEventResponseCreate:
		i.responseCreates = append(i.responseCreates, index)
		return nil
	case liveEventConversationItemCreate:
	default:
		return nil
	}
	var event struct {
		Item struct {
			Type    string `json:"type"`
			CallID  string `json:"call_id"`
			Output  string `json:"output"`
			Content []struct {
				Type     string `json:"type"`
				ImageURL string `json:"image_url"`
			} `json:"content"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode provider conversation item: %w", err)
	}
	switch event.Item.Type {
	case liveItemFunctionCallOutput:
		return i.observeFunctionOutput(index, event.Item.CallID, event.Item.Output)
	case "message":
		for _, part := range event.Item.Content {
			if part.Type != "input_image" {
				continue
			}
			if err := i.observeInputImage(index, part.ImageURL); err != nil {
				return err
			}
		}
	}
	return nil
}

func (i *cubecadeLiveVoiceInspector) observeSessionUpdate(payload []byte) error {
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
	showDefinitions := 0
	for _, tool := range event.Session.Tools {
		if tool.Name == webmcp.ShowPageToolName {
			showDefinitions++
		}
	}
	if showDefinitions > 1 {
		return fmt.Errorf("session advertised show_page %d times, want at most once per update", showDefinitions)
	}
	if showDefinitions == 1 {
		i.advertisedShowPage = true
	}
	return nil
}

func (i *cubecadeLiveVoiceInspector) observeFunctionOutput(index int, callID, output string) error {
	if callID != i.showCallID {
		return fmt.Errorf("function output call ID %q, want %q", callID, i.showCallID)
	}
	if i.observation.FunctionOutput > 0 {
		return errors.New("show_page function output was delivered more than once")
	}
	if strings.Contains(strings.ToLower(output), "base64") || strings.Contains(strings.ToLower(output), "data:image/") {
		return errors.New("show_page metadata envelope contains encoded pixels")
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(output))
	if err != nil {
		return fmt.Errorf("decode show_page envelope: %w", err)
	}
	if !envelope.OK {
		return fmt.Errorf("show_page returned failure: %+v", envelope.Error)
	}
	if err := json.Unmarshal(envelope.Data, &i.output.Result); err != nil {
		return fmt.Errorf("decode show_page metadata: %w", err)
	}
	result := i.output.Result
	if result.Version != webmcpTools.ShowPageResultVersion || result.Status != webmcpTools.ShowPageResultStatusSuccess || result.Source != "browser_page" || result.TypedProjection != webmcpTools.ShowPageResultTypedProjectionInputImage || result.BrowserID == "" || result.TargetID == "" || result.MIMEType != liveMIMETypePNG || result.ByteLength <= 0 || result.Width <= 0 || result.Height <= 0 || len(result.SHA256) != sha256.Size*2 || strings.ToLower(result.SHA256) != result.SHA256 {
		return fmt.Errorf("show_page metadata=%+v, want one complete browser-page PNG result", result)
	}
	i.output.Index = index
	i.output.CallID = callID
	i.observation.FunctionOutput++
	i.functionOutputIndex = index
	i.observation.BrowserID, i.observation.TargetID = result.BrowserID, result.TargetID
	i.observation.MIMEType, i.observation.ByteLength = result.MIMEType, result.ByteLength
	i.observation.Width, i.observation.Height, i.observation.SHA256 = result.Width, result.Height, result.SHA256
	return nil
}

func (i *cubecadeLiveVoiceInspector) observeInputImage(index int, imageURL string) error {
	i.observation.InputImageCount++
	if i.imageIndex >= 0 {
		return errors.New("more than one input_image projection was delivered")
	}
	imageMIME, imageBytes, err := decodeLiveVoiceDataURL(imageURL)
	if err != nil {
		return fmt.Errorf("decode show_page input_image: %w", err)
	}
	i.observation.MIMEType = imageMIME
	i.observation.Image = imageBytes
	i.imageIndex = index
	imageDigest := sha256.Sum256(imageBytes)
	i.observation.EncodedImageUses = countEncodedImageOccurrences(i.capture.Records, base64.StdEncoding.EncodeToString(imageBytes))
	result := i.output.Result
	if result.ByteLength != len(imageBytes) || result.SHA256 != hex.EncodeToString(imageDigest[:]) {
		return fmt.Errorf("projected image bytes do not match show_page metadata")
	}
	if result.MIMEType != imageMIME {
		return fmt.Errorf("projected image MIME=%q, metadata MIME=%q", imageMIME, result.MIMEType)
	}
	decoded, format, err := image.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		return fmt.Errorf("decode projected show_page image: %w", err)
	}
	if format != "png" || decoded.Bounds().Dx() <= 200 || decoded.Bounds().Dy() <= 200 || decoded.Bounds().Dx() != result.Width || decoded.Bounds().Dy() != result.Height {
		return fmt.Errorf("projected show_page image format/dimensions=%s/%dx%d metadata=%dx%d", format, decoded.Bounds().Dx(), decoded.Bounds().Dy(), result.Width, result.Height)
	}
	if assertCubecadeScreenshotMarkerNoFatal(decoded, i.oracle) == 0 {
		return errors.New("projected show_page image did not contain the independent Cubecade marker")
	}
	return nil
}

// validateRoundTrip checks the one show_page call, output, and image
// projection happened in order for the selected target.
func (i *cubecadeLiveVoiceInspector) validateRoundTrip(expectedBrowserID, expectedTargetID string) error {
	observation := i.observation
	if !i.advertisedShowPage {
		return errors.New("session never advertised show_page")
	}
	if observation.ShowPageCalls != 1 || i.showCallID == "" || i.showArgumentsIndex <= i.showCallIndex {
		return fmt.Errorf("show_page call count/id/order=%d/%q/%d/%d, want one real call with arguments after dispatch", observation.ShowPageCalls, i.showCallID, i.showCallIndex, i.showArgumentsIndex)
	}
	if observation.FunctionOutput != 1 || i.functionOutputIndex <= i.showArgumentsIndex {
		return fmt.Errorf("show_page function output count/order=%d/%d, want one output after arguments", observation.FunctionOutput, i.functionOutputIndex)
	}
	if observation.InputImageCount != 1 || i.imageIndex <= i.functionOutputIndex {
		return fmt.Errorf("input_image count/order=%d/%d, want one projection after function output", observation.InputImageCount, i.imageIndex)
	}
	if observation.EncodedImageUses != 1 {
		return fmt.Errorf("encoded image occurrence count=%d, want exactly once across provider frames", observation.EncodedImageUses)
	}
	if observation.BrowserID != expectedBrowserID || observation.TargetID != expectedTargetID {
		return fmt.Errorf("show_page identity=(%q,%q), want selected (%q,%q)", observation.BrowserID, observation.TargetID, expectedBrowserID, expectedTargetID)
	}
	if len(i.responseCreates) < 2 {
		return fmt.Errorf("response.create count=%d, want an initial request and one continuation", len(i.responseCreates))
	}
	return nil
}

// validateSpokenContinuation checks the one continuation after the image
// completed with spoken, grounded audio inside the capture budget.
func (i *cubecadeLiveVoiceInspector) validateSpokenContinuation() error {
	continuationIndex, err := i.continuationIndex()
	if err != nil {
		return err
	}
	terminalIndex, err := i.terminalIndex(continuationIndex)
	if err != nil {
		return err
	}
	spokenTranscripts := make([]string, 0, len(i.transcripts))
	for _, event := range i.transcripts {
		if event.Index > continuationIndex && event.Index <= terminalIndex {
			spokenTranscripts = append(spokenTranscripts, event.Text)
		}
	}
	spokenAudioEvents := 0
	for _, eventIndex := range i.outputAudioIndices {
		if eventIndex > continuationIndex && eventIndex <= terminalIndex {
			spokenAudioEvents++
		}
	}
	if spokenAudioEvents == 0 || len(spokenTranscripts) == 0 {
		return fmt.Errorf("spoken continuation audio events=%d transcript_count=%d, want non-empty spoken continuation", spokenAudioEvents, len(spokenTranscripts))
	}
	i.observation.SpokenTranscript = strings.Join(spokenTranscripts, " ")
	i.observation.SpokenFacts = cubecadeLiveVoiceFacts(i.observation.SpokenTranscript)
	if len(i.observation.SpokenFacts) < 2 {
		return fmt.Errorf("spoken transcript=%q contains %d grounded visual facts, want at least two", i.observation.SpokenTranscript, len(i.observation.SpokenFacts))
	}
	if i.functionOutputIndex >= continuationIndex || i.imageIndex >= continuationIndex {
		return errors.New("show_page result or image was not delivered before continuation")
	}
	return i.measureCaptureElapsed()
}

func (i *cubecadeLiveVoiceInspector) continuationIndex() (int, error) {
	continuations := make([]int, 0, 1)
	for _, responseIndex := range i.responseCreates {
		if responseIndex > i.imageIndex {
			continuations = append(continuations, responseIndex)
		}
	}
	if len(continuations) != 1 {
		return 0, fmt.Errorf("continuation response.create count=%d, want exactly one after image projection=%d", len(continuations), i.imageIndex)
	}
	i.observation.ContinuationCount = 1
	return continuations[0], nil
}

func (i *cubecadeLiveVoiceInspector) terminalIndex(continuationIndex int) (int, error) {
	if len(i.responseDone) == 0 {
		return 0, errors.New("provider emitted no response.done")
	}
	for _, done := range i.responseDone {
		if done.Index <= continuationIndex {
			continue
		}
		if done.Text != "completed" {
			return 0, fmt.Errorf("continuation response.done status=%q, want completed", done.Text)
		}
		i.observation.TerminalStatus = done.Text
		return done.Index, nil
	}
	return 0, errors.New("no completed terminal response.done followed the continuation")
}

func (i *cubecadeLiveVoiceInspector) measureCaptureElapsed() error {
	if i.functionOutputIndex < 0 || i.showArgumentsIndex < 0 {
		return nil
	}
	elapsed := i.capture.Records[i.functionOutputIndex].TimestampMs - i.capture.Records[i.showArgumentsIndex].TimestampMs
	i.observation.CaptureElapsedMS = elapsed
	if elapsed < 0 {
		return errors.New("show_page capture elapsed time is negative")
	}
	if elapsed >= int64(cubecadeScreenshotBudget/time.Millisecond) {
		return fmt.Errorf("show_page capture elapsed=%dms, want less than %s", elapsed, cubecadeScreenshotBudget)
	}
	return nil
}

func cubecadeLiveVoiceFacts(transcript string) []string {
	normalized := strings.ToLower(transcript)
	facts := make([]string, 0, 3)
	if strings.Contains(normalized, "solved") || strings.Contains(normalized, "complete") {
		facts = append(facts, "solved_status")
	}
	if strings.Contains(normalized, "cube") || strings.Contains(normalized, "cubecade") {
		facts = append(facts, "cube_or_brand")
	}
	for _, color := range []string{"neon", "bright", "colored", "green", "pink", "blue", "yellow", "orange", "lime"} {
		if strings.Contains(normalized, color) {
			facts = append(facts, "visible_color")
			break
		}
	}
	return facts
}

func decodeLiveVoiceDataURL(value string) (string, []byte, error) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
		return "", nil, errors.New("image projection is not a base64 data URL")
	}
	mimeType := strings.TrimPrefix(strings.TrimSuffix(header, ";base64"), "data:")
	if mimeType == "" {
		return "", nil, errors.New("image projection has no MIME type")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, err
	}
	if len(decoded) == 0 {
		return "", nil, errors.New("image projection is empty")
	}
	return mimeType, decoded, nil
}

func countEncodedImageOccurrences(records []gatewaytesting.CapturedSessionEvent, encoded string) int {
	if encoded == "" {
		return 0
	}
	count := 0
	for _, record := range records {
		if record.Direction != gatewaytesting.DirectionClientToServer {
			continue
		}
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		count += strings.Count(string(payload), encoded)
	}
	return count
}

func assertCubecadeScreenshotMarkerNoFatal(screenshot image.Image, oracle cubecadeSightOracle) int {
	want, ok := parseCSSRGB(oracle.MarkerBackground)
	if !ok || oracle.ViewportWidth <= 0 || oracle.ViewportHeight <= 0 || oracle.MarkerRect.Width <= 0 || oracle.MarkerRect.Height <= 0 {
		return 0
	}
	bounds := screenshot.Bounds()
	scaleX := float64(bounds.Dx()) / oracle.ViewportWidth
	scaleY := float64(bounds.Dy()) / oracle.ViewportHeight
	left := max(bounds.Min.X, bounds.Min.X+int(oracle.MarkerRect.X*scaleX))
	top := max(bounds.Min.Y, bounds.Min.Y+int(oracle.MarkerRect.Y*scaleY))
	right := min(bounds.Max.X, bounds.Min.X+int((oracle.MarkerRect.X+oracle.MarkerRect.Width)*scaleX))
	bottom := min(bounds.Max.Y, bounds.Min.Y+int((oracle.MarkerRect.Y+oracle.MarkerRect.Height)*scaleY))
	if right <= left || bottom <= top {
		return 0
	}
	return countScreenshotMarkerPixels(screenshot, image.Rect(left, top, right, bottom), want)
}
