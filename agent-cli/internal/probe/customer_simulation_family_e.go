package probe

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

const (
	FamilyEScenarioID        = "family-e-patient-listener"
	FamilyEActionID          = "complete-after-patience"
	FamilyETurnID            = "family-e-turn-1"
	FamilyEPatienceEventPath = "events/patience.json"
)

var (
	ErrInvalidPatienceEvidence = errors.New("invalid customer simulation patience evidence")
	ErrPatienceClockRegression = errors.New("customer simulation patience clock moved backwards")
	ErrPatienceDecisionDenied  = errors.New("customer simulation patience decision is not currently allowed")
)

// PatienceOutcome is the terminal state recorded by the customer simulator.
// Timeout is deliberately distinct from natural completion: a timeout is
// never allowed to masquerade as a satisfied customer.
type PatienceOutcome string

const (
	PatienceOutcomeCompleted PatienceOutcome = "completed"
	PatienceOutcomeDeadAir   PatienceOutcome = "dead_air"
	PatienceOutcomeTimeout   PatienceOutcome = "timeout"
	PatienceOutcomeCancelled PatienceOutcome = "cancelled"
	PatienceCompleted                        = PatienceOutcomeCompleted
	PatienceDeadAir                          = PatienceOutcomeDeadAir
	PatienceTimeout                          = PatienceOutcomeTimeout
	PatienceCancelled                        = PatienceOutcomeCancelled
)

func (o PatienceOutcome) valid() bool {
	switch o {
	case PatienceOutcomeCompleted, PatienceOutcomeDeadAir, PatienceOutcomeTimeout, PatienceOutcomeCancelled:
		return true
	default:
		return false
	}
}

// PatienceEventKind identifies an observable boundary in the customer/product
// conversation. Events are timestamped as elapsed monotonic durations rather
// than wall-clock strings so a run can be compared without clock skew.
type PatienceEventKind string

const (
	PatienceEventListenStarted     PatienceEventKind = "listen_started"
	PatienceEventResponseStarted   PatienceEventKind = "response_started"
	PatienceEventProductSpeech     PatienceEventKind = "product_speech"
	PatienceEventToolProgress      PatienceEventKind = "tool_progress"
	PatienceEventReprompt          PatienceEventKind = "reprompt"
	PatienceEventResponseCompleted PatienceEventKind = "response_completed"
	PatienceEventDeadAir           PatienceEventKind = "dead_air"
	PatienceEventTimeout           PatienceEventKind = "timeout"
	PatienceEventCancelled         PatienceEventKind = "cancelled"
)

func (k PatienceEventKind) valid() bool {
	switch k {
	case PatienceEventListenStarted, PatienceEventResponseStarted, PatienceEventProductSpeech, PatienceEventToolProgress, PatienceEventReprompt, PatienceEventResponseCompleted, PatienceEventDeadAir, PatienceEventTimeout, PatienceEventCancelled:
		return true
	default:
		return false
	}
}

type PatienceActivityState string

const (
	PatienceActivityListening     PatienceActivityState = "listening"
	PatienceActivityProductSpeech PatienceActivityState = "product_speech"
	PatienceActivityTool          PatienceActivityState = "tool"
	PatienceActivityIdle          PatienceActivityState = "idle"
	PatienceActivityCompleted     PatienceActivityState = "completed"
	PatienceActivityDeadAir       PatienceActivityState = "dead_air"
)

func (s PatienceActivityState) valid() bool {
	switch s {
	case PatienceActivityListening, PatienceActivityProductSpeech, PatienceActivityTool, PatienceActivityIdle, PatienceActivityCompleted, PatienceActivityDeadAir:
		return true
	default:
		return false
	}
}

type PatienceEvent struct {
	ID       string            `json:"id"`
	TurnID   string            `json:"turn_id"`
	Kind     PatienceEventKind `json:"kind"`
	At       time.Duration     `json:"at"`
	Duration time.Duration     `json:"duration,omitempty"`
	Detail   string            `json:"detail,omitempty"`
}

func (e PatienceEvent) validate(field string) error {
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.TurnID) == "" {
		return contractFieldError(ErrInvalidPatienceEvidence, field, "id and turn_id must not be empty")
	}
	if !e.Kind.valid() {
		return contractFieldError(ErrInvalidPatienceEvidence, field+".kind", fmt.Sprintf("%q is invalid", e.Kind))
	}
	if e.At < 0 || e.Duration < 0 {
		return contractFieldError(ErrInvalidPatienceEvidence, field, "at and duration must not be negative")
	}
	if e.Duration > time.Duration(1<<63-1)-e.At {
		return contractFieldError(ErrInvalidPatienceEvidence, field+".duration", "event interval overflows time.Duration")
	}
	return nil
}

