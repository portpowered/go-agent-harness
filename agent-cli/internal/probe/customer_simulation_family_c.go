package probe

import (
	"fmt"
	"strings"
	"time"
)

const (
	FamilyCScenarioID       = "family-c-mixed-modal-brief"
	FamilyCImageEventID     = "family-c-indigo-pixel"
	FamilyCCreateActionID   = "create-mixed-modal-brief"
	FamilyCTextActionID     = "add-text-context"
	FamilyCImageActionID    = "record-image-grounded-fact"
	FamilyCImageFixturePath = "fixtures/indigo-pixel.png"

	FamilyCInitialBrief = "# Aurora Mixed-Modal Brief\n\n"
	FamilyCTextBrief    = "# Aurora Mixed-Modal Brief\n\nAudience: engineers.\nTone: concise.\n"
	FamilyCImageFact    = "Observed image: indigo pixel (#4f46e5).\n"

	// The committed fixture is a 1x1 PNG whose only pixel is indigo (#4f46e5).
	// Keeping its digest in the scenario makes a provider response falsifiable
	// without putting image meaning into the spoken customer turn.
	FamilyCImageFixtureSHA256 = "6e9dc04fbe72a6a58b226206c8779938002eb26c9302299190b8a2574e8672a5"

	FamilyCMidSessionImageGapCode = "unsupported_mid_session_image_input"
	FamilyCMidSessionImageGap     = "the shipped agent session CLI exposes --image only on the initial user turn; --audio-in - has no supported public mid-session image injection boundary"
)

// CustomerScriptTurn is declared in customer_simulation_family_a.go. Family C
// deliberately keeps the image fact out of the third spoken turn: the image
// event, not simulator wording, is the source of the grounded fact.

// MixedModalDelivery describes how the declared image crossed (or failed to
// cross) the product boundary. Preloaded and wrong-payload values are
// negative controls, not alternate successful delivery modes.
type MixedModalDelivery string

const (
	MixedModalDeliveryMidSession  MixedModalDelivery = "mid_session"
	MixedModalDeliveryPreloaded   MixedModalDelivery = "preloaded"
	MixedModalDeliveryWrongImage  MixedModalDelivery = "wrong_image"
	MixedModalDeliveryUnsupported MixedModalDelivery = "unsupported"
)

func (d MixedModalDelivery) valid() bool {
	switch d {
	case MixedModalDeliveryMidSession, MixedModalDeliveryPreloaded, MixedModalDeliveryWrongImage, MixedModalDeliveryUnsupported:
		return true
	default:
		return false
	}
}

// MixedModalEvidence records the image boundary separately from ordinary
// action evidence. That distinction lets a run preserve a precise product
// gap rather than laundering a startup image or a wrong fixture into a
// successful later spoken task.
type MixedModalEvidence struct {
	ImageEventID  string `json:"image_event_id"`
	PriorActionID string `json:"prior_action_id"`
	PriorTurnID   string `json:"prior_turn_id"`
	ImageTurnID   string `json:"image_turn_id"`

	PriorActionCompletedAt time.Duration `json:"prior_action_completed_at"`
	CustomerTurnStartedAt  time.Duration `json:"customer_turn_started_at"`
	ImageSentAt            time.Duration `json:"image_sent_at,omitempty"`

	ImageObserved  bool               `json:"image_observed"`
	ExpectedSHA256 string             `json:"expected_sha256"`
	ObservedSHA256 string             `json:"observed_sha256,omitempty"`
	Delivery       MixedModalDelivery `json:"delivery"`
	Supported      bool               `json:"supported"`

	// ImageMeaningInCustomerSpeech is an explicit audit fact. It must remain
	// false for Family C: the spoken request may refer to the image, but may
	// not smuggle its visual answer into speech.
	ImageMeaningInCustomerSpeech bool     `json:"image_meaning_in_customer_speech"`
	ProductGapCode               string   `json:"product_gap_code,omitempty"`
	ProductGap                   string   `json:"product_gap,omitempty"`
	EvidenceRefs                 []string `json:"evidence_refs"`
}

