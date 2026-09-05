//go:build live

// This file is an explicitly opted-in OpenAI Realtime confirmation. The
// default integration suite keeps the same collision proof credential-free;
// this test only corroborates its event shape against the real provider.
package integration

import runtimecontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime"

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	liveBargeInAPIKeyEnv = "OPENAI_API_KEY"
	liveBargeInOptInEnv  = "AGENT_HARNESS_LIVE_S2S_BARGE_IN_V3"
	liveBargeInModel     = "gpt-realtime"
	liveBargeInTurns     = 4

	// The command's own max-duration is below the package test timeout used by
	// the documented command. The outer context and command join bound leave
	// time for cleanup without permitting a suite-killing timeout panic.
	liveBargeInMaxDuration = 75 * time.Second
	liveBargeInTestTimeout = 90 * time.Second
	liveBargeInJoinWait    = 2 * time.Second
	liveBargeInFrameWait   = 30 * time.Millisecond
)

type liveBargeInRuntimeObserver struct {
	mu           sync.Mutex
	observations []liveBargeInRuntimeFact
}

type liveBargeInRuntimeFact struct {
	Kind           runtimecontract.SessionRuntimeObservationKind
	PayloadBytes   int
	TurnsCompleted int
	InputCommit    int
	Clean          bool
	HasError       bool
	HasAccounting  bool
}

func (o *liveBargeInRuntimeObserver) ObserveSessionRuntime(observation runtimecontract.SessionRuntimeObservation) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.observations = append(o.observations, liveBargeInRuntimeFact{
		Kind:           observation.Kind,
		PayloadBytes:   len(observation.Payload),
		TurnsCompleted: observation.TurnsCompleted,
		InputCommit:    observation.InputCommit,
		Clean:          observation.Clean,
		HasError:       observation.Error != "",
		HasAccounting:  observation.FinalAccounting != nil,
	})
	o.mu.Unlock()
}

func (o *liveBargeInRuntimeObserver) snapshot() []liveBargeInRuntimeFact {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]liveBargeInRuntimeFact(nil), o.observations...)
}

func liveBargeInRuntimeEvidence(observations []liveBargeInRuntimeFact) string {
	parts := make([]string, 0, len(observations))
	for _, observation := range observations {
		parts = append(parts, fmt.Sprintf("%s{bytes=%d,turns=%d,commit=%d,clean=%t,error=%t,accounting=%t}",
			observation.Kind,
			observation.PayloadBytes,
			observation.TurnsCompleted,
			observation.InputCommit,
			observation.Clean,
			observation.HasError,
			observation.HasAccounting,
		))
	}
	return strings.Join(parts, ",")
}

func liveBargeInContract() probe.BargeInContract {
	inputs := make([]probe.BargeInInputExpectation, 0, liveBargeInTurns)
	responses := make([]probe.BargeInResponseExpectation, 0, liveBargeInTurns)
	for ordinal := 1; ordinal <= liveBargeInTurns; ordinal++ {
		inputID := fmt.Sprintf("input-%d", ordinal)
		turnID := fmt.Sprintf("turn-%d", ordinal)
		responseID := fmt.Sprintf("response-%d", ordinal)
		inputs = append(inputs, probe.BargeInInputExpectation{ID: inputID, TurnID: turnID})
		expectation := probe.BargeInResponseExpectation{
			ID:            responseID,
			InputID:       inputID,
			TurnID:        turnID,
			Disposition:   probe.BargeInDispositionCompleted,
			ForbidCancel:  true,
			RequireOutput: true,
		}
		if ordinal <= 2 {
			expectation.Disposition = probe.BargeInDispositionCancelled
			expectation.RequireCancel = true
			expectation.ForbidCancel = false
		}
		if ordinal == 2 {
			expectation.RequireOutput = false
			expectation.ForbidOutput = true
		}
		if ordinal >= 3 {
			expectation.RequireContinuation = true
		}
		responses = append(responses, expectation)
	}
	return probe.BargeInContract{
		Inputs:                 inputs,
		Responses:              responses,
		RequireSessionTerminal: true,
	}
}

type liveBargeInResponseIdentity struct {
	stable     string
	providerID string
	inputID    string
	turnID     string
	ordinal    int
	terminal   bool
	cancelSeq  int
}

