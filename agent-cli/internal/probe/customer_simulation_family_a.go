package probe

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

const (
	FamilyAScenarioID = "family-a-iterative-project"

	FamilyAInitialREADME = "# Aurora\n\nA small conversational project.\n"
	FamilyAFinalREADME   = "# Aurora\n\nA small conversational project.\n\nStatus: ready for review.\n"

	familyAEmptyProjectSHA256 = "88b1c13b8a583b27447037cc9cc5a6505e8df6c8f7e21365f567223b2dddbefb"
	familyAInitialREADMEHash  = "80c5d7af4946465790e1fde42f1100c969b7a42564c1a0203d3f14681df1e600"
	familyAFinalREADMEHash    = "2d0f35b4ead27c0082743e865b28ecdde73cb33aac6b5228fffc8d134865d688"
)

// CustomerScriptTurn is the natural-language wording associated with one
// declarative scenario action. The wording is evidence for the customer
// transcript only; it is not sent to the product as a hidden text bridge.
type CustomerScriptTurn struct {
	ActionID string
	Text     string
}

// FamilyASpokenScript returns the four ordered customer utterances used by the
// hermetic Family A process proof. Callers receive a fresh slice and may pace
// or timestamp the turns independently.
func FamilyASpokenScript() []CustomerScriptTurn {
	return []CustomerScriptTurn{
		{ActionID: "create-project-directory", Text: "Could you start an Aurora project by creating the project directory?"},
		{ActionID: "add-readme-content", Text: "Now add the small Aurora README content to that project."},
		{ActionID: "revise-readme", Text: "Please revise the README so its status says it is ready for review."},
		{ActionID: "summarize-final-state", Text: "Please tell me what is actually in the finished project, including the final status."},
	}
}

func isCustomerSimulationAgentRecord(record transcript.Record) bool {
	return record.Peer == transcript.PeerAgent && (record.Stream == transcript.StreamRuntimeAudio || record.Stream == transcript.StreamRuntimeMessage || record.Stream == transcript.StreamWS)
}

func customerSimulationMessageIsAssistant(record customerSimulationRecordedMessage) bool {
	msg := record.message
	if msg.Role == messages.RoleAssistant || msg.ActorID == messages.Model {
		return true
	}
	return record.dir == transcript.DirectionOut && msg.Role != messages.RoleTool && msg.ActorID != messages.Tool
}

func customerSimulationResponseOutputBoundaries(response customerSimulationResponse) (time.Duration, time.Duration) {
	if !response.AudioObserved || response.AudioEnd <= response.AudioStart {
		return 0, 0
	}
	end := response.AudioEnd
	if response.End > end {
		end = response.End
	}
	return response.AudioStart, end
}

// NewFamilyAScenario returns the versioned, filesystem-grounded Family A
// declaration. It intentionally uses four actions: directory creation,
// content addition, modification of prior content, and a final spoken
// summary. Every action has its own checkpoint so a later correct state cannot
// erase an earlier incorrect one.
func NewFamilyAScenario() CustomerScenario {
	allDispositions := []TerminalDisposition{DispositionCompleted, DispositionFailed, DispositionCancelled}
	return CustomerScenario{
		SchemaVersion:  CustomerScenarioSchemaVersion,
		ID:             FamilyAScenarioID,
		Name:           "Iterative Aurora project build-up",
		Family:         ScenarioFamilyA,
		Persona:        "A patient but exacting project collaborator",
		Goal:           "Build a small Aurora project incrementally and report its actual final state",
		WordingFreedom: "Use natural conversational wording while preserving the declared paths, content, order, and final facts.",
		TextSeed:       "The sandbox starts empty. Work through each request in order and confirm only what the filesystem proves.",
		ImageEvents:    []ScenarioImageEvent{},
		Actions: []ActionIntent{
			{
				ID: "create-project-directory", Intent: "Create the project directory.",
				AllowedDispositions:     append([]TerminalDisposition(nil), allDispositions...),
				PartialSideEffectPolicy: PartialSideEffectsPreserve,
				SideEffectRule:          "Preserve the directory if later work is cancelled and report its observed type.",
				Oracle: ActionOracle{
					Description:         "The project directory exists before any file content is added.",
					RequireConfirmation: true,
					RequiredText:        []string{"created the project directory"},
					Checkpoints: []FilesystemExpectation{{
						Path: "project", Type: FileTypeDirectory, SHA256: familyAEmptyProjectSHA256,
					}},
				},
			},
			{
				ID: "add-readme-content", Intent: "Add the initial README content.",
				AllowedDispositions:     append([]TerminalDisposition(nil), allDispositions...),
				PartialSideEffectPolicy: PartialSideEffectsPreserve,
				SideEffectRule:          "Keep the written README and report its exact content if a later action fails.",
				Oracle: ActionOracle{
					Description:         "project/README.md contains the declared initial content.",
					RequireConfirmation: true,
					RequiredText:        []string{"added the README content"},
					Checkpoints: []FilesystemExpectation{{
						Path: "project/README.md", Type: FileTypeFile, SHA256: familyAInitialREADMEHash, Content: FamilyAInitialREADME,
					}},
				},
			},
			{
				ID: "revise-readme", Intent: "Modify the README status from its initial content to ready for review.",
				AllowedDispositions:     append([]TerminalDisposition(nil), allDispositions...),
				PartialSideEffectPolicy: PartialSideEffectsPreserve,
				SideEffectRule:          "Preserve the last actual README state and never claim the revision before the edit is observed.",
				Oracle: ActionOracle{
					Description:         "project/README.md contains the revised final content.",
					RequireConfirmation: true,
					RequiredText:        []string{"updated project/README.md", "ready for review"},
					Checkpoints: []FilesystemExpectation{{
						Path: "project/README.md", Type: FileTypeFile, SHA256: familyAFinalREADMEHash, Content: FamilyAFinalREADME,
					}},
				},
			},
			{
				ID: "summarize-final-state", Intent: "Speak a summary of the actual final project state.",
				AllowedDispositions:     append([]TerminalDisposition(nil), allDispositions...),
				PartialSideEffectPolicy: PartialSideEffectsForbid,
				SideEffectRule:          "Make no filesystem change; preserve the final README checkpoint as the source of truth for the spoken summary.",
				Oracle: ActionOracle{
					Description:         "The spoken summary names the actual README and its ready-for-review status.",
					RequireConfirmation: true,
					RequiredText:        []string{"project/README.md", "ready for review"},
					ForbiddenText:       []string{"draft"},
					Checkpoints: []FilesystemExpectation{{
						Path: "project/README.md", Type: FileTypeFile, SHA256: familyAFinalREADMEHash, Content: FamilyAFinalREADME,
					}},
				},
			},
		},
		Sandbox:      SandboxSpec{Name: "fresh-family-a-sandbox", Root: ".", Fresh: true},
		Interruption: InterruptionTrigger{Kind: InterruptionNone},
		Patience: PatienceThresholds{
			ListenBeforeFollowUp: 500 * time.Millisecond,
			ResponseStart:        time.Second,
			InProgressWork:       2 * time.Second,
			Reprompt:             3 * time.Second,
			AbsoluteDeadAir:      10 * time.Second,
			MaxReprompts:         2,
		},
		Termination: TerminationNatural,
		Deadline:    30 * time.Second,
	}
}