type PatienceReprompt struct {
	ID     string        `json:"id"`
	TurnID string        `json:"turn_id"`
	At     time.Duration `json:"at"`
	Text   string        `json:"text"`
	Reason string        `json:"reason"`
}

func (r PatienceReprompt) validate(field string) error {
	if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.TurnID) == "" || strings.TrimSpace(r.Text) == "" || strings.TrimSpace(r.Reason) == "" {
		return contractFieldError(ErrInvalidPatienceEvidence, field, "id, turn_id, text, and reason must not be empty")
	}
	if r.At < 0 {
		return contractFieldError(ErrInvalidPatienceEvidence, field+".at", "must not be negative")
	}
	return nil
}

// PatienceEvidence is the reviewable timing ledger for one customer turn.
// Process and outstanding-tool fields are retained even for dead air, where
// they explain whether the customer was waiting on speech, work, or an
// orphaned product operation.
type PatienceEvidence struct {
	ActionID           string                `json:"action_id"`
	TurnID             string                `json:"turn_id"`
	ListenStartedAt    time.Duration         `json:"listen_started_at"`
	ResponseStartedAt  time.Duration         `json:"response_started_at,omitempty"`
	FirstProgressAt    time.Duration         `json:"first_progress_at,omitempty"`
	LastProgressAt     time.Duration         `json:"last_progress_at,omitempty"`
	TerminalAt         time.Duration         `json:"terminal_at"`
	Outcome            PatienceOutcome       `json:"outcome"`
	ActivityState      PatienceActivityState `json:"activity_state"`
	RepromptCount      int                   `json:"reprompt_count"`
	Reprompts          []PatienceReprompt    `json:"reprompts,omitempty"`
	Events             []PatienceEvent       `json:"events"`
	DeadAirAt          time.Duration         `json:"dead_air_at,omitempty"`
	DeadAirDuration    time.Duration         `json:"dead_air_duration,omitempty"`
	Process            ProcessFacts          `json:"process"`
	OutstandingToolIDs []string              `json:"outstanding_tool_ids,omitempty"`
	CustomerImpact     string                `json:"customer_impact,omitempty"`
	EvidenceRefs       []string              `json:"evidence_refs"`
}