func (e MixedModalEvidence) Validate(scenario CustomerScenario) error {
	if err := scenario.Validate(); err != nil {
		return err
	}
	if scenario.Family != ScenarioFamilyC {
		return contractFieldError(ErrInvalidCustomerEvidence, "mixed_modal", "requires a Family C scenario")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"image_event_id", e.ImageEventID},
		{"prior_action_id", e.PriorActionID},
		{"prior_turn_id", e.PriorTurnID},
		{"image_turn_id", e.ImageTurnID},
		{"expected_sha256", e.ExpectedSHA256},
	} {
		if strings.TrimSpace(field.value) == "" {
			return contractFieldError(ErrInvalidCustomerEvidence, "mixed_modal."+field.name, "must not be empty")
		}
	}
	if e.PriorTurnID == e.ImageTurnID {
		return contractFieldError(ErrInvalidCustomerEvidence, "mixed_modal.image_turn_id", "must identify a turn after the prior text turn")
	}
	if !e.Delivery.valid() {
		return contractFieldError(ErrInvalidCustomerEvidence, "mixed_modal.delivery", fmt.Sprintf("%q is invalid", e.Delivery))
	}
	if err := validateSHA256("mixed_modal.expected_sha256", e.ExpectedSHA256, true); err != nil {
		return err
	}
	if e.ImageObserved {
		if err := validateSHA256("mixed_modal.observed_sha256", e.ObservedSHA256, true); err != nil {
			return err
		}
	} else if e.ObservedSHA256 != "" {
		return contractFieldError(ErrInvalidCustomerEvidence, "mixed_modal.observed_sha256", "must be empty when image_observed is false")
	}
	for _, field := range []struct {
		name  string
		value time.Duration
	}{
		{"prior_action_completed_at", e.PriorActionCompletedAt},
		{"customer_turn_started_at", e.CustomerTurnStartedAt},
		{"image_sent_at", e.ImageSentAt},
	} {
		if field.value < 0 {
			return contractFieldError(ErrInvalidCustomerEvidence, "mixed_modal."+field.name, "must not be negative")
		}
	}
	if e.Supported && !e.ImageObserved {
		return contractFieldError(ErrMissingEvidence, "mixed_modal.image_observed", "supported image delivery needs an observed provider event")
	}
	if !e.Supported {
		if e.Delivery != MixedModalDeliveryUnsupported {
			return contractFieldError(ErrInvalidCustomerEvidence, "mixed_modal.delivery", "unsupported evidence must use delivery=unsupported")
		}
		if strings.TrimSpace(e.ProductGapCode) == "" || strings.TrimSpace(e.ProductGap) == "" {
			return contractFieldError(ErrInvalidCustomerEvidence, "mixed_modal.product_gap", "unsupported delivery needs a precise product gap code and description")
		}
		if e.ImageObserved {
			return contractFieldError(ErrInvalidCustomerEvidence, "mixed_modal.image_observed", "unsupported delivery cannot claim an observed image")
		}
	}
	if len(e.EvidenceRefs) == 0 {
		return contractFieldError(ErrMissingEvidence, "mixed_modal.evidence_refs", "must not be empty")
	}
	var image *ScenarioImageEvent
	for index := range scenario.ImageEvents {
		candidate := &scenario.ImageEvents[index]
		if candidate.ID == e.ImageEventID {
			image = candidate
			break
		}
	}
	if image == nil {
		return contractFieldError(ErrUnknownActionIntent, "mixed_modal.image_event_id", e.ImageEventID)
	}
	if image.SHA256 != e.ExpectedSHA256 {
		return contractFieldError(ErrInvalidCustomerEvidence, "mixed_modal.expected_sha256", "does not match the declared image event")
	}
	return nil
}