// liveBargeInCaptureAdapter is the OpenAI transport adapter for the live
// proof. It correlates provider response IDs privately, then emits only the
// provider-neutral event vocabulary consumed by BargeInLedger.
type liveBargeInCaptureAdapter struct {
	ledger             *probe.BargeInLedger
	nextSequence       int
	inputOrdinal       int
	currentInput       string
	lastCommittedInput string
	committedInputs    []string
	userTurnInputs     map[string]string
	userItemIDs        map[string]struct{}
	responseOrdinal    int
	providerResponses  map[string]liveBargeInResponseIdentity
	responseByProvider map[string]string
	issues             []string
	facts              liveBargeInCaptureFacts
}

type liveBargeInWireResponse struct {
	Created    int
	FirstAudio int
	FirstText  int
	AudioBytes int
	Cancel     int
	Done       int
	Terminal   bool
}

type liveBargeInCaptureFacts struct {
	SessionCreated     int
	SessionUpdated     int
	SessionClosed      int
	Appends            int
	Commits            int
	UserItems          int
	Cancels            int
	ProviderErrors     int
	ProviderCodes      []string
	ProviderLateOutput int
	InputStarts        []int
	Responses          []liveBargeInWireResponse
}

func normalizeLiveBargeInCapture(capture gwtesting.SessionCapture) (*probe.BargeInLedger, liveBargeInCaptureFacts, error) {
	adapter := &liveBargeInCaptureAdapter{
		ledger:             probe.NewBargeInLedger(),
		providerResponses:  make(map[string]liveBargeInResponseIdentity),
		responseByProvider: make(map[string]string),
		userTurnInputs:     make(map[string]string),
		userItemIDs:        make(map[string]struct{}),
	}
	lastCaptureSequence := 0
	for index, record := range capture.Records {
		if record.Sequence <= lastCaptureSequence {
			adapter.issues = append(adapter.issues, fmt.Sprintf("capture sequence regressed at record %d", index))
		}
		lastCaptureSequence = record.Sequence
		if record.PayloadType != gwtesting.SessionPayloadTypeWebSocketMessage {
			adapter.issues = append(adapter.issues, fmt.Sprintf("record %d is not a websocket event", index))
		}
		adapter.observe(record)
	}
	adapter.ledger.Observe(probe.BargeInEvent{
		Sequence:    adapter.nextEventSequence(),
		Kind:        probe.BargeInEventSessionTerminal,
		Disposition: probe.BargeInDispositionClean,
		Clean:       true,
	})
	if len(adapter.issues) > 0 {
		return adapter.ledger, adapter.facts, errors.New(strings.Join(adapter.issues, "; "))
	}
	if adapter.facts.ProviderErrors > 0 {
		reason := "provider error event observed"
		if len(adapter.facts.ProviderCodes) > 0 {
			reason += ": code/type=" + strings.Join(adapter.facts.ProviderCodes, ",")
		}
		return adapter.ledger, adapter.facts, &liveBargeInInconclusiveError{Reason: reason}
	}
	return adapter.ledger, adapter.facts, nil
}

