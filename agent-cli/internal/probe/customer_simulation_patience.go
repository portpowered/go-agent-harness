package probe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type PatienceController struct {
	scenario        CustomerScenario
	policy          PatiencePolicy
	clock           PatienceClock
	startedAt       time.Time
	lastObservedAt  time.Duration
	listening       bool
	actionID        string
	turnID          string
	events          []PatienceEvent
	reprompts       []PatienceReprompt
	responseStarted bool
	firstProgress   time.Duration
	lastProgress    time.Duration
	hasProgress     bool
	outcome         PatienceOutcome
	terminalAt      time.Duration
	deadAirAt       time.Duration
	deadAirDuration time.Duration
	activityState   PatienceActivityState
	customerImpact  string
}

func NewPatienceController(scenario CustomerScenario, actionID, turnID string, source PatienceClock) (*PatienceController, error) {
	if err := scenario.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(actionID) == "" || strings.TrimSpace(turnID) == "" {
		return nil, contractFieldError(ErrInvalidPatienceEvidence, "patience", "action_id and turn_id must not be empty")
	}
	knownAction := false
	for _, action := range scenario.Actions {
		if action.ID == actionID {
			knownAction = true
			break
		}
	}
	if !knownAction {
		return nil, contractFieldError(ErrUnknownActionIntent, "patience.action_id", actionID)
	}
	policy, err := NewPatiencePolicy(scenario.Patience)
	if err != nil {
		return nil, err
	}
	if source == nil {
		source = RealPatienceClock{}
	}
	return &PatienceController{
		scenario: scenario, policy: policy, clock: source, startedAt: source.Now(), lastObservedAt: -1,
		actionID: actionID, turnID: turnID, activityState: PatienceActivityListening,
	}, nil
}

func (c *PatienceController) elapsed() (time.Duration, error) {
	if c == nil || c.clock == nil {
		return 0, fmt.Errorf("%w: controller clock is unavailable", ErrPatienceClockRegression)
	}
	at := c.clock.Now().Sub(c.startedAt)
	if at < 0 || (c.lastObservedAt >= 0 && at < c.lastObservedAt) {
		return 0, fmt.Errorf("%w: elapsed=%s previous=%s", ErrPatienceClockRegression, at, c.lastObservedAt)
	}
	c.lastObservedAt = at
	return at, nil
}

func (c *PatienceController) ensureActive() error {
	if c == nil {
		return fmt.Errorf("%w: controller is nil", ErrInvalidPatienceEvidence)
	}
	if c.outcome != "" {
		return fmt.Errorf("%w: patience already ended as %q", ErrPatienceDecisionDenied, c.outcome)
	}
	if !c.listening {
		return fmt.Errorf("%w: listening has not started", ErrPatienceDecisionDenied)
	}
	return nil
}

func (c *PatienceController) appendEvent(kind PatienceEventKind, at, duration time.Duration, detail string) error {
	if duration < 0 || at < 0 {
		return fmt.Errorf("%w: invalid event interval", ErrInvalidPatienceEvidence)
	}
	if len(c.events) > 0 && at < c.events[len(c.events)-1].At {
		return fmt.Errorf("%w: event at %s follows %s", ErrPatienceClockRegression, at, c.events[len(c.events)-1].At)
	}
	c.events = append(c.events, PatienceEvent{
		ID: fmt.Sprintf("patience-event-%03d", len(c.events)+1), TurnID: c.turnID,
		Kind: kind, At: at, Duration: duration, Detail: detail,
	})
	return nil
}

func (c *PatienceController) StartListening() error {
	if c == nil {
		return fmt.Errorf("%w: controller is nil", ErrInvalidPatienceEvidence)
	}
	if c.listening {
		return fmt.Errorf("%w: listening already started", ErrPatienceDecisionDenied)
	}
	at, err := c.elapsed()
	if err != nil {
		return err
	}
	if err := c.appendEvent(PatienceEventListenStarted, at, 0, "customer began listening for product progress"); err != nil {
		return err
	}
	c.listening = true
	c.activityState = PatienceActivityListening
	return nil
}

func (c *PatienceController) ObserveResponseStart(detail string) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	if c.responseStarted {
		return fmt.Errorf("%w: response already started", ErrPatienceDecisionDenied)
	}
	at, err := c.elapsed()
	if err != nil {
		return err
	}
	if err := c.appendEvent(PatienceEventResponseStarted, at, 0, detail); err != nil {
		return err
	}
	c.responseStarted = true
	c.recordProgress(at)
	c.activityState = PatienceActivityIdle
	return nil
}