// FamilyCSpokenScript returns natural customer wording for text context and
// the later image-grounded request. The third turn does not name the image's
// color or pixels, so a plausible response cannot pass without image bytes.
func FamilyCSpokenScript() []CustomerScriptTurn {
	return []CustomerScriptTurn{
		{ActionID: FamilyCCreateActionID, Text: "Please create a small mixed-modal brief for me."},
		{ActionID: FamilyCTextActionID, Text: "Now add this text context: the audience is engineers and the tone should stay concise."},
		{ActionID: FamilyCImageActionID, Text: "I shared an image after that update. Use the image I just sent to record its distinguishing visual fact in the brief, then tell me what you observed."},
	}
}

// NewFamilyCScenario declares two spoken text iterations followed by one
// image-grounded spoken action. The image is explicitly ordered after the
// second action so a startup --image attachment cannot satisfy the scenario.
func NewFamilyCScenario() CustomerScenario {
	allDispositions := []TerminalDisposition{DispositionCompleted, DispositionFailed, DispositionCancelled}
	return CustomerScenario{
		SchemaVersion:  CustomerScenarioSchemaVersion,
		ID:             FamilyCScenarioID,
		Name:           "Mixed-modal Aurora brief",
		Family:         ScenarioFamilyC,
		Persona:        "A concise collaborator who adds visual context after text work",
		Goal:           "Build a text brief, then record a fact grounded in the actual later image event",
		WordingFreedom: "Use natural wording while preserving the text-first order, later image boundary, fixture digest, and falsifiable final fact.",
		TextSeed:       "Begin with the customer's text context. The image arrives only after the text iteration and its bytes are the source of truth for the final fact.",
		ImageEvents: []ScenarioImageEvent{{
			ID:            FamilyCImageEventID,
			Path:          FamilyCImageFixturePath,
			SHA256:        FamilyCImageFixtureSHA256,
			Text:          "A committed one-pixel indigo fixture delivered after the text context; do not infer its fact from customer wording.",
			AfterActionID: FamilyCTextActionID,
		}},
		Actions: []ActionIntent{
			{
				ID: FamilyCCreateActionID, Intent: "Create mixed-modal brief content.",
				AllowedDispositions: append([]TerminalDisposition(nil), allDispositions...), PartialSideEffectPolicy: PartialSideEffectsPreserve,
				SideEffectRule: "Preserve the initial brief bytes and report them if later image delivery is unavailable.",
				Oracle: ActionOracle{
					Description: "mixed-modal/brief.md exists with the initial brief content.", RequireConfirmation: true,
					RequiredText: []string{"created mixed-modal/brief.md"},
					Checkpoints:  []FilesystemExpectation{{Path: "mixed-modal/brief.md", Type: FileTypeFile, SHA256: sha256HexBytes([]byte(FamilyCInitialBrief)), Content: FamilyCInitialBrief}},
				},
			},
			{
				ID: FamilyCTextActionID, Intent: "Add the declared audience and concise tone text context.",
				AllowedDispositions: append([]TerminalDisposition(nil), allDispositions...), PartialSideEffectPolicy: PartialSideEffectsPreserve,
				SideEffectRule: "Keep the text context as the last truthful brief state even if the later image boundary is unsupported.",
				Oracle: ActionOracle{
					Description: "mixed-modal/brief.md contains the declared text context before any image-grounded work.", RequireConfirmation: true,
					RequiredText: []string{"updated mixed-modal/brief.md", "audience: engineers", "concise"},
					Checkpoints:  []FilesystemExpectation{{Path: "mixed-modal/brief.md", Type: FileTypeFile, SHA256: sha256HexBytes([]byte(FamilyCTextBrief)), Content: FamilyCTextBrief}},
				},
			},
			{
				ID: FamilyCImageActionID, Intent: "Record the actual image pixel fact in the brief.",
				AllowedDispositions: append([]TerminalDisposition(nil), allDispositions...), PartialSideEffectPolicy: PartialSideEffectsPreserve,
				SideEffectRule: "Only write and confirm the image fact after the declared image bytes cross the same supported session boundary; otherwise preserve the gap as BROKEN.",
				Oracle: ActionOracle{
					Description: "The final brief retains text context and mixed-modal/image-fact.txt contains the actual indigo pixel fact.", RequireConfirmation: true,
					RequiredText:  []string{"recorded indigo", "mixed-modal/image-fact.txt", "#4f46e5"},
					ForbiddenText: []string{"image unavailable without evidence"},
					Checkpoints: []FilesystemExpectation{
						{Path: "mixed-modal/brief.md", Type: FileTypeFile, SHA256: sha256HexBytes([]byte(FamilyCTextBrief)), Content: FamilyCTextBrief},
						{Path: "mixed-modal/image-fact.txt", Type: FileTypeFile, SHA256: sha256HexBytes([]byte(FamilyCImageFact)), Content: FamilyCImageFact},
					},
				},
			},
		},
		Sandbox:      SandboxSpec{Name: "fresh-family-c-sandbox", Root: ".", Fresh: true},
		Interruption: InterruptionTrigger{Kind: InterruptionNone},
		Patience: PatienceThresholds{
			ListenBeforeFollowUp: 250 * time.Millisecond, ResponseStart: time.Second, InProgressWork: 2 * time.Second,
			Reprompt: 3 * time.Second, AbsoluteDeadAir: 10 * time.Second, MaxReprompts: 1,
		},
		Termination: TerminationNatural,
		Deadline:    30 * time.Second,
	}
}