// Validate checks evidence shape and identity. Timing semantics that depend
// on the declared thresholds are left to EvaluateCustomerSimulationPatience
// so malformed-but-readable runs can still receive action-specific findings.
func (e PatienceEvidence) Validate(scenario CustomerScenario) error {
	if err := scenario.Validate(); err != nil {
		return err
	}
	if scenario.Family != ScenarioFamilyE {
		return contractFieldError(ErrInvalidPatienceEvidence, "patience", "requires a Family E scenario")
	}
	if strings.TrimSpace(e.ActionID) == "" || strings.TrimSpace(e.TurnID) == "" {
		return contractFieldError(ErrInvalidPatienceEvidence, "patience", "action_id and turn_id must not be empty")
	}
	knownAction := false
	for _, action := range scenario.Actions {
		if action.ID == e.ActionID {
			knownAction = true
			break
		}
	}
	if !knownAction {
		return contractFieldError(ErrUnknownActionIntent, "patience.action_id", e.ActionID)
	}
	if !e.Outcome.valid() {
		return contractFieldError(ErrInvalidPatienceEvidence, "patience.outcome", fmt.Sprintf("%q is invalid", e.Outcome))
	}
	if !e.ActivityState.valid() {
		return contractFieldError(ErrInvalidPatienceEvidence, "patience.activity_state", fmt.Sprintf("%q is invalid", e.ActivityState))
	}
	for _, field := range []struct {
		name  string
		value time.Duration
	}{
		{"listen_started_at", e.ListenStartedAt},
		{"response_started_at", e.ResponseStartedAt},
		{"first_progress_at", e.FirstProgressAt},
		{"last_progress_at", e.LastProgressAt},
		{"terminal_at", e.TerminalAt},
		{"dead_air_at", e.DeadAirAt},
		{"dead_air_duration", e.DeadAirDuration},
	} {
		if field.value < 0 {
			return contractFieldError(ErrInvalidPatienceEvidence, "patience."+field.name, "must not be negative")
		}
	}
	if e.TerminalAt < e.ListenStartedAt {
		return contractFieldError(ErrInvalidPatienceEvidence, "patience.terminal_at", "must not precede listening")
	}
	if e.FirstProgressAt != 0 && e.LastProgressAt < e.FirstProgressAt {
		return contractFieldError(ErrInvalidPatienceEvidence, "patience.last_progress_at", "must not precede first progress")
	}
	if e.RepromptCount < 0 || e.RepromptCount != len(e.Reprompts) {
		return contractFieldError(ErrInvalidPatienceEvidence, "patience.reprompt_count", "must equal the number of recorded re-prompts and must not be negative")
	}
	if len(e.Events) == 0 {
		return contractFieldError(ErrMissingEvidence, "patience.events", "must contain the listening and observable progress timeline")
	}
	seenEvents := map[string]struct{}{}
	seenReprompts := map[string]struct{}{}
	var previousAt time.Duration
	listenEvent := false
	responseEvent := false
	var firstProgress, lastProgress time.Duration
	hasProgress := false
	for index, event := range e.Events {
		field := fmt.Sprintf("patience.events[%d]", index)
		if err := event.validate(field); err != nil {
			return err
		}
		if event.TurnID != e.TurnID {
			return contractFieldError(ErrInvalidPatienceEvidence, field+".turn_id", "must match patience.turn_id")
		}
		if _, ok := seenEvents[event.ID]; ok {
			return contractFieldError(ErrInvalidPatienceEvidence, field+".id", "must be unique")
		}
		seenEvents[event.ID] = struct{}{}
		if index > 0 && event.At < previousAt {
			return contractFieldError(ErrInvalidPatienceEvidence, field+".at", "timestamps must be monotonic")
		}
		if event.At < e.ListenStartedAt || event.At > e.TerminalAt || addPatienceDuration(event.At, event.Duration) > e.TerminalAt {
			return contractFieldError(ErrInvalidPatienceEvidence, field+".at", "event must be contained between listening and terminal timestamps")
		}
		previousAt = event.At
		switch event.Kind {
		case PatienceEventListenStarted:
			if listenEvent {
				return contractFieldError(ErrInvalidPatienceEvidence, field+".kind", "listen_started may occur only once")
			}
			listenEvent = true
			if event.At != e.ListenStartedAt {
				return contractFieldError(ErrInvalidPatienceEvidence, field+".at", "must match listen_started_at")
			}
		case PatienceEventResponseStarted:
			if responseEvent {
				return contractFieldError(ErrInvalidPatienceEvidence, field+".kind", "response_started may occur only once")
			}
			responseEvent = true
			if e.ResponseStartedAt != event.At {
				return contractFieldError(ErrInvalidPatienceEvidence, field+".at", "must match response_started_at")
			}
			if !hasProgress {
				firstProgress = event.At
				hasProgress = true
			}
			if event.At > lastProgress {
				lastProgress = event.At
			}
		case PatienceEventProductSpeech, PatienceEventToolProgress:
			end := event.At + event.Duration
			if !hasProgress {
				firstProgress = event.At
				hasProgress = true
			}
			if end > lastProgress {
				lastProgress = end
			}
		case PatienceEventReprompt:
		case PatienceEventResponseCompleted, PatienceEventDeadAir, PatienceEventTimeout, PatienceEventCancelled:
		}
	}
	if !listenEvent {
		return contractFieldError(ErrMissingEvidence, "patience.events", "must contain listen_started")
	}
	if e.ResponseStartedAt != 0 && !responseEvent {
		return contractFieldError(ErrMissingEvidence, "patience.events", "must contain response_started")
	}
	if hasProgress {
		if e.FirstProgressAt != firstProgress || e.LastProgressAt != lastProgress {
			return contractFieldError(ErrInvalidPatienceEvidence, "patience.progress", "first and last progress timestamps must match observable events")
		}
	} else if e.FirstProgressAt != 0 || e.LastProgressAt != 0 {
		return contractFieldError(ErrInvalidPatienceEvidence, "patience.progress", "progress timestamps require an observable progress event")
	}
	var previousRepromptAt time.Duration
	for index, reprompt := range e.Reprompts {
		field := fmt.Sprintf("patience.reprompts[%d]", index)
		if err := reprompt.validate(field); err != nil {
			return err
		}
		if reprompt.TurnID != e.TurnID {
			return contractFieldError(ErrInvalidPatienceEvidence, field+".turn_id", "must match patience.turn_id")
		}
		if _, ok := seenReprompts[reprompt.ID]; ok {
			return contractFieldError(ErrInvalidPatienceEvidence, field+".id", "must be unique")
		}
		seenReprompts[reprompt.ID] = struct{}{}
		if index > 0 && reprompt.At < previousRepromptAt {
			return contractFieldError(ErrInvalidPatienceEvidence, field+".at", "timestamps must be monotonic")
		}
		previousRepromptAt = reprompt.At
	}
	for _, field := range []struct {
		name  string
		value []string
	}{
		{"outstanding_tool_ids", e.OutstandingToolIDs},
	} {
		seen := map[string]struct{}{}
		for index, value := range field.value {
			if strings.TrimSpace(value) == "" {
				return contractFieldError(ErrInvalidPatienceEvidence, fmt.Sprintf("patience.%s[%d]", field.name, index), "must not be empty")
			}
			if _, ok := seen[value]; ok {
				return contractFieldError(ErrInvalidPatienceEvidence, "patience."+field.name, "values must be unique")
			}
			seen[value] = struct{}{}
		}
	}
	if err := e.Process.validate("patience.process"); err != nil {
		return err
	}
	if e.Process.EndedAt != 0 && e.Process.EndedAt < e.TerminalAt {
		return contractFieldError(ErrInvalidPatienceEvidence, "patience.process.ended_at", "must not precede patience terminal_at")
	}
	if e.Outcome == PatienceOutcomeDeadAir {
		if e.DeadAirAt == 0 || e.DeadAirDuration <= 0 {
			return contractFieldError(ErrMissingEvidence, "patience.dead_air", "dead-air outcomes require a positive breach timestamp and duration")
		}
		if e.DeadAirAt != e.TerminalAt {
			return contractFieldError(ErrInvalidPatienceEvidence, "patience.dead_air_at", "must match terminal_at")
		}
		if strings.TrimSpace(e.CustomerImpact) == "" {
			return contractFieldError(ErrMissingEvidence, "patience.customer_impact", "dead-air outcomes require customer impact")
		}
	} else if e.DeadAirAt != 0 || e.DeadAirDuration != 0 {
		return contractFieldError(ErrInvalidPatienceEvidence, "patience.dead_air", "only dead-air outcomes may record a dead-air breach")
	}
	if len(e.EvidenceRefs) == 0 {
		return contractFieldError(ErrMissingEvidence, "patience.evidence_refs", "must not be empty")
	}
	return nil
}

