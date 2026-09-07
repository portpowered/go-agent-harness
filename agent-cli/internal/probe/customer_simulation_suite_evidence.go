package probe

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// customerSimulationRecordingFacts are derived only from the copied product
// record directory. They intentionally omit tool arguments and raw payloads.
type customerSimulationRecordingFacts struct {
	responses         []customerSimulationResponse
	tools             []ToolObservation
	cancelObserved    bool
	cancelAt          time.Duration
	cancelWallAt      time.Time
	cancelResponseID  string
	inputSpeechStarts []time.Duration
	recordingBase     time.Time
}

type customerSimulationResponse struct {
	ID            string
	Text          string
	Start         time.Duration
	End           time.Duration
	AudioBytes    int
	Complete      bool
	Cancelled     bool
	WallStart     time.Time
	WallEnd       time.Time
	AudioStart    time.Duration
	AudioEnd      time.Duration
	AudioObserved bool
}

type customerSimulationRecordedMessage struct {
	media   *customerSimulationMediaBoundary
	message messages.StreamMessage
	at      time.Duration
	wallAt  time.Time
	dir     transcript.Direction
}

type customerSimulationStreamParser struct {
	deliveredResponseID string
	mediaByResponse     map[string]customerSimulationMediaInterval
	facts               customerSimulationRecordingFacts
	scenario            CustomerScenario
	knownResponses      int
	completedToolIDs    map[string]time.Duration
	pending             map[string]*customerSimulationTool
	current             *customerSimulationResponse
	text                strings.Builder
	activeResponseID    string
	responseIndex       int
	inputSpeechActive   bool
	lastWallAt          time.Time
	lastAt              time.Duration
}

type customerSimulationTool struct {
	ID       string
	Name     string
	Start    time.Duration
	End      time.Duration
	ResultAt time.Duration
	Result   bool
}

type customerSimulationSessionLogEntry struct {
	TurnIndex int `json:"turn_index"`
	Response  struct {
		Text       string `json:"text"`
		Complete   bool   `json:"complete"`
		AudioBytes int    `json:"audio_bytes"`
	} `json:"response"`
}

func readCustomerSimulationRecording(recordRoot string, scenario CustomerScenario) (customerSimulationRecordingFacts, error) {
	var facts customerSimulationRecordingFacts
	var sessionLogResponses []customerSimulationResponse
	var failures []error
	if data, err := os.ReadFile(filepath.Join(recordRoot, "session-log.jsonl")); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			var entry customerSimulationSessionLogEntry
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				failures = append(failures, fmt.Errorf("decode session-log entry: %v", err))
				continue
			}
			sessionLogResponses = append(sessionLogResponses, customerSimulationResponse{Text: entry.Response.Text, Complete: entry.Response.Complete, AudioBytes: entry.Response.AudioBytes})
		}
		if err := scanner.Err(); err != nil {
			failures = append(failures, fmt.Errorf("read session-log: %v", err))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		failures = append(failures, fmt.Errorf("read session-log: %v", err))
	}

	streamFacts, err := readCustomerSimulationStream(recordRoot, scenario, len(sessionLogResponses))
	if err != nil {
		failures = append(failures, err)
	}
	if len(streamFacts.responses) > 0 {
		// The raw stream is authoritative for response identity, timing, audio
		// ranges, and cancellation. session-log.jsonl is used only when the raw
		// stream is unavailable; it can contain tool continuations that do not
		// line up one-for-one with the response boundaries needed by a correction
		// ledger.
		facts.responses = streamFacts.responses
	} else if len(sessionLogResponses) > 0 {
		facts.responses = sessionLogResponses
	}
	facts.tools = streamFacts.tools
	facts.cancelObserved = streamFacts.cancelObserved
	facts.cancelAt = streamFacts.cancelAt
	facts.cancelWallAt = streamFacts.cancelWallAt
	facts.cancelResponseID = streamFacts.cancelResponseID
	facts.inputSpeechStarts = append([]time.Duration(nil), streamFacts.inputSpeechStarts...)
	facts.recordingBase = streamFacts.recordingBase
	return facts, errors.Join(failures...)
}