// customerSimulationResponseOutputInterval maps a recorded response's audio
// range onto the actual stdout reads from the shipped child. Session recording
// timestamps are logical and intentionally comparable across runs; they are
// not used as wall-clock evidence for a process-boundary interruption.
func customerSimulationResponseOutputInterval(scenario CustomerScenario, facts customerSimulationRecordingFacts, response customerSimulationResponse, result DuplexRunResult) (time.Duration, time.Duration, bool) {
	if strings.TrimSpace(response.ID) == "" || response.AudioBytes <= 0 {
		return 0, 0, false
	}
	var target customerSimulationResponseAudioRange
	found := false
	for _, candidate := range customerSimulationResponseAudioRanges(scenario, facts.responses) {
		if candidate.ResponseID == response.ID {
			target = candidate
			found = true
			break
		}
	}
	if !found {
		return 0, 0, false
	}

	var previousTotal int64
	var start, end time.Duration
	observed := false
	for _, output := range result.Output {
		outputEnd := output.Total
		if outputEnd <= previousTotal || outputEnd < int64(output.Bytes) {
			outputEnd = previousTotal + int64(output.Bytes)
		}
		outputStart := max(previousTotal, outputEnd-int64(output.Bytes))
		previousTotal = outputEnd

		overlapStart := maxInt64(outputStart, target.Start)
		overlapEnd := minInt64(outputEnd, target.End)
		if overlapEnd <= overlapStart {
			continue
		}
		at := max(output.At, 0)
		partEnd := at + max(customerSimulationPCM16Duration(int(overlapEnd-overlapStart)), time.Nanosecond)
		if !observed {
			start = at
		}
		start = min(start, at)
		end = max(end, partEnd)
		observed = true
	}
	return start, end, observed
}

func customerSimulationRecordedResponse(facts customerSimulationRecordingFacts, index int) customerSimulationResponse {
	responses := customerSimulationResponseCandidates(facts.responses)
	if index < 0 || index >= len(responses) {
		return customerSimulationResponse{}
	}
	return responses[index]
}

func customerSimulationResponseCandidates(responses []customerSimulationResponse) []customerSimulationResponse {
	// A Realtime tool continuation may be a distinct assistant response and
	// may carry a small audio marker of its own. Prefer response boundaries that
	// contain spoken transcript for action-level correction evidence; fall back
	// to audio-bearing boundaries when a provider records audio without a
	// transcript, and only then use every recorded response.
	withTranscript := make([]customerSimulationResponse, 0, len(responses))
	for _, response := range responses {
		if strings.TrimSpace(response.Text) != "" {
			withTranscript = append(withTranscript, response)
		}
	}
	if len(withTranscript) >= 2 {
		return withTranscript
	}
	withAudio := make([]customerSimulationResponse, 0, len(responses))
	for _, response := range responses {
		if response.AudioBytes > 0 {
			withAudio = append(withAudio, response)
		}
	}
	if len(withAudio) >= 2 {
		return withAudio
	}
	return responses
}