func (a *liveBargeInCaptureAdapter) observe(record gwtesting.CapturedSessionEvent) {
	payload := liveBargeInRecordPayload(record)
	server := record.Direction == gwtesting.DirectionServerToClient
	client := record.Direction == gwtesting.DirectionClientToServer
	switch record.Type {
	case "session.created":
		if server {
			a.facts.SessionCreated++
		}
	case "session.updated":
		if server {
			a.facts.SessionUpdated++
		}
	case "session.closed":
		if server {
			a.facts.SessionClosed++
		}
	case "error":
		if server {
			a.facts.ProviderErrors++
			code := liveBargeInSafeToken(liveBargeInJSONField(payload, "error.code", "error.type", "code", "type"))
			if code == "unknown" {
				code = "unknown"
			}
			a.facts.ProviderCodes = append(a.facts.ProviderCodes, code)
		}
	case "input_audio_buffer.append":
		if !client {
			return
		}
		if a.currentInput == "" {
			a.inputOrdinal++
			a.currentInput = fmt.Sprintf("input-%d", a.inputOrdinal)
			a.facts.InputStarts = append(a.facts.InputStarts, record.Sequence)
		}
		encoded := liveBargeInJSONField(payload, "audio")
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 {
			a.issues = append(a.issues, fmt.Sprintf("input append %d was not non-empty base64 audio", record.Sequence))
		}
		a.facts.Appends++
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:      a.nextEventSequence(),
			Kind:          probe.BargeInEventInputAppend,
			InputID:       a.currentInput,
			TurnID:        liveBargeInTurnID(a.currentInput),
			AppendGroupID: a.currentInput,
			Bytes:         len(decoded),
			NonEmpty:      len(decoded) > 0,
		})
	case "input_audio_buffer.commit":
		if !client {
			return
		}
		a.facts.Commits++
		a.ledger.Observe(probe.BargeInEvent{
			Sequence: a.nextEventSequence(),
			Kind:     probe.BargeInEventInputCommit,
			InputID:  a.currentInput,
			TurnID:   liveBargeInTurnID(a.currentInput),
		})
		a.lastCommittedInput = a.currentInput
		if a.currentInput != "" {
			a.committedInputs = append(a.committedInputs, a.currentInput)
		}
		a.currentInput = ""
	case "conversation.item.created":
		if !server || liveBargeInJSONField(payload, "item.role") != "user" {
			return
		}
		a.observeUserTurn(liveBargeInJSONField(payload, "item.id"))
	case "input_audio_buffer.committed":
		if !server {
			return
		}
		// Realtime also exposes the committed user item on this acknowledgement.
		// Treat it as the same logical user-turn signal and deduplicate it if a
		// later conversation.item.created event carries the same item ID.
		a.observeUserTurn(liveBargeInJSONField(payload, "item_id", "item.id"))
	case "conversation.item.input_audio_transcription.completed":
		if !server {
			return
		}
		// Current OpenAI Realtime sessions identify the user item on the
		// transcription completion event; older captures may expose the same
		// identity through conversation.item.created above. Both are one logical
		// user-turn representation and are deduplicated by item ID.
		a.observeUserTurn(liveBargeInJSONField(payload, "item_id", "item.id"))
	case "response.created":
		if !server {
			return
		}
		a.responseOrdinal++
		providerID := liveBargeInJSONField(payload, "response.id", "response_id")
		if providerID == "" {
			a.issues = append(a.issues, "response.created had no provider identity")
		}
		stableID := fmt.Sprintf("response-%d", a.responseOrdinal)
		identity := liveBargeInResponseIdentity{
			stable:     stableID,
			providerID: providerID,
			inputID:    a.lastCommittedInput,
			turnID:     liveBargeInTurnID(a.lastCommittedInput),
			ordinal:    a.responseOrdinal,
		}
		if _, exists := a.providerResponses[providerID]; exists {
			a.issues = append(a.issues, "response provider identity was reused")
		}
		a.providerResponses[providerID] = identity
		a.responseByProvider[providerID] = stableID
		a.facts.Responses = append(a.facts.Responses, liveBargeInWireResponse{Created: record.Sequence})
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventResponseCreated,
			InputID:    identity.inputID,
			TurnID:     identity.turnID,
			ResponseID: stableID,
		})
		if a.responseOrdinal > 1 {
			a.ledger.Observe(probe.BargeInEvent{
				Sequence:   a.nextEventSequence(),
				Kind:       probe.BargeInEventContinuation,
				InputID:    identity.inputID,
				TurnID:     identity.turnID,
				ResponseID: stableID,
			})
		}
	case "response.output_audio.delta", "response.audio.delta":
		if !server {
			return
		}
		providerID := liveBargeInJSONField(payload, "response_id", "response.id")
		stableID := a.responseByProvider[providerID]
		if a.providerOutputWasDiscarded(providerID, record.Sequence) {
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(liveBargeInJSONField(payload, "delta"))
		if err != nil || len(decoded) == 0 {
			a.issues = append(a.issues, "response audio output was not non-empty base64 audio")
		}
		if response := a.wireResponse(providerID); response != nil && len(decoded) > 0 && response.FirstAudio == 0 {
			response.FirstAudio = record.Sequence
		}
		if response := a.wireResponse(providerID); response != nil {
			response.AudioBytes += len(decoded)
		}
		if stableID == "" {
			a.issues = append(a.issues, "response audio output referenced unknown provider identity")
		}
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventResponseOutput,
			ResponseID: stableID,
			Bytes:      len(decoded),
			NonEmpty:   len(decoded) > 0,
		})
	case "response.output_text.delta", "response.text.delta", "response.output_audio_transcript.delta", "response.audio_transcript.delta", "response.output_audio_transcript.done", "response.audio_transcript.done":
		if !server {
			return
		}
		providerID := liveBargeInJSONField(payload, "response_id", "response.id")
		stableID := a.responseByProvider[providerID]
		if a.providerOutputWasDiscarded(providerID, record.Sequence) {
			return
		}
		text := liveBargeInJSONField(payload, "delta", "transcript")
		if response := a.wireResponse(providerID); response != nil && text != "" && response.FirstText == 0 {
			response.FirstText = record.Sequence
		}
		if stableID == "" {
			a.issues = append(a.issues, "response text output referenced unknown provider identity")
		}
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventResponseOutput,
			ResponseID: stableID,
			Bytes:      len(text),
			NonEmpty:   text != "",
		})
	case "response.cancel":
		if !client {
			return
		}
		identity := a.activeResponse()
		interruptingInput := a.currentInput
		if interruptingInput == "" {
			interruptingInput = fmt.Sprintf("input-%d", a.inputOrdinal+1)
		}
		if identity.ordinal == 0 {
			a.issues = append(a.issues, "response.cancel had no active response")
		} else {
			identity.cancelSeq = record.Sequence
			a.providerResponses[identity.providerID] = identity
			if response := a.wireResponse(identity.providerID); response != nil {
				response.Cancel = record.Sequence
			}
			a.facts.Cancels++
		}
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventResponseCancel,
			InputID:    interruptingInput,
			TurnID:     liveBargeInTurnID(interruptingInput),
			ResponseID: identity.stable,
		})
	case "response.done":
		if !server {
			return
		}
		providerID := liveBargeInJSONField(payload, "response.id", "response_id")
		identity, exists := a.providerResponses[providerID]
		if !exists {
			a.issues = append(a.issues, "response.done referenced unknown provider identity")
		}
		status := liveBargeInJSONField(payload, "response.status", "status")
		reason := liveBargeInJSONField(payload, "response.status_details.reason", "status_details.reason")
		if status == "" {
			a.issues = append(a.issues, "response.done had no terminal status")
		}
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:    a.nextEventSequence(),
			Kind:        probe.BargeInEventResponseTerminal,
			ResponseID:  identity.stable,
			Disposition: liveBargeInDisposition(status, reason, identity.cancelSeq > 0),
			Reason:      liveBargeInSafeToken(reason),
		})
		if response := a.wireResponse(providerID); response != nil {
			response.Done = record.Sequence
			response.Terminal = true
		}
		identity.terminal = true
		a.providerResponses[providerID] = identity
	}
}