// PatienceSnapshot is the observable state consumed by the shared policy.
// The caller supplies elapsed monotonic durations; no wall-clock reads occur
// while a decision is being made.
type PatienceSnapshot struct {
	At              time.Duration
	ListenStartedAt time.Duration
	ResponseStarted bool
	HasProgress     bool
	LastProgressAt  time.Duration
	RepromptAt      []time.Duration
}

type PatienceDecisionKind string

const (
	PatienceDecisionWait     PatienceDecisionKind = "wait"
	PatienceDecisionReprompt PatienceDecisionKind = "reprompt"
	PatienceDecisionDeadAir  PatienceDecisionKind = "dead_air"
	PatienceDecisionComplete PatienceDecisionKind = "complete"
)

type PatienceDecision struct {
	Kind               PatienceDecisionKind
	At                 time.Duration
	SinceLastProgress  time.Duration
	EarliestRepromptAt time.Duration
	RepromptCount      int
	Listening          bool
	ResponseActive     bool
	Reason             string
}

// PatiencePolicy centralizes all threshold decisions used by the live
// simulator and by the hermetic evidence oracle.
type PatiencePolicy struct {
	Thresholds PatienceThresholds
}

func NewPatiencePolicy(thresholds PatienceThresholds) (PatiencePolicy, error) {
	if err := validatePatience(thresholds); err != nil {
		return PatiencePolicy{}, err
	}
	return PatiencePolicy{Thresholds: thresholds}, nil
}