func (c *PatienceController) recordProgress(at time.Duration) {
	if !c.hasProgress {
		c.firstProgress = at
		c.hasProgress = true
	}
	if at > c.lastProgress {
		c.lastProgress = at
	}
}

func (c *PatienceController) ObserveProductSpeech(duration time.Duration, detail string) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	if duration < 0 {
		return fmt.Errorf("%w: speech duration must not be negative", ErrInvalidPatienceEvidence)
	}
	if !c.responseStarted {
		if err := c.ObserveResponseStart("response started with observable product speech"); err != nil {
			return err
		}
	}
	at, err := c.elapsed()
	if err != nil {
		return err
	}
	if at < c.lastProgress {
		return fmt.Errorf("%w: speech begins before previous progress interval ends", ErrPatienceClockRegression)
	}
	if err := c.appendEvent(PatienceEventProductSpeech, at, duration, detail); err != nil {
		return err
	}
	c.recordProgress(addPatienceDuration(at, duration))
	c.activityState = PatienceActivityProductSpeech
	return nil
}

func (c *PatienceController) ObserveToolProgress(duration time.Duration, detail string) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	if duration < 0 {
		return fmt.Errorf("%w: tool progress duration must not be negative", ErrInvalidPatienceEvidence)
	}
	if !c.responseStarted {
		if err := c.ObserveResponseStart("response started with observable tool work"); err != nil {
			return err
		}
	}
	at, err := c.elapsed()
	if err != nil {
		return err
	}
	if at < c.lastProgress {
		return fmt.Errorf("%w: tool progress begins before previous progress interval ends", ErrPatienceClockRegression)
	}
	if err := c.appendEvent(PatienceEventToolProgress, at, duration, detail); err != nil {
		return err
	}
	c.recordProgress(addPatienceDuration(at, duration))
	c.activityState = PatienceActivityTool
	return nil
}

func (c *PatienceController) Decision() (PatienceDecision, error) {
	if err := c.ensureActive(); err != nil {
		return PatienceDecision{}, err
	}
	at, err := c.elapsed()
	if err != nil {
		return PatienceDecision{}, err
	}
	return c.policy.Decide(PatienceSnapshot{
		At: at, ListenStartedAt: c.listenStartedAt(), ResponseStarted: c.responseStarted,
		HasProgress: c.hasProgress, LastProgressAt: c.lastProgress, RepromptAt: c.repromptTimes(),
	})
}

func (c *PatienceController) listenStartedAt() time.Duration {
	if len(c.events) == 0 {
		return 0
	}
	return c.events[0].At
}

func (c *PatienceController) repromptTimes() []time.Duration {
	times := make([]time.Duration, len(c.reprompts))
	for index, reprompt := range c.reprompts {
		times[index] = reprompt.At
	}
	return times
}

func (c *PatienceController) Reprompt(text string) (PatienceReprompt, error) {
	if err := c.ensureActive(); err != nil {
		return PatienceReprompt{}, err
	}
	if strings.TrimSpace(text) == "" {
		return PatienceReprompt{}, fmt.Errorf("%w: re-prompt text must not be empty", ErrInvalidPatienceEvidence)
	}
	decision, err := c.Decision()
	if err != nil {
		return PatienceReprompt{}, err
	}
	if decision.Kind != PatienceDecisionReprompt {
		return PatienceReprompt{}, fmt.Errorf("%w: %s", ErrPatienceDecisionDenied, decision.Reason)
	}
	at, err := c.elapsed()
	if err != nil {
		return PatienceReprompt{}, err
	}
	reprompt := PatienceReprompt{
		ID: fmt.Sprintf("patience-reprompt-%03d", len(c.reprompts)+1), TurnID: c.turnID, At: at,
		Text: text, Reason: decision.Reason,
	}
	if err := c.appendEvent(PatienceEventReprompt, at, 0, text); err != nil {
		return PatienceReprompt{}, err
	}
	c.reprompts = append(c.reprompts, reprompt)
	c.activityState = PatienceActivityListening
	return reprompt, nil
}

func (c *PatienceController) complete(outcome PatienceOutcome, eventKind PatienceEventKind, activity PatienceActivityState, impact string) error {
	if err := c.ensureActive(); err != nil {
		return err
	}
	at, err := c.elapsed()
	if err != nil {
		return err
	}
	if err := c.appendEvent(eventKind, at, 0, impact); err != nil {
		return err
	}
	c.outcome, c.terminalAt, c.activityState, c.customerImpact = outcome, at, activity, impact
	if outcome == PatienceOutcomeDeadAir {
		progressAt := c.listenStartedAt()
		if c.hasProgress {
			progressAt = c.lastProgress
		}
		c.deadAirAt = at
		c.deadAirDuration = at - progressAt
	}
	return nil
}