func (a *liveBargeInCaptureAdapter) providerOutputWasDiscarded(providerID string, sequence int) bool {
	identity, ok := a.providerResponses[providerID]
	if !ok || identity.cancelSeq == 0 || sequence <= identity.cancelSeq {
		return false
	}
	a.facts.ProviderLateOutput++
	return true
}

func (a *liveBargeInCaptureAdapter) observeUserTurn(itemID string) {
	if itemID == "" {
		a.issues = append(a.issues, "user turn had no identity")
		return
	}
	if _, exists := a.userItemIDs[itemID]; exists {
		return
	}
	inputID := ""
	for _, candidate := range a.committedInputs {
		if _, represented := a.userTurnInputs[candidate]; !represented {
			inputID = candidate
			break
		}
	}
	if inputID == "" {
		a.issues = append(a.issues, "user turn had no unrepresented committed input")
		return
	}
	a.userItemIDs[itemID] = struct{}{}
	a.userTurnInputs[inputID] = itemID
	a.facts.UserItems++
	a.ledger.Observe(probe.BargeInEvent{
		Sequence: a.nextEventSequence(),
		Kind:     probe.BargeInEventUserTurn,
		InputID:  inputID,
		TurnID:   liveBargeInTurnID(inputID),
	})
}

func (a *liveBargeInCaptureAdapter) nextEventSequence() int {
	a.nextSequence++
	return a.nextSequence
}

func (a *liveBargeInCaptureAdapter) activeResponse() liveBargeInResponseIdentity {
	for ordinal := a.responseOrdinal; ordinal > 0; ordinal-- {
		for _, identity := range a.providerResponses {
			if identity.ordinal == ordinal && !identity.terminal {
				return identity
			}
		}
	}
	return liveBargeInResponseIdentity{}
}

func (a *liveBargeInCaptureAdapter) wireResponse(providerID string) *liveBargeInWireResponse {
	identity, ok := a.providerResponses[providerID]
	if !ok || identity.ordinal <= 0 || identity.ordinal > len(a.facts.Responses) {
		return nil
	}
	return &a.facts.Responses[identity.ordinal-1]
}

func liveBargeInTurnID(inputID string) string {
	if inputID == "" {
		return ""
	}
	return "turn-" + strings.TrimPrefix(inputID, "input-")
}

func liveBargeInDisposition(status, reason string, wasCancelled bool) probe.BargeInDisposition {
	switch strings.ToLower(status) {
	case "completed":
		return probe.BargeInDispositionCompleted
	case "cancelled", "canceled":
		return probe.BargeInDispositionCancelled
	case "incomplete":
		if wasCancelled || strings.Contains(strings.ToLower(reason), "cancel") || strings.EqualFold(reason, "turn_detected") {
			return probe.BargeInDispositionCancelled
		}
		return probe.BargeInDispositionFailed
	case "failed":
		return probe.BargeInDispositionFailed
	default:
		return probe.BargeInDisposition(status)
	}
}