const customerSimulationResponseIncomplete = "incomplete"

func customerSimulationResponseStatus(response customerSimulationResponse) string {
	if response.Cancelled {
		return string(DispositionCancelled)
	}
	if response.Complete {
		return string(DispositionCompleted)
	}
	return customerSimulationResponseIncomplete
}

func customerSimulationResponseTime(response customerSimulationResponse, fallback time.Duration, result DuplexRunResult, start bool) time.Duration {
	wallAt := response.WallStart
	if !start {
		wallAt = response.WallEnd
	}
	if converted, ok := customerSimulationRecordedTimeOK(wallAt, result); ok {
		return converted
	}
	return fallback
}

func customerSimulationRecordedTime(wallAt time.Time, fallback time.Duration, result DuplexRunResult) time.Duration {
	if converted, ok := customerSimulationRecordedTimeOK(wallAt, result); ok {
		return converted
	}
	return fallback
}

func customerSimulationRecordedTimeOK(wallAt time.Time, result DuplexRunResult) (time.Duration, bool) {
	if wallAt.IsZero() {
		return 0, false
	}
	base, ok := customerSimulationDuplexWallOrigin(result)
	if !ok {
		return 0, false
	}
	converted := wallAt.Sub(base)
	if converted < 0 {
		return 0, false
	}
	return converted, true
}

func customerSimulationDuplexWallOrigin(result DuplexRunResult) (time.Time, bool) {
	for _, input := range result.Input {
		if !input.Timestamp.IsZero() {
			return input.Timestamp.Add(-input.At), true
		}
	}
	for _, output := range result.Output {
		if !output.Timestamp.IsZero() {
			return output.Timestamp.Add(-output.At), true
		}
	}
	return time.Time{}, false
}

func customerSimulationInputStart(result DuplexRunResult, segmentID string, ordinal int) time.Duration {
	if strings.TrimSpace(segmentID) != "" {
		for _, input := range result.Input {
			if input.SegmentID == segmentID {
				return input.At
			}
		}
	}
	seenSegments := make(map[string]struct{})
	segmentIndex := 0
	for _, input := range result.Input {
		if _, seen := seenSegments[input.SegmentID]; seen {
			continue
		}
		seenSegments[input.SegmentID] = struct{}{}
		if segmentIndex == ordinal {
			return input.At
		}
		segmentIndex++
	}
	return 0
}

func customerSimulationResponseInterval(product []TranscriptEvent, index int) (time.Duration, time.Duration) {
	if index < 0 || index >= len(product) {
		return 0, 0
	}
	start := product[index].At
	end := start + time.Millisecond
	if index+1 < len(product) && product[index+1].At > end {
		end = product[index+1].At
	}
	return start, end
}

// Media timing is joined by the provider's explicit response ID. Never infer
// identity from arrival order of the independently consumed normalized stream.
type customerSimulationMediaBoundary struct {
	Kind        string                                                 `json:"kind"`
	Admission   string                                                 `json:"admission"`
	SampleCount int                                                    `json:"sample_count"`
	Frame       struct{ PlaybackResponse struct{ ResponseID string } } `json:"frame"`
}
type customerSimulationMediaInterval struct{ start, end time.Duration }

func decodeCustomerSimulationMedia(record transcript.Record) (*customerSimulationMediaBoundary, error) {
	var media customerSimulationMediaBoundary
	if err := json.Unmarshal(record.Payload, &media); err != nil {
		return nil, err
	}
	if record.Direction != transcript.DirectionOut || media.Kind != "audio.frame" || media.Admission != "media_bridged" || media.SampleCount <= 0 || media.Frame.PlaybackResponse.ResponseID == "" {
		return nil, nil
	}
	return &media, nil
}

func (p *customerSimulationStreamParser) consumeMediaBoundary(record customerSimulationRecordedMessage) {
	id := record.media.Frame.PlaybackResponse.ResponseID
	if p.mediaByResponse == nil {
		p.mediaByResponse = make(map[string]customerSimulationMediaInterval)
	}
	interval, seen := p.mediaByResponse[id]
	if !seen {
		interval.start = record.at
	}
	interval.end = maxDuration(interval.end, record.at+customerSimulationPCM16Duration(record.media.SampleCount*2))
	p.mediaByResponse[id] = interval
	p.deliveredResponseID = id
	p.inputSpeechActive = false
}

func (p *customerSimulationStreamParser) applyMediaBoundaries() {
	for index := range p.facts.responses {
		response := &p.facts.responses[index]
		if interval, ok := p.mediaByResponse[response.ID]; ok {
			response.AudioObserved = true
			response.AudioStart = interval.start
			response.AudioEnd = interval.end
		}
		if p.facts.cancelObserved && response.ID == p.facts.cancelResponseID {
			response.Cancelled = true
		}
	}
}