func (c *PatienceController) Complete() error {
	return c.complete(PatienceOutcomeCompleted, PatienceEventResponseCompleted, PatienceActivityCompleted, "the product response reached a terminal completion")
}

func (c *PatienceController) DeclareDeadAir() error {
	decision, err := c.Decision()
	if err != nil {
		return err
	}
	if decision.Kind != PatienceDecisionDeadAir {
		return fmt.Errorf("%w: dead air is not yet beyond the absolute threshold", ErrPatienceDecisionDenied)
	}
	return c.complete(PatienceOutcomeDeadAir, PatienceEventDeadAir, PatienceActivityDeadAir, "The customer waited beyond the absolute dead-air threshold without observable progress.")
}

func (c *PatienceController) Timeout() error {
	return c.complete(PatienceOutcomeTimeout, PatienceEventTimeout, PatienceActivityDeadAir, "The run deadline elapsed before the customer received a terminal response.")
}

func (c *PatienceController) Cancel() error {
	return c.complete(PatienceOutcomeCancelled, PatienceEventCancelled, PatienceActivityDeadAir, "The customer session was cancelled before a terminal response.")
}

func (c *PatienceController) Evidence(process ProcessFacts, outstandingToolIDs, refs []string) (PatienceEvidence, error) {
	if c == nil || c.outcome == "" {
		return PatienceEvidence{}, fmt.Errorf("%w: controller needs a terminal outcome", ErrMissingEvidence)
	}
	if len(refs) == 0 {
		refs = FamilyEPatienceEvidenceRefs()
	}
	evidence := PatienceEvidence{
		ActionID: c.actionID, TurnID: c.turnID, ListenStartedAt: c.listenStartedAt(),
		ResponseStartedAt: c.responseStartAt(), FirstProgressAt: c.firstProgress, LastProgressAt: c.lastProgress,
		TerminalAt: c.terminalAt, Outcome: c.outcome, ActivityState: c.activityState,
		RepromptCount: len(c.reprompts), Reprompts: append([]PatienceReprompt(nil), c.reprompts...),
		Events: append([]PatienceEvent(nil), c.events...), DeadAirAt: c.deadAirAt, DeadAirDuration: c.deadAirDuration,
		Process: process, OutstandingToolIDs: append([]string(nil), outstandingToolIDs...), CustomerImpact: c.customerImpact,
		EvidenceRefs: append([]string(nil), refs...),
	}
	if err := evidence.Validate(c.scenario); err != nil {
		return PatienceEvidence{}, err
	}
	return evidence, nil
}

func (c *PatienceController) responseStartAt() time.Duration {
	for _, event := range c.events {
		if event.Kind == PatienceEventResponseStarted {
			return event.At
		}
	}
	return 0
}

func observeCustomerSimulationOutput(controller *PatienceController, progress *DuplexProgress, outputIndex *int) error {
	events := progress.OutputEvents()
	if *outputIndex > len(events) {
		*outputIndex = len(events)
	}
	for *outputIndex < len(events) {
		event := events[*outputIndex]
		*outputIndex = *outputIndex + 1
		if event.Bytes <= 0 {
			continue
		}
		if !controller.responseStarted {
			if err := controller.ObserveResponseStart(fmt.Sprintf("stdout read %d crossed the product audio boundary", event.Read)); err != nil {
				return err
			}
		}
		if err := controller.ObserveProductSpeech(0, fmt.Sprintf("stdout read %d carried %d product PCM bytes", event.Read, event.Bytes)); err != nil {
			return err
		}
	}
	return nil
}

func waitForCustomerSimulationPatienceChange(ctx context.Context, progress *DuplexProgress) error {
	waitContext, cancel := context.WithTimeout(ctx, customerSimulationPatienceWakeInterval)
	defer cancel()
	err := progress.WaitForChange(waitContext)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}

func completeCustomerSimulationPatience(controller *PatienceController) error {
	if controller.outcome == "" {
		return controller.Complete()
	}
	return nil
}

func finishCustomerSimulationPatienceOnContext(controller *PatienceController, ctx context.Context, waitErr error) error {
	if ctx.Err() != nil && controller.outcome == "" {
		var terminalErr error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			terminalErr = controller.Timeout()
		} else {
			terminalErr = controller.Cancel()
		}
		return errors.Join(waitErr, terminalErr)
	}
	return waitErr
}