func (p PatiencePolicy) Decide(snapshot PatienceSnapshot) (PatienceDecision, error) {
	if err := validatePatience(p.Thresholds); err != nil {
		return PatienceDecision{}, err
	}
	if snapshot.At < 0 || snapshot.ListenStartedAt < 0 || snapshot.LastProgressAt < 0 {
		return PatienceDecision{}, contractFieldError(ErrInvalidPatienceEvidence, "patience.snapshot", "timestamps must not be negative")
	}
	if snapshot.At < snapshot.ListenStartedAt {
		return PatienceDecision{}, fmt.Errorf("%w: decision precedes listening", ErrPatienceClockRegression)
	}
	if snapshot.HasProgress && snapshot.LastProgressAt < snapshot.ListenStartedAt {
		return PatienceDecision{}, fmt.Errorf("%w: progress precedes listening", ErrPatienceClockRegression)
	}
	if snapshot.HasProgress && snapshot.LastProgressAt > snapshot.At {
		return PatienceDecision{}, fmt.Errorf("%w: progress is later than the decision time", ErrPatienceClockRegression)
	}
	for index, repromptAt := range snapshot.RepromptAt {
		if repromptAt < snapshot.ListenStartedAt || (index > 0 && repromptAt < snapshot.RepromptAt[index-1]) {
			return PatienceDecision{}, fmt.Errorf("%w: reprompt timeline is not monotonic", ErrPatienceClockRegression)
		}
	}
	progressAt := snapshot.ListenStartedAt
	if snapshot.HasProgress {
		progressAt = snapshot.LastProgressAt
	}
	silence := snapshot.At - progressAt
	decision := PatienceDecision{
		At:                snapshot.At,
		SinceLastProgress: silence,
		RepromptCount:     len(snapshot.RepromptAt),
		Listening:         true,
		ResponseActive:    snapshot.ResponseStarted,
	}
	if silence >= p.Thresholds.AbsoluteDeadAir {
		decision.Kind = PatienceDecisionDeadAir
		decision.Reason = fmt.Sprintf("no observable product speech or tool progress for %s", silence)
		return decision, nil
	}
	earliest := addPatienceDuration(snapshot.ListenStartedAt, p.Thresholds.ListenBeforeFollowUp)
	earliest = maxPatienceDuration(earliest, addPatienceDuration(progressAt, p.Thresholds.Reprompt))
	if len(snapshot.RepromptAt) > 0 {
		earliest = maxPatienceDuration(earliest, addPatienceDuration(snapshot.RepromptAt[len(snapshot.RepromptAt)-1], p.Thresholds.Reprompt))
	}
	decision.EarliestRepromptAt = earliest
	if snapshot.At >= earliest && len(snapshot.RepromptAt) < p.Thresholds.MaxReprompts {
		decision.Kind = PatienceDecisionReprompt
		decision.Reason = "the customer has listened through the follow-up threshold without observable progress"
		return decision, nil
	}
	if !snapshot.ResponseStarted && snapshot.At-snapshot.ListenStartedAt >= p.Thresholds.ResponseStart {
		decision.Reason = "the product response has not started yet; continue listening before the bounded check-in"
	} else if snapshot.ResponseStarted && silence >= p.Thresholds.InProgressWork {
		decision.Reason = "work is still active or may be progressing; continue listening until the re-prompt threshold"
	} else {
		decision.Reason = "within the declared listen-before-follow-up window"
	}
	decision.Kind = PatienceDecisionWait
	return decision, nil
}

func addPatienceDuration(left, right time.Duration) time.Duration {
	if right > 0 && left > time.Duration(1<<63-1)-right {
		return time.Duration(1<<63 - 1)
	}
	return left + right
}

func maxPatienceDuration(left, right time.Duration) time.Duration {
	if right > left {
		return right
	}
	return left
}

// PatienceClock is intentionally smaller than time.Timer: the controller
// samples a single monotonic source and tests can replace it with a logical
// clock without sleeping.
type PatienceClock interface {
	Now() time.Time
}

type RealPatienceClock struct{}

func (RealPatienceClock) Now() time.Time { return time.Now() }

// ManualPatienceClock is a deterministic, concurrency-safe clock for policy
// tests. Its elapsed duration is independent of host scheduling and sleeps.
type ManualPatienceClock struct {
	base    time.Time
	elapsed atomic.Int64
}

func NewManualPatienceClock(base time.Time) *ManualPatienceClock {
	if base.IsZero() {
		base = time.Unix(0, 0).UTC()
	}
	return &ManualPatienceClock{base: base}
}

func NewPatienceTestClock() *ManualPatienceClock {
	return NewManualPatienceClock(time.Unix(0, 0).UTC())
}

type DeterministicPatienceClock = ManualPatienceClock

func NewDeterministicPatienceClock(base time.Time) *ManualPatienceClock {
	return NewManualPatienceClock(base)
}

func (c *ManualPatienceClock) Now() time.Time {
	if c == nil {
		return time.Time{}
	}
	return c.base.Add(time.Duration(c.elapsed.Load()))
}

func (c *ManualPatienceClock) Elapsed() time.Duration {
	if c == nil {
		return 0
	}
	return time.Duration(c.elapsed.Load())
}