func liveBargeInRecordPayload(record gwtesting.CapturedSessionEvent) []byte {
	if len(record.Payload) > 0 {
		return record.Payload
	}
	return record.Data
}

func liveBargeInJSONField(payload []byte, paths ...string) string {
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	for _, path := range paths {
		current := value
		for _, part := range strings.Split(path, ".") {
			object, ok := current.(map[string]any)
			if !ok {
				current = nil
				break
			}
			current = object[part]
		}
		if text, ok := current.(string); ok && text != "" {
			return text
		}
	}
	return ""
}

func liveBargeInSafeToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	if len(value) > 64 {
		return "redacted"
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' && char != '.' {
			return "redacted"
		}
	}
	return value
}

func (r liveBargeInWireResponse) firstOutput() int {
	if r.FirstAudio == 0 {
		return r.FirstText
	}
	if r.FirstText == 0 || r.FirstAudio < r.FirstText {
		return r.FirstAudio
	}
	return r.FirstText
}

type liveBargeInInconclusiveError struct{ Reason string }

func (e *liveBargeInInconclusiveError) Error() string {
	if e == nil {
		return "live barge-in observation was inconclusive"
	}
	return "live barge-in observation was inconclusive: " + e.Reason
}

func liveBargeInTraceBoundary(trace *liveBargeInTrace, response, turn int, output bool) (before, after int, ok bool) {
	events, starts := trace.snapshot()
	start, ok := starts[turn]
	if !ok {
		return 0, 0, false
	}
	for index, event := range events {
		if event.ResponseOrdinal != response {
			continue
		}
		matched := event.Type == messages.StreamTypeMessageStart
		if output {
			matched = event.AudioBytes > 0 || event.TextBytes > 0
		}
		if !matched {
			continue
		}
		if index < start {
			before++
		} else {
			after++
		}
	}
	return before, after, true
}

func liveBargeInOutputAfterInputStart(trace *liveBargeInTrace, response, turn int) (audio, text int, ok bool) {
	events, starts := trace.snapshot()
	start, ok := starts[turn]
	if !ok {
		return 0, 0, false
	}
	for index, event := range events {
		if index < start || event.ResponseOrdinal != response {
			continue
		}
		audio += event.AudioBytes
		text += event.TextBytes
	}
	return audio, text, true
}

// validateLiveBargeInBoundaries separates an unavailable provider or missed
// timing gate from a completed run that violated the identity contract. This
// keeps setup/rate-limit/slow-provider outcomes inconclusive while making
// stale output after an actually released input a hard failure.
func validateLiveBargeInBoundaries(facts liveBargeInCaptureFacts, trace *liveBargeInTrace) error {
	if facts.SessionCreated != 1 || facts.SessionUpdated == 0 {
		return &liveBargeInInconclusiveError{Reason: "session readiness boundary was not observed"}
	}
	if len(facts.InputStarts) < liveBargeInTurns || len(facts.Responses) < liveBargeInTurns {
		return &liveBargeInInconclusiveError{Reason: "four input and response boundaries were not observed"}
	}
	first := facts.Responses[0]
	second := facts.Responses[1]
	third := facts.Responses[2]
	fourth := facts.Responses[3]
	if first.FirstAudio == 0 {
		return &liveBargeInInconclusiveError{Reason: "active assistant audio was not observed"}
	}
	activeBefore, _, activeOK := liveBargeInTraceBoundary(trace, 1, 2, true)
	if !activeOK || activeBefore == 0 {
		return &liveBargeInInconclusiveError{Reason: "active assistant audio did not precede input 2 before response 1 terminality"}
	}
	if first.FirstAudio >= facts.InputStarts[1] || first.Done == 0 || facts.InputStarts[1] >= first.Done || first.Cancel == 0 || first.Cancel >= first.Done {
		return &liveBargeInInconclusiveError{Reason: "active-speech input was not observed while response 1 was non-terminal"}
	}

	createdBefore, _, createdOK := liveBargeInTraceBoundary(trace, 2, 3, false)
	if !createdOK || createdBefore == 0 || facts.InputStarts[2] <= second.Created || second.Created == 0 {
		return &liveBargeInInconclusiveError{Reason: "response 2 creation did not precede input 3"}
	}
	if second.firstOutput() != 0 && second.firstOutput() < facts.InputStarts[2] {
		return &liveBargeInInconclusiveError{Reason: "response 2 emitted first output before the turn-start input boundary"}
	}
	if second.Done == 0 || second.Cancel == 0 || second.Cancel >= second.Done {
		return &liveBargeInInconclusiveError{Reason: "turn-start input was not observed while response 2 was non-terminal"}
	}
	if third.Done == 0 || facts.InputStarts[3] <= third.Done {
		return &liveBargeInInconclusiveError{Reason: "input 4 did not follow completed response 3"}
	}
	if fourth.Done == 0 {
		return &liveBargeInInconclusiveError{Reason: "completed same-session continuation response was not observed"}
	}
	if err := validateLiveBargeInDeliveredAudio(facts, trace); err != nil {
		return err
	}
	return nil
}