func readCustomerSimulationStream(recordRoot string, scenario CustomerScenario, knownResponses int) (customerSimulationRecordingFacts, error) {
	var facts customerSimulationRecordingFacts
	path := filepath.Join(recordRoot, "agent.transcript.jsonl")
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return facts, nil
		}
		return facts, fmt.Errorf("open product transcript: %v", err)
	}
	defer file.Close()
	var records []customerSimulationRecordedMessage
	var base time.Time
	completedToolIDs := make(map[string]time.Duration)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		record, decodeErr := transcript.Decode(scanner.Bytes())
		if decodeErr != nil {
			return facts, fmt.Errorf("decode product transcript: %v", decodeErr)
		}
		if !isCustomerSimulationAgentRecord(record) {
			continue
		}
		wallAt, parseErr := time.Parse(time.RFC3339Nano, record.Timestamp)
		if base.IsZero() && parseErr == nil {
			base = wallAt
		}
		at := time.Duration(0)
		if parseErr == nil && !base.IsZero() && wallAt.After(base) {
			at = wallAt.Sub(base)
		}
		parsed, keep, err := parseCustomerSimulationRecord(record, at, wallAt, completedToolIDs)
		if err != nil {
			return facts, err
		}
		if keep {
			records = append(records, parsed)
		}
	}
	if err := scanner.Err(); err != nil {
		return facts, fmt.Errorf("read product transcript: %v", err)
	}

	parser := customerSimulationStreamParser{
		facts:            customerSimulationRecordingFacts{recordingBase: base},
		scenario:         scenario,
		knownResponses:   knownResponses,
		completedToolIDs: completedToolIDs,
		pending:          make(map[string]*customerSimulationTool),
		lastWallAt:       base,
	}
	for _, record := range records {
		if parser.consume(record) {
			break
		}
	}
	parser.finish()
	parser.applyMediaBoundaries()
	return parser.facts, nil
}

func (p *customerSimulationStreamParser) consume(record customerSimulationRecordedMessage) bool {
	p.lastAt = record.at
	if !record.wallAt.IsZero() {
		p.lastWallAt = record.wallAt
	}
	if record.media != nil {
		p.consumeMediaBoundary(record)
		return false
	}
	msg := record.message
	isAssistant := customerSimulationMessageIsAssistant(record)
	switch msg.Type {
	case messages.StreamTypeMessageStart:
		p.consumeMessageStart(record, isAssistant)
	case messages.StreamTypeTextDelta:
		p.consumeTextDelta(record, isAssistant)
	case messages.StreamTypeTranscriptEnd:
		p.consumeTranscriptEnd(record, isAssistant)
	case messages.StreamTypeAudioDelta:
		p.consumeAudioDelta(record, isAssistant)
	case messages.StreamTypeToolCallEnd:
		p.consumeToolCallEnd(record, isAssistant)
	case messages.StreamTypeResponseCancel:
		p.consumeResponseCancel(record)
	case messages.StreamTypeMessageEnd:
		p.consumeMessageEnd(record, isAssistant)
	}
	return p.responseIndex > len(p.scenario.Actions)+p.knownResponses+1
}

func (p *customerSimulationStreamParser) consumeMessageStart(record customerSimulationRecordedMessage, isAssistant bool) {
	if !isAssistant || p.current != nil {
		return
	}
	p.current = &customerSimulationResponse{ID: record.message.ResponseID, Start: record.at, WallStart: record.wallAt}
	p.activeResponseID = strings.TrimSpace(record.message.ResponseID)
	p.responseIndex++
}

func (p *customerSimulationStreamParser) ensureResponse(record customerSimulationRecordedMessage) {
	if p.current == nil {
		p.current = &customerSimulationResponse{ID: record.message.ResponseID, Start: record.at, WallStart: record.wallAt}
	} else if p.current.ID == "" {
		p.current.ID = record.message.ResponseID
	}
	if p.activeResponseID == "" {
		p.activeResponseID = strings.TrimSpace(record.message.ResponseID)
	}
}