func (c *ManualPatienceClock) Advance(duration time.Duration) time.Duration {
	if c == nil || duration <= 0 {
		return c.Elapsed()
	}
	for {
		current := c.elapsed.Load()
		if duration > time.Duration(1<<63-1)-time.Duration(current) {
			if c.elapsed.CompareAndSwap(current, int64(1<<63-1)) {
				return time.Duration(1<<63 - 1)
			}
			continue
		}
		next := current + int64(duration)
		if c.elapsed.CompareAndSwap(current, next) {
			return time.Duration(next)
		}
	}
}

func (c *ManualPatienceClock) SetElapsed(elapsed time.Duration) time.Duration {
	if c == nil {
		return 0
	}
	if elapsed < 0 {
		elapsed = 0
	}
	c.elapsed.Store(int64(elapsed))
	return elapsed
}

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

// FamilyESpokenScript gives the patience scenario natural customer wording;
// the policy, rather than a hidden text bridge, decides when a check-in is
// permitted.
func FamilyESpokenScript() []CustomerScriptTurn {
	return []CustomerScriptTurn{{
		ActionID: FamilyEActionID,
		Text:     "Please work through this request while I listen. Take the time you need, and tell me when it is complete.",
	}}
}

func FamilyEReprompt(count int) string {
	if count <= 0 {
		return "I am still listening. Could you give me a quick progress update when you can?"
	}
	return "I am here; is the request still moving forward, or should I wait a little longer?"
}

func FamilyERepromptScript() []string {
	return []string{FamilyEReprompt(0), FamilyEReprompt(1)}
}

func FamilyEPatienceEvidenceRefs() []string {
	return []string{
		FamilyEPatienceEventPath,
		"transcripts/customer.jsonl",
		"transcripts/product.jsonl",
		"tool-observations.jsonl",
		"process.json",
	}
}

func FamilyEPatienceThresholds() PatienceThresholds {
	return PatienceThresholds{
		ListenBeforeFollowUp: 250 * time.Millisecond,
		ResponseStart:        time.Second,
		InProgressWork:       2 * time.Second,
		Reprompt:             3 * time.Second,
		AbsoluteDeadAir:      8 * time.Second,
		MaxReprompts:         1,
	}
}

func NewFamilyEScenario() CustomerScenario {
	return CustomerScenario{
		SchemaVersion:  CustomerScenarioSchemaVersion,
		ID:             FamilyEScenarioID,
		Name:           "Patient Aurora listener",
		Family:         ScenarioFamilyE,
		Persona:        "A human-like customer who listens before checking in",
		Goal:           "Wait for observable product speech or work and surface dead air honestly",
		WordingFreedom: "Use natural wording while preserving the listen, progress, re-prompt, and dead-air boundaries.",
		TextSeed:       "The customer listens while the product speaks or tools make progress. A check-in is allowed only after the re-prompt threshold, and absolute dead air is a broken run.",
		ImageEvents:    []ScenarioImageEvent{},
		Actions: []ActionIntent{{
			ID:                      FamilyEActionID,
			Intent:                  "Handle the customer request while exposing truthful latency and progress.",
			AllowedDispositions:     []TerminalDisposition{DispositionCompleted, DispositionFailed, DispositionCancelled},
			PartialSideEffectPolicy: PartialSideEffectsForbid,
			SideEffectRule:          "Do not create a filesystem artifact; retain the empty sandbox while latency is evaluated.",
			Oracle: ActionOracle{
				Description:         "The request completes with a truthful spoken response and patience evidence; patience/marker.txt remains absent.",
				RequireConfirmation: true,
				RequiredText:        []string{"request is complete"},
				ForbiddenText:       []string{"timeout", "dead air"},
				Checkpoints:         []FilesystemExpectation{{Path: "patience/marker.txt", Type: FileTypeAbsent}},
			},
		}},
		Sandbox:      SandboxSpec{Name: "fresh-family-e-sandbox", Root: ".", Fresh: true},
		Interruption: InterruptionTrigger{Kind: InterruptionNone},
		Patience:     FamilyEPatienceThresholds(),
		Termination:  TerminationNatural,
		Deadline:     20 * time.Second,
	}
}

func patienceFinding(code, actionID, turnID, message string) MechanicalFinding {
	return MechanicalFinding{
		Code: code, ActionID: actionID, TurnID: turnID, Message: message,
		EvidenceRefs: append([]string(nil), FamilyEPatienceEvidenceRefs()...),
	}
}