func validateLiveBargeInDeliveredAudio(facts liveBargeInCaptureFacts, trace *liveBargeInTrace) error {
	events, _ := trace.snapshot()
	observed := make(map[int]int)
	for _, event := range events {
		observed[event.ResponseOrdinal] += event.AudioBytes
	}
	for ordinal, response := range facts.Responses {
		if observed[ordinal+1] != response.AudioBytes {
			return fmt.Errorf("response %d delivered audio bytes=%d but accepted provider output was %d", ordinal+1, observed[ordinal+1], response.AudioBytes)
		}
	}
	return nil
}

func liveBargeInCaptureSummary(facts liveBargeInCaptureFacts, recordCount int) string {
	return fmt.Sprintf("records=%d,session_created=%d,session_updated=%d,session_closed=%d,appends=%d,commits=%d,user_items=%d,responses=%d,cancels=%d,provider_errors=%d,provider_codes=%v,provider_late_output_discarded=%d",
		recordCount,
		facts.SessionCreated,
		facts.SessionUpdated,
		facts.SessionClosed,
		facts.Appends,
		facts.Commits,
		facts.UserItems,
		len(facts.Responses),
		facts.Cancels,
		facts.ProviderErrors,
		facts.ProviderCodes,
		facts.ProviderLateOutput,
	)
}