func (p *customerSimulationStreamParser) consumeTextDelta(record customerSimulationRecordedMessage, isAssistant bool) {
	if !isAssistant {
		return
	}
	p.ensureResponse(record)
	if value, ok := record.message.Value.(*messages.TextDeltaValue); ok && value != nil {
		p.text.WriteString(value.Content)
	}
}

func (p *customerSimulationStreamParser) consumeTranscriptEnd(record customerSimulationRecordedMessage, isAssistant bool) {
	if !isAssistant {
		return
	}
	p.ensureResponse(record)
	if value, ok := record.message.Value.(*messages.TranscriptEndValue); ok && value != nil && p.text.Len() == 0 {
		p.text.WriteString(value.FullText)
	}
}

func (p *customerSimulationStreamParser) consumeAudioDelta(record customerSimulationRecordedMessage, isAssistant bool) {
	value, ok := record.message.Value.(*messages.AudioDeltaValue)
	if !ok || value == nil {
		return
	}
	if isAssistant {
		p.consumeAssistantAudio(record, value)
		return
	}
	if record.dir != transcript.DirectionIn {
		return
	}
	// The recorder's agent-side DirectionIn is the outbound provider-bound
	// customer audio. Keep only the first frame of each non-silent run so the
	// correction boundary is grounded in this same transcript clock as
	// response audio and RESPONSE.CANCEL.
	signal := customerSimulationPCM16HasSignal(value.Content)
	if signal && !p.inputSpeechActive {
		p.facts.inputSpeechStarts = append(p.facts.inputSpeechStarts, record.at)
	}
	p.inputSpeechActive = signal
}

func (p *customerSimulationStreamParser) consumeAssistantAudio(record customerSimulationRecordedMessage, value *messages.AudioDeltaValue) {
	p.ensureResponse(record)
	p.current.AudioBytes += len(value.Content)
	if !p.current.AudioObserved {
		p.current.AudioStart = record.at
		p.current.AudioObserved = true
	}
	end := record.at + customerSimulationPCM16Duration(len(value.Content))
	if end <= record.at {
		end = record.at + time.Nanosecond
	}
	if end > p.current.AudioEnd {
		p.current.AudioEnd = end
	}
	// A provider audio response is an observable boundary between customer
	// speech runs. This also covers providers that omit explicit VAD silence
	// frames from the recording.
	p.inputSpeechActive = false
}

func (p *customerSimulationStreamParser) consumeToolCallEnd(record customerSimulationRecordedMessage, isAssistant bool) {
	value, ok := record.message.Value.(*messages.ToolCallEndValue)
	if !ok || value == nil || strings.TrimSpace(value.ToolCallID) == "" {
		return
	}
	if isAssistant {
		p.pending[value.ToolCallID] = &customerSimulationTool{ID: value.ToolCallID, Name: value.Name, Start: record.at}
		return
	}
	p.completedToolIDs[value.ToolCallID] = record.at
}

func (p *customerSimulationStreamParser) consumeResponseCancel(record customerSimulationRecordedMessage) {
	if record.dir != transcript.DirectionIn {
		return
	}
	// DirectionIn on the agent transcript is the actual outbound provider
	// cancellation. Do not treat a provider-side or inferred terminal event as
	// proof of a customer barge-in.
	if !p.facts.cancelObserved {
		p.facts.cancelObserved = true
		p.facts.cancelAt = record.at
		p.facts.cancelWallAt = record.wallAt
		p.facts.cancelResponseID = strings.TrimSpace(record.message.ResponseID)
		if p.facts.cancelResponseID == "" {
			p.facts.cancelResponseID = p.deliveredResponseID
			if p.facts.cancelResponseID == "" {
				p.facts.cancelResponseID = p.activeResponseID
			}
		}
		if p.facts.cancelResponseID == "" && p.current != nil {
			p.facts.cancelResponseID = p.current.ID
		}
	}
	if p.current != nil && p.current.ID == p.facts.cancelResponseID {
		p.current.Cancelled = true
	}
}