func evaluatePatienceFindings(scenario CustomerScenario, evidence PatienceEvidence) []MechanicalFinding {
	findings := make([]MechanicalFinding, 0, 8)
	add := func(code, message string) {
		findings = append(findings, patienceFinding(code, evidence.ActionID, evidence.TurnID, message))
	}
	progressAt := evidence.ListenStartedAt
	responseStarted := false
	responseStartedAt := time.Duration(0)
	progressEvents := make([]PatienceEvent, 0)
	repromptEvents := 0
	terminalEvent := false
	for _, event := range evidence.Events {
		switch event.Kind {
		case PatienceEventResponseStarted:
			responseStarted = true
			responseStartedAt = event.At
			progressEvents = append(progressEvents, event)
		case PatienceEventProductSpeech, PatienceEventToolProgress:
			progressEvents = append(progressEvents, event)
		case PatienceEventReprompt:
			repromptEvents++
		case PatienceEventResponseCompleted, PatienceEventDeadAir, PatienceEventTimeout, PatienceEventCancelled:
			terminalEvent = true
		}
	}
	if !responseStarted {
		add("patience_response_never_started", fmt.Sprintf("turn %q has no observable response-start event", evidence.TurnID))
	}
	if !terminalEvent {
		add("patience_terminal_event_missing", fmt.Sprintf("turn %q has no captured terminal event for outcome %q", evidence.TurnID, evidence.Outcome))
	}
	if len(progressEvents) > 0 {
		for _, event := range progressEvents {
			end := addPatienceDuration(event.At, event.Duration)
			if end > progressAt {
				progressAt = end
			}
		}
	}
	if responseStarted && responseStartedAt-evidence.ListenStartedAt > scenario.Patience.ResponseStart {
		add("patience_response_start_timeout", fmt.Sprintf("response for turn %q started at %s, after the response-start threshold of %s", evidence.TurnID, responseStartedAt, scenario.Patience.ResponseStart))
	}
	if !responseStarted && evidence.TerminalAt-evidence.ListenStartedAt >= scenario.Patience.ResponseStart {
		add("patience_response_start_timeout", fmt.Sprintf("turn %q reached terminal observation without a response after %s", evidence.TurnID, evidence.TerminalAt-evidence.ListenStartedAt))
	}
	if evidence.FirstProgressAt != 0 && evidence.FirstProgressAt != progressEventFirst(progressEvents) {
		add("patience_progress_timing_mismatch", "first observable progress timestamp does not match the captured progress event")
	}
	if evidence.LastProgressAt != 0 && evidence.LastProgressAt != progressAt {
		add("patience_progress_timing_mismatch", "last observable progress timestamp does not match the captured progress event")
	}
	if repromptEvents != len(evidence.Reprompts) {
		add("patience_reprompt_evidence_mismatch", fmt.Sprintf("recorded %d re-prompts but captured %d re-prompt events", len(evidence.Reprompts), repromptEvents))
	}
	if len(evidence.Reprompts) > scenario.Patience.MaxReprompts {
		add("patience_reprompt_limit_exceeded", fmt.Sprintf("turn %q sent %d re-prompts but the maximum is %d", evidence.TurnID, len(evidence.Reprompts), scenario.Patience.MaxReprompts))
	}
	for index, reprompt := range evidence.Reprompts {
		lastProgressBefore := evidence.ListenStartedAt
		for _, event := range progressEvents {
			end := addPatienceDuration(event.At, event.Duration)
			if end <= reprompt.At && end > lastProgressBefore {
				lastProgressBefore = end
			}
		}
		earliest := addPatienceDuration(evidence.ListenStartedAt, scenario.Patience.ListenBeforeFollowUp)
		earliest = maxPatienceDuration(earliest, addPatienceDuration(lastProgressBefore, scenario.Patience.Reprompt))
		if index > 0 {
			earliest = maxPatienceDuration(earliest, addPatienceDuration(evidence.Reprompts[index-1].At, scenario.Patience.Reprompt))
		}
		if reprompt.At < earliest {
			add("patience_reprompt_too_early", fmt.Sprintf("re-prompt %q began at %s, before the allowed threshold of %s", reprompt.ID, reprompt.At, earliest))
		}
		for _, event := range evidence.Events {
			if event.Kind != PatienceEventProductSpeech && event.Kind != PatienceEventToolProgress {
				continue
			}
			if reprompt.At >= event.At && reprompt.At <= addPatienceDuration(event.At, event.Duration) {
				add("patience_reprompt_during_progress", fmt.Sprintf("re-prompt %q interrupted observable %s at %s; the customer must listen while progress is active", reprompt.ID, event.Kind, reprompt.At))
			}
		}
	}
	if evidence.TerminalAt > scenario.Deadline {
		add("patience_deadline_exceeded", fmt.Sprintf("turn %q ended at %s beyond the scenario deadline of %s", evidence.TurnID, evidence.TerminalAt, scenario.Deadline))
	}
	if evidence.Outcome == PatienceOutcomeDeadAir {
		expectedAt := addPatienceDuration(progressAt, scenario.Patience.AbsoluteDeadAir)
		expectedDuration := evidence.DeadAirAt - progressAt
		if evidence.DeadAirAt < expectedAt {
			add("patience_dead_air_before_threshold", fmt.Sprintf("dead air was declared at %s, before the absolute threshold of %s from the last observable progress", evidence.DeadAirAt, expectedAt))
		}
		if evidence.DeadAirDuration != expectedDuration {
			add("patience_dead_air_duration_mismatch", fmt.Sprintf("dead-air duration was recorded as %s, but the captured silence was %s", evidence.DeadAirDuration, expectedDuration))
		}
		add("patience_dead_air", fmt.Sprintf("customer turn %q entered dead air for %s after last observable progress at %s; re-prompts=%d; activity=%s; outstanding_tools=%v; process_exit=%s/%d waited=%t descendants_alive=%t; customer impact: %s", evidence.TurnID, evidence.DeadAirDuration, progressAt, evidence.RepromptCount, evidence.ActivityState, evidence.OutstandingToolIDs, evidence.Process.ExitClassification, evidence.Process.ExitCode, evidence.Process.ChildWaited, evidence.Process.DescendantsAlive, evidence.CustomerImpact))
	} else if evidence.Outcome == PatienceOutcomeTimeout {
		add("timeout_not_natural_completion", fmt.Sprintf("turn %q timed out at %s and cannot be classified as natural completion; customer impact: %s", evidence.TurnID, evidence.TerminalAt, evidence.CustomerImpact))
	} else if evidence.Outcome == PatienceOutcomeCancelled {
		add("patience_cancelled", fmt.Sprintf("turn %q was cancelled before a truthful terminal response", evidence.TurnID))
	}
	if evidence.Outcome == PatienceOutcomeCompleted {
		if evidence.ActivityState != PatienceActivityCompleted {
			add("patience_completion_state_mismatch", fmt.Sprintf("completed outcome has activity state %q", evidence.ActivityState))
		}
		if evidence.Process.ExitClassification != "normal" {
			add("patience_success_wrong_process_exit", fmt.Sprintf("completed outcome has process exit classification %q", evidence.Process.ExitClassification))
		}
		if len(evidence.OutstandingToolIDs) > 0 {
			add("patience_unresolved_tool", fmt.Sprintf("completed outcome left outstanding tools: %v", evidence.OutstandingToolIDs))
		}
		if evidence.TerminalAt-progressAt >= scenario.Patience.AbsoluteDeadAir {
			add("patience_dead_air_suppressed", fmt.Sprintf("completed outcome crossed the absolute dead-air threshold without recording a dead-air failure; silence=%s", evidence.TerminalAt-progressAt))
		}
	}
	return findings
}