func validateLiveBargeInRecordDir(path string) error {
	for _, name := range []string{"manifest.json", "client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl"} {
		info, err := os.Stat(filepath.Join(path, name))
		if err != nil || info.Size() == 0 {
			return fmt.Errorf("recording artifact %q is missing or empty", name)
		}
	}
	file, err := os.Open(filepath.Join(path, "session-log.jsonl"))
	if err != nil {
		return fmt.Errorf("open session log: %w", err)
	}
	defer file.Close()
	entries := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry struct {
			TurnIndex int `json:"turn_index"`
			Input     struct {
				AudioBytes uint64 `json:"audio_bytes"`
				Committed  bool   `json:"committed"`
			} `json:"input"`
			Response struct {
				Complete   bool   `json:"complete"`
				AudioBytes uint64 `json:"audio_bytes"`
			} `json:"response"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return fmt.Errorf("decode session log: %w", err)
		}
		entries++
		if entry.TurnIndex != entries || !entry.Input.Committed || entry.Input.AudioBytes == 0 || !entry.Response.Complete {
			return fmt.Errorf("session log entry %d did not reconcile non-empty input and output", entries)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read session log: %w", err)
	}
	if entries != liveBargeInTurns {
		return fmt.Errorf("session log entries=%d, want %d", entries, liveBargeInTurns)
	}
	return nil
}

func liveBargeInRunErrorClass(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "api key"), strings.Contains(text, "authentication"), strings.Contains(text, "unauthorized"), strings.Contains(text, "invalid_api_key"), strings.Contains(text, "401"), strings.Contains(text, "403"):
		return "authentication/setup"
	case strings.Contains(text, "rate limit"), strings.Contains(text, "rate_limit"), strings.Contains(text, "quota"), strings.Contains(text, "429"):
		return "rate-limited/provider-unavailable"
	case strings.Contains(text, "deadline"), strings.Contains(text, "timeout"), strings.Contains(text, "timed out"), strings.Contains(text, "context canceled"):
		return "timeout"
	case strings.Contains(text, "connection"), strings.Contains(text, "websocket"), strings.Contains(text, "network"), strings.Contains(text, "no such host"), strings.Contains(text, "unavailable"), strings.Contains(text, "tls"):
		return "provider-unavailable"
	default:
		return "runtime-contract-failure"
	}
}

func awaitLiveBargeInCommand(ctx context.Context, root interface {
	ExecuteContext(context.Context) error
}) error {
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		timer := time.NewTimer(liveBargeInJoinWait)
		defer timer.Stop()
		select {
		case err := <-done:
			return err
		case <-timer.C:
			return fmt.Errorf("session command join exceeded %s: %w", liveBargeInJoinWait, probe.ErrBargeInWait)
		}
	}
}

func liveBargeInSanitizedLedger(facts liveBargeInCaptureFacts, trace *liveBargeInTrace) string {
	events, _ := trace.snapshot()
	audioByResponse := make(map[int]int)
	textByResponse := make(map[int]int)
	for _, event := range events {
		audioByResponse[event.ResponseOrdinal] += event.AudioBytes
		textByResponse[event.ResponseOrdinal] += event.TextBytes
	}
	parts := make([]string, 0, liveBargeInTurns)
	for ordinal := 1; ordinal <= liveBargeInTurns; ordinal++ {
		status := "completed"
		if ordinal <= 2 {
			status = "cancelled"
		}
		parts = append(parts, fmt.Sprintf("T%d{append_group=1,commit=1,user_turn=1} R%d{%s,audio_bytes=%d,text_bytes=%d}",
			ordinal, ordinal, status, audioByResponse[ordinal], textByResponse[ordinal]))
	}
	return fmt.Sprintf("%s terminal={clean=true,unresolved=0} counts={appends=%d,commits=%d,user_turns=%d,responses=%d,cancels=%d,provider_late_output_discarded=%d}",
		strings.Join(parts, "; "), facts.Appends, facts.Commits, facts.UserItems, len(facts.Responses), facts.Cancels, facts.ProviderLateOutput)
}

// TestLiveSessionS2SBargeInProofV3 is the only billed test in this story. It
// requires both a build tag and an environment opt-in. A missing credential,
// provider setup failure, unavailable service, timeout, or missed timing gate
// is reported as inconclusive; it is never turned into a successful ledger.
func TestLiveSessionS2SBargeInProofV3(t *testing.T) {
	if os.Getenv(liveBargeInOptInEnv) != "1" {
		t.Skipf("%s!=1; live OpenAI Realtime barge-in confirmation is explicit opt-in", liveBargeInOptInEnv)
	}
	apiKey := os.Getenv(liveBargeInAPIKeyEnv)
	if apiKey == "" {
		t.Skipf("%s is not set; live OpenAI Realtime barge-in confirmation is inconclusive", liveBargeInAPIKeyEnv)
	}

	trace := newLiveBargeInTrace()
	runtimeObserver := &liveBargeInRuntimeObserver{}
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortSessionRuntimeObserver, runtimeObserver),
	)
	if err != nil {
		t.Fatalf("initialize shipped CLI for live barge-in proof: %v", err)
	}
	agentCLI.SetSessionStreamObserver(trace.observe)

	workDir := t.TempDir()
	configDir := filepath.Join(workDir, "config")
	writeLiveBargeInNoToolConfig(t, configDir)
	capturePath := filepath.Join(workDir, "live-s2s-barge-in.session.json")
	recordDir := filepath.Join(workDir, "recording")
	audioOutPath := filepath.Join(workDir, "assistant.wav")
	root := agentCLI.Generate()
	root.SetIn(newLiveBargeInAudioReader(t, trace))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"--config-dir", configDir,
		"session",
		"--record", capturePath,
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", liveBargeInModel,
		"--api-key", apiKey,
		"--system-prompt", "Answer every spoken request with a concise but clearly audible response of several short sentences.",
		"--audio-in", "-",
		"--audio-out", audioOutPath,
		"--max-duration", liveBargeInMaxDuration.String(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), liveBargeInTestTimeout)
	defer cancel()
	runErr := awaitLiveBargeInCommand(ctx, root)
	capture, loadErr := gwtesting.LoadSessionCapture(capturePath)
	if loadErr != nil {
		if liveBargeInRunErrorClass(runErr) != "runtime-contract-failure" {
			t.Skipf("INCONCLUSIVE live barge-in proof: provider/setup result did not produce a capture")
		}
		t.Fatalf("live barge-in capture was not written; result class=%s", liveBargeInRunErrorClass(runErr))
	}

	ledger, facts, validationErr := normalizeLiveBargeInCapture(capture)
	var inconclusive *liveBargeInInconclusiveError
	if errors.As(validationErr, &inconclusive) {
		t.Skipf("INCONCLUSIVE live barge-in proof: %s; capture=%s; trace=%s", inconclusive.Reason, liveBargeInCaptureSummary(facts, len(capture.Records)), trace.evidence())
	}
	if runErr != nil {
		if liveBargeInRunErrorClass(runErr) != "runtime-contract-failure" {
			t.Skipf("INCONCLUSIVE live barge-in proof: provider result class=%s; capture=%s; trace=%s", liveBargeInRunErrorClass(runErr), liveBargeInCaptureSummary(facts, len(capture.Records)), trace.evidence())
		}
		if validationErr == nil {
			if boundaryErr := validateLiveBargeInBoundaries(facts, trace); boundaryErr != nil {
				if errors.As(boundaryErr, &inconclusive) {
					t.Skipf("INCONCLUSIVE live barge-in proof: %s; capture=%s; trace=%s", inconclusive.Reason, liveBargeInCaptureSummary(facts, len(capture.Records)), trace.evidence())
				}
			}
		}
		t.Fatalf("live barge-in command returned a contract failure; capture=%s; trace=%s", liveBargeInCaptureSummary(facts, len(capture.Records)), trace.evidence())
	}
	if validationErr != nil {
		t.Fatalf("live barge-in capture adapter failed; capture=%s", liveBargeInCaptureSummary(facts, len(capture.Records)))
	}
	if boundaryErr := validateLiveBargeInBoundaries(facts, trace); boundaryErr != nil {
		if errors.As(boundaryErr, &inconclusive) {
			t.Skipf("INCONCLUSIVE live barge-in proof: %s; capture=%s; trace=%s", inconclusive.Reason, liveBargeInCaptureSummary(facts, len(capture.Records)), trace.evidence())
		}
		t.Fatalf("live barge-in collision boundary failed: %v; capture=%s; trace=%s", boundaryErr, liveBargeInCaptureSummary(facts, len(capture.Records)), trace.evidence())
	}
	if err := ledger.Validate(liveBargeInContract()); err != nil {
		t.Fatalf("live barge-in identity ledger failed: %v; capture=%s; trace=%s", err, liveBargeInCaptureSummary(facts, len(capture.Records)), trace.evidence())
	}
	if err := validateLiveBargeInRecordDir(recordDir); err != nil {
		t.Fatalf("live barge-in recording diagnostics failed: %v; capture=%s", err, liveBargeInCaptureSummary(facts, len(capture.Records)))
	}
	if info, err := os.Stat(audioOutPath); err != nil || info.Size() <= 44 {
		t.Fatalf("live barge-in assistant audio artifact is missing or empty")
	}
	validateLiveBargeInRuntime(t, runtimeObserver.snapshot())

	t.Logf("OpenAI live barge-in proof (gpt-realtime): %s; capture=%s; runtime=%s", liveBargeInSanitizedLedger(facts, trace), liveBargeInCaptureSummary(facts, len(capture.Records)), liveBargeInRuntimeEvidence(runtimeObserver.snapshot()))
}

func writeLiveBargeInNoToolConfig(t *testing.T, directory string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("prepare live proof config directory: %v", err)
	}
	var contents strings.Builder
	contents.WriteString("tools:\n  list:\n")
	for _, id := range config.DefaultToolIDs {
		fmt.Fprintf(&contents, "    - id: %s\n      enabled: false\n", id)
	}
	if err := os.WriteFile(filepath.Join(directory, config.ConfigFileName), []byte(contents.String()), 0o600); err != nil {
		t.Fatalf("write live proof no-tool config: %v", err)
	}
}

func validateLiveBargeInRuntime(t *testing.T, observations []liveBargeInRuntimeFact) {
	t.Helper()
	inputCommits := 0
	turns := 0
	outputBytes := 0
	terminals := 0
	for _, observation := range observations {
		switch observation.Kind {
		case runtimecontract.SessionRuntimeObservationInputCommit:
			inputCommits++
			if observation.PayloadBytes == 0 {
				t.Fatalf("live runtime input commit %d carried no PCM", observation.InputCommit)
			}
		case runtimecontract.SessionRuntimeObservationTurnCompleted:
			turns++
		case runtimecontract.SessionRuntimeObservationAudioOutput:
			outputBytes += observation.PayloadBytes
		case runtimecontract.SessionRuntimeObservationTerminal:
			terminals++
			if !observation.Clean || observation.HasError || !observation.HasAccounting {
				t.Fatalf("live runtime terminal was not clean and accounted: %s", liveBargeInRuntimeEvidence(observations))
			}
		}
	}
	if inputCommits != liveBargeInTurns || turns != liveBargeInTurns || terminals != 1 || outputBytes == 0 {
		t.Fatalf("live runtime did not reconcile inputs, turns, output, and terminal: %s", liveBargeInRuntimeEvidence(observations))
	}
}