func (p *customerSimulationStreamParser) consumeMessageEnd(record customerSimulationRecordedMessage, isAssistant bool) {
	if !isAssistant {
		return
	}
	p.flush(record.at, record.wallAt, true, p.current != nil && p.current.Cancelled)
	p.activeResponseID = ""
}

func (p *customerSimulationStreamParser) flush(at time.Duration, wallAt time.Time, complete, cancelled bool) {
	if p.current == nil {
		return
	}
	p.current.Text = p.text.String()
	p.current.End = at
	p.current.WallEnd = wallAt
	p.current.Complete = complete
	p.current.Cancelled = cancelled
	p.facts.responses = append(p.facts.responses, *p.current)
	p.current = nil
	p.text.Reset()
}

func (p *customerSimulationStreamParser) finish() {
	if p.current != nil {
		p.flush(p.lastAt, p.lastWallAt, false, p.current.Cancelled)
	}
	toolIDs := make([]string, 0, len(p.pending))
	for id := range p.pending {
		toolIDs = append(toolIDs, id)
	}
	sort.Strings(toolIDs)
	for _, toolID := range toolIDs {
		tool := p.pending[toolID]
		actionIndex := len(p.facts.tools)
		if actionIndex >= len(p.scenario.Actions) {
			actionIndex = len(p.scenario.Actions) - 1
		}
		if actionIndex < 0 {
			actionIndex = 0
		}
		turnID := customerSimulationTurnID(p.scenario, actionIndex)
		resultAt, resultSeen := p.completedToolIDs[tool.ID]
		status := "started"
		duration := maxDuration(0, resultAt-tool.Start)
		if resultSeen {
			status = "completed"
		}
		p.facts.tools = append(p.facts.tools, ToolObservation{ID: tool.ID, ActionID: p.scenario.Actions[actionIndex].ID, TurnID: turnID, Tool: customerSimulationSlug(tool.Name), Status: status, At: tool.Start, Duration: duration, ResultSeen: resultSeen})
	}
	for index := range p.facts.responses {
		if p.facts.responses[index].End < p.facts.responses[index].Start {
			p.facts.responses[index].End = p.facts.responses[index].Start
		}
	}
}

func recordContainsStreamType(payload []byte, want string) bool {
	var envelope struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(payload, &envelope) == nil && envelope.Type == want
}

func customerSimulationPCM16HasSignal(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return true
		}
	}
	return false
}