func progressEventFirst(events []PatienceEvent) time.Duration {
	if len(events) == 0 {
		return 0
	}
	first := events[0].At
	for _, event := range events[1:] {
		if event.At < first {
			first = event.At
		}
	}
	return first
}

// EvaluateCustomerSimulationPatience combines the ordinary action oracle
// with the shared timing policy. A dead-air or timeout finding remains
// action/turn-specific and therefore cannot be hidden by a later success.
func EvaluateCustomerSimulationPatience(
	scenario CustomerScenario,
	actionResults []ActionResult,
	checkpoints []FilesystemCheckpoint,
	toolObservations []ToolObservation,
	productTranscript []TranscriptEvent,
	patience PatienceEvidence,
) (MechanicalVerdict, error) {
	if err := scenario.Validate(); err != nil {
		return MechanicalVerdict{}, err
	}
	if err := patience.Validate(scenario); err != nil {
		return MechanicalVerdict{}, err
	}
	mechanical, err := EvaluateCustomerSimulation(scenario, actionResults, checkpoints, toolObservations, productTranscript)
	if err != nil {
		return mechanical, err
	}
	mechanical.Findings = append(mechanical.Findings, evaluatePatienceFindings(scenario, patience)...)
	mechanical.Pass = len(mechanical.Findings) == 0
	mechanical.Summary = mechanicalSummary(len(mechanical.Findings), len(scenario.Actions))
	if err := mechanical.validate(scenario, "mechanical_verdict"); err != nil {
		return mechanical, err
	}
	return mechanical, nil
}