func mixedModalEvidenceRefs() []string {
	return []string{
		"scenario.json",
		"transcripts/customer.jsonl",
		"transcripts/product.jsonl",
		"events/audio-turn-events.jsonl",
		"events/mixed-modal.json",
	}
}

// EvaluateCustomerSimulationMixedModal adds the image event boundary and
// fixture-grounding checks to the ordinary action/tool/filesystem oracle.
// An unsupported public seam is intentionally a BROKEN mechanical result with
// an exact gap finding; it is never converted into a successful startup-image
// approximation.
func EvaluateCustomerSimulationMixedModal(
	scenario CustomerScenario,
	actionResults []ActionResult,
	checkpoints []FilesystemCheckpoint,
	toolObservations []ToolObservation,
	productTranscript []TranscriptEvent,
	mixed MixedModalEvidence,
) (MechanicalVerdict, error) {
	if err := scenario.Validate(); err != nil {
		return MechanicalVerdict{}, err
	}
	if err := mixed.Validate(scenario); err != nil {
		return MechanicalVerdict{}, err
	}
	mechanical, err := EvaluateCustomerSimulation(scenario, actionResults, checkpoints, toolObservations, productTranscript)
	if err != nil {
		return mechanical, err
	}
	findings := append([]MechanicalFinding(nil), mechanical.Findings...)
	addFinding := func(code, actionID, turnID, message string) {
		findings = append(findings, MechanicalFinding{
			Code: code, ActionID: actionID, TurnID: turnID, Message: message,
			EvidenceRefs: append([]string(nil), mixedModalEvidenceRefs()...),
		})
	}

	var imageEvent *ScenarioImageEvent
	for index := range scenario.ImageEvents {
		if scenario.ImageEvents[index].ID == mixed.ImageEventID {
			imageEvent = &scenario.ImageEvents[index]
			break
		}
	}
	if imageEvent == nil {
		// Validate already rejects this, but retaining a finding keeps this
		// function defensive if the contract grows a nullable image reference.
		addFinding("missing_image_event", mixed.PriorActionID, mixed.ImageTurnID, "mixed-modal evidence names no declared image event")
	} else if imageEvent.AfterActionID != mixed.PriorActionID {
		addFinding("image_after_action_mismatch", mixed.PriorActionID, mixed.PriorTurnID, fmt.Sprintf("image event %q is declared after %q, not %q", imageEvent.ID, imageEvent.AfterActionID, mixed.PriorActionID))
	}

	resultByID := make(map[string]ActionResult, len(actionResults))
	for _, result := range actionResults {
		resultByID[result.ActionID] = result
	}
	prior, priorObserved := resultByID[mixed.PriorActionID]
	if !priorObserved || prior.Disposition != DispositionCompleted {
		addFinding("image_after_incomplete_text_iteration", mixed.PriorActionID, mixed.PriorTurnID, "the image event did not follow a completed text action")
	}
	if strings.TrimSpace(transcriptTextForTurn(productTranscript, mixed.PriorTurnID)) == "" {
		addFinding("prior_text_turn_missing", mixed.PriorActionID, mixed.PriorTurnID, "no product response was recorded for the text turn before the image event")
	}
	if mixed.CustomerTurnStartedAt <= mixed.PriorActionCompletedAt {
		addFinding("image_before_prior_turn", mixed.PriorActionID, mixed.ImageTurnID, fmt.Sprintf("image-grounded customer turn at %s did not follow prior completion at %s", mixed.CustomerTurnStartedAt, mixed.PriorActionCompletedAt))
	}
	if mixed.ImageObserved {
		if mixed.ImageSentAt <= mixed.PriorActionCompletedAt {
			addFinding("image_before_prior_turn", mixed.PriorActionID, mixed.ImageTurnID, fmt.Sprintf("image provider event at %s preceded prior completion at %s", mixed.ImageSentAt, mixed.PriorActionCompletedAt))
		}
		if mixed.ImageSentAt < mixed.CustomerTurnStartedAt {
			addFinding("image_before_customer_turn", mixed.PriorActionID, mixed.ImageTurnID, fmt.Sprintf("image provider event at %s preceded image-grounded customer speech at %s", mixed.ImageSentAt, mixed.CustomerTurnStartedAt))
		}
		if mixed.ObservedSHA256 != mixed.ExpectedSHA256 {
			addFinding("wrong_image_payload", mixed.ImageActionID(), mixed.ImageTurnID, fmt.Sprintf("provider observed image hash %q, expected fixture hash %q", mixed.ObservedSHA256, mixed.ExpectedSHA256))
		}
	} else if mixed.Delivery != MixedModalDeliveryUnsupported {
		addFinding("missing_image_event", mixed.PriorActionID, mixed.ImageTurnID, "the declared image delivery has no provider-observed image event")
	}

	if mixed.ImageMeaningInCustomerSpeech {
		addFinding("image_meaning_encoded_in_speech", mixed.PriorActionID, mixed.ImageTurnID, "the simulator supplied visual meaning in speech instead of relying on the image event")
	}
	switch mixed.Delivery {
	case MixedModalDeliveryMidSession:
		if !mixed.Supported || !mixed.ImageObserved {
			addFinding("image_boundary_unavailable", mixed.PriorActionID, mixed.ImageTurnID, "mid-session delivery was declared without a supported observed image event")
		}
	case MixedModalDeliveryPreloaded:
		addFinding("image_preloaded_before_prior_turn", mixed.PriorActionID, mixed.ImageTurnID, "the image was attached at session startup and cannot prove delivery after the completed text iteration")
	case MixedModalDeliveryWrongImage:
		addFinding("image_not_mid_session", mixed.PriorActionID, mixed.ImageTurnID, "the image control did not deliver the declared fixture through the later session boundary")
	case MixedModalDeliveryUnsupported:
		if mixed.ProductGapCode != FamilyCMidSessionImageGapCode {
			addFinding("unsupported_gap_unprecise", mixed.PriorActionID, mixed.ImageTurnID, fmt.Sprintf("unsupported image seam reported gap code %q, want %q", mixed.ProductGapCode, FamilyCMidSessionImageGapCode))
		}
		addFinding("unsupported_mid_session_image", mixed.ImageActionID(), mixed.ImageTurnID, fmt.Sprintf("%s: %s", mixed.ProductGapCode, mixed.ProductGap))
	}

	mechanical.Findings = findings
	mechanical.Pass = len(findings) == 0
	mechanical.Summary = mechanicalSummary(len(findings), len(scenario.Actions))
	if err := mechanical.validate(scenario, "mechanical_verdict"); err != nil {
		return mechanical, err
	}
	return mechanical, nil
}

// ImageActionID returns the action after the declared image boundary. It is
// kept derived from the scenario so evidence cannot invent a second action
// identity for the image-grounded request.
func (e MixedModalEvidence) ImageActionID() string {
	return FamilyCImageActionID
}