func fullMessageToolID(payload []byte) (string, bool) {
	var envelope struct {
		ToolCallID string `json:"tool_call_id"`
		Value      struct {
			Message struct {
				ToolCallID string `json:"ToolCallID"`
			} `json:"message"`
		} `json:"value"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", false
	}
	if envelope.ToolCallID != "" {
		return envelope.ToolCallID, true
	}
	return envelope.Value.Message.ToolCallID, envelope.Value.Message.ToolCallID != ""
}

func maxDuration(left, right time.Duration) time.Duration {
	if right > left {
		return right
	}
	return left
}

func customerSimulationMixedModalEvidence(scenario CustomerScenario, transcripts PairedTranscripts, result DuplexRunResult) MixedModalEvidence {
	priorAt := time.Duration(0)
	if len(transcripts.Product) > 1 {
		priorAt = transcripts.Product[1].At
	}
	customerAt := priorAt + time.Millisecond
	return MixedModalEvidence{
		ImageEventID: FamilyCImageEventID, PriorActionID: FamilyCTextActionID, PriorTurnID: customerSimulationTurnID(scenario, 1), ImageTurnID: customerSimulationTurnID(scenario, 2),
		PriorActionCompletedAt: priorAt, CustomerTurnStartedAt: customerAt, ImageObserved: false, ExpectedSHA256: FamilyCImageFixtureSHA256,
		Delivery: MixedModalDeliveryUnsupported, Supported: false, ImageMeaningInCustomerSpeech: false, ProductGapCode: FamilyCMidSessionImageGapCode, ProductGap: FamilyCMidSessionImageGap,
		EvidenceRefs: []string{"events/mixed-modal.json", "transcripts/product.jsonl", "process.json"},
	}
}

func customerSimulationTerminationEvidence(scenario CustomerScenario, product []TranscriptEvent, process ProcessFacts, result DuplexRunResult, facts customerSimulationRecordingFacts) TerminationEvidence {
	start, end := customerSimulationResponseInterval(product, 0)
	if start == 0 && len(result.Output) > 0 {
		start = result.Output[0].At
	}
	status := "incomplete"
	if scenario.Termination == TerminationSIGINT {
		if process.SignalSent {
			status = "interrupted"
			if facts.cancelObserved {
				status = "cancelled"
			}
		}
	} else if len(product) > 0 && product[0].Final {
		status = "completed"
	}
	if status != "incomplete" {
		if end <= start {
			end = start + time.Millisecond
		}
		if process.SignalSent && process.SignalAt > end {
			end = process.SignalAt
		}
	}
	satisfaction := status == "completed" && scenario.Termination == TerminationNatural
	satisfactionAt := time.Duration(0)
	if satisfaction {
		satisfactionAt = end + time.Nanosecond
	}
	return TerminationEvidence{
		Method: scenario.Termination, ActiveActionID: FamilyDActionID, ActiveTurnID: FamilyDActiveTurnID, ActiveResponseID: FamilyDActiveResponseID,
		ActiveResponseStatus: status, ActiveResponseStartedAt: start, ActiveResponseEndedAt: end, SatisfactionDeclared: satisfaction, SatisfactionAt: satisfactionAt,
		SignalSent: process.SignalSent, Signal: process.Signal, SignalAt: process.SignalAt, Process: process,
		OutstandingToolIDs: factsOutstandingToolIDs(facts), EvidenceRefs: FamilyDTerminationEvidenceRefs(),
	}
}

func factsOutstandingToolIDs(facts customerSimulationRecordingFacts) []string {
	var result []string
	for _, tool := range facts.tools {
		if tool.Status != "completed" || !tool.ResultSeen {
			result = append(result, tool.ID)
		}
	}
	return result
}

func customerSimulationPatienceEvidence(scenario CustomerScenario, product []TranscriptEvent, process ProcessFacts, result DuplexRunResult, tools []ToolObservation, facts customerSimulationRecordingFacts, controller *PatienceController) PatienceEvidence {
	if controller != nil {
		controllerProcess := process
		// The controller and runner share the OnStart origin. Keep this small
		// guard for a process that exits during the callback itself, where the
		// scheduler can otherwise make the two terminal observations differ by
		// a few nanoseconds.
		if controller.outcome == "" {
			// A child failure before the patience gate reaches a terminal product
			// boundary is not evidence of completion. Preserve a typed cancellation
			// outcome so the mechanical evaluator reports the missing terminal
			// observation instead of fabricating success from stdout.
			if result.TimedOut || process.ExitClassification == "timeout" {
				_ = controller.Timeout()
			} else {
				_ = controller.Cancel()
			}
		}
		if controllerProcess.EndedAt < controller.terminalAt {
			controllerProcess.EndedAt = controller.terminalAt
		}
		if evidence, err := controller.Evidence(controllerProcess, toolObservationIDsNotComplete(tools), FamilyEPatienceEvidenceRefs()); err == nil {
			return evidence
		}
	}

	turnID := FamilyETurnID
	terminal := process.EndedAt
	if terminal <= 0 {
		terminal = time.Millisecond
	}
	events := []PatienceEvent{{ID: "listen-started", TurnID: turnID, Kind: PatienceEventListenStarted, At: 0}}
	responseStart := time.Duration(0)
	firstProgress := time.Duration(0)
	lastProgress := time.Duration(0)
	for index, output := range result.Output {
		if output.Bytes <= 0 {
			continue
		}
		at := output.At
		if at < 0 {
			at = 0
		}
		if at > terminal {
			at = terminal
		}
		if len(events) == 1 {
			responseStart = at
			events = append(events, PatienceEvent{ID: "response-started", TurnID: turnID, Kind: PatienceEventResponseStarted, At: at})
		}
		if at < lastProgress {
			at = lastProgress
		}
		events = append(events, PatienceEvent{ID: fmt.Sprintf("product-speech-%03d", index+1), TurnID: turnID, Kind: PatienceEventProductSpeech, At: at, Detail: fmt.Sprintf("stdout read %d carried %d product PCM bytes", output.Read, output.Bytes)})
		if firstProgress == 0 && len(events) > 2 {
			firstProgress = at
		}
		if at > lastProgress {
			lastProgress = at
		}
	}

	// This path is only a fail-closed compatibility fallback for callers that
	// do not have a live PatienceController. Product runs always construct the
	// controller above; without it, stdout alone cannot prove completion or
	// distinguish a terminal response from a stalled one.
	outcome := PatienceOutcomeCancelled
	state := PatienceActivityDeadAir
	deadAirAt := time.Duration(0)
	deadAirDuration := time.Duration(0)
	if result.TimedOut {
		outcome = PatienceOutcomeTimeout
	}
	if outcome == PatienceOutcomeCompleted {
		events = append(events, PatienceEvent{ID: "response-completed", TurnID: turnID, Kind: PatienceEventResponseCompleted, At: terminal})
	} else if outcome == PatienceOutcomeTimeout {
		events = append(events, PatienceEvent{ID: "timeout", TurnID: turnID, Kind: PatienceEventTimeout, At: terminal, Detail: "the shipped session reached its deadline before a terminal customer response"})
	} else {
		events = append(events, PatienceEvent{ID: "cancelled", TurnID: turnID, Kind: PatienceEventCancelled, At: terminal, Detail: "the shipped session was cancelled before a terminal customer response"})
	}
	_ = scenario
	_ = product
	_ = facts
	return PatienceEvidence{
		ActionID: FamilyEActionID, TurnID: turnID, ListenStartedAt: 0, ResponseStartedAt: responseStart, FirstProgressAt: firstProgress, LastProgressAt: lastProgress,
		TerminalAt: terminal, Outcome: outcome, ActivityState: state, Events: events, DeadAirAt: deadAirAt, DeadAirDuration: deadAirDuration, Process: process,
		OutstandingToolIDs: toolObservationIDsNotComplete(tools), CustomerImpact: "The customer could not rely on a timely, observable response.", EvidenceRefs: FamilyEPatienceEvidenceRefs(),
	}
}

func toolObservationIDsNotComplete(tools []ToolObservation) []string {
	var result []string
	for _, tool := range tools {
		if tool.Status != "completed" || !tool.ResultSeen {
			result = append(result, tool.ID)
		}
	}
	return result
}

func parseCustomerSimulationRecord(record transcript.Record, at time.Duration, wallAt time.Time, completedToolIDs map[string]time.Duration) (customerSimulationRecordedMessage, bool, error) {
	parsed := customerSimulationRecordedMessage{at: at, wallAt: wallAt, dir: record.Direction}
	if record.Stream == transcript.StreamRuntimeAudio {
		media, err := decodeCustomerSimulationMedia(record)
		parsed.media = media
		return parsed, media != nil, err
	}
	message, err := gatewaytesting.UnmarshalStreamMessage(record.Payload)
	if err == nil {
		parsed.message = message
		return parsed, true, nil
	}
	if recordContainsStreamType(record.Payload, string(messages.StreamTypeSystemFullMessage)) {
		if toolID, ok := fullMessageToolID(record.Payload); ok {
			completedToolIDs[toolID] = at
		}
		return parsed, false, nil
	}
	return parsed, false, fmt.Errorf("decode product stream message: %w", err)
}
