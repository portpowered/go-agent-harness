package probe

import (
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
	return record.Peer == transcript.PeerAgent && (record.Stream == transcript.StreamRuntimeMessage || record.Stream == transcript.StreamWS)
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
