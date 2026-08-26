package probe

// Registered scenarios for the s2s-v3c repeated-barge-in vertical: at least
// three barge-in interruptions land against in-flight responses within one
// session, and the conversation bookkeeping must reconcile exactly — every
// interrupted response is cancelled exactly once, every committed user turn
// survives, delivered assistant turns appear once each, and cancelled turns
// leak no post-cancel deltas.
//
// The positive case replays a fixture whose counts satisfy both v3c
// invariants. Each negative control replays a fixture that violates exactly
// one of them (a duplicated delivered turn, a dropped user commit, a
// double-cancelled response), so the corresponding assertion must fail when
// driven through the CLI. All cases run offline through the CLI probe
// entrypoints over the fixtures named by their scenario ID.

const (
	// ScenarioIDS2SV3CBargeInRepeated selects the whole v3c suite; every
	// registered case whose ID extends this prefix runs when it is selected.
	ScenarioIDS2SV3CBargeInRepeated = "s2s-v3c-barge-in-repeated"
	// ScenarioIDS2SV3CBargeInRepeatedDuplicatedTurn is the negative control
	// whose fixture re-emits one already-delivered assistant turn.
	ScenarioIDS2SV3CBargeInRepeatedDuplicatedTurn = ScenarioIDS2SV3CBargeInRepeated + "-duplicated-turn"
	// ScenarioIDS2SV3CBargeInRepeatedDroppedCommit is the negative control
	// whose fixture loses one committed user utterance.
	ScenarioIDS2SV3CBargeInRepeatedDroppedCommit = ScenarioIDS2SV3CBargeInRepeated + "-dropped-commit"
	// ScenarioIDS2SV3CBargeInRepeatedDoubleCancel is the negative control
	// whose fixture cancels one interrupted response twice.
	ScenarioIDS2SV3CBargeInRepeatedDoubleCancel = ScenarioIDS2SV3CBargeInRepeated + "-double-cancel"
)

// ScenarioTextS2SV3COpening is the text prompt opening every v3c case; the
// three interrupting utterances then arrive as send_audio steps separated by
// advance_to waits so each lands while a response is still streaming.
const ScenarioTextS2SV3COpening = "Brief the weather, then hold for follow-ups."

// v3cInterruptions is the exact number of barge-in cancellations the positive
// session records — one per interrupted in-flight response.
const v3cInterruptions = 3

// v3cExpectedComposition is the explicitly expected final composition of the
// positive session: 7 committed user turns (3 initial + 3 interrupting + 1
// closing), 7 responses created (3 interrupted + 3 replacements + 1 closing),
// 4 delivered assistant turns (3 replacements + 1 closing), 3 interrupts
// cancelled exactly once each, and zero deltas leaking after any cancel.
const v3cExpectedComposition = "user_turns=7,assistant_delivered=4,responses_created=7," +
	"cancelled_responses=3,cancel_events=3,post_cancel_deltas=0"

// v3cCorpusID names the synthetic utterance corpus for interrupting turn n.
func v3cCorpusID(turn int) string {
	return "v3c-utterance-" + string(rune('0'+turn))
}

// v3cSteps interleaves the opening text prompt with three send_audio turns,
// each followed by an advance_to wait, so three responses are interrupted
// mid-flight before the session closes.
func v3cSteps() []Step {
	steps := []Step{{Type: StepSendText, Text: ScenarioTextS2SV3COpening}}
	for turn := 1; turn <= v3cInterruptions; turn++ {
		steps = append(steps,
			Step{Type: StepSendAudio, CorpusID: v3cCorpusID(turn)},
			Step{Type: StepAdvanceTo, At: LogicalTime(10 * turn)},
		)
	}
	return append(steps, Step{Type: StepClose})
}

// v3cExpectations declares the lane's two reconciliation invariants plus, for
// the positive case only, the clean synthetic terminal reason.
func v3cExpectations(positive bool) []ExpectedBehavior {
	expectations := []ExpectedBehavior{
		{Type: ExpectBargeInCancelOnce, Kind: ExpectBargeInCancelOnce, Count: v3cInterruptions},
		{Type: ExpectMessageCountsReconcile, Kind: ExpectMessageCountsReconcile, Value: v3cExpectedComposition},
	}
	if positive {
		expectations = append(expectations,
			ExpectedBehavior{Type: ExpectTerminalReason, Kind: ExpectTerminalReason, Value: "synthetic"})
	}
	return expectations
}

func init() {
	for _, registration := range []struct {
		id          string
		description string
		positive    bool
	}{
		{
			id:          ScenarioIDS2SV3CBargeInRepeated,
			description: "Positive case: three mid-response barge-ins cancel exactly once each and cumulative message counts reconcile with no loss or duplication",
			positive:    true,
		},
		{
			id:          ScenarioIDS2SV3CBargeInRepeatedDuplicatedTurn,
			description: "Negative control: a duplicated delivered assistant turn must fail the composition reconciliation",
		},
		{
			id:          ScenarioIDS2SV3CBargeInRepeatedDroppedCommit,
			description: "Negative control: a lost committed user utterance must fail the composition reconciliation",
		},
		{
			id:          ScenarioIDS2SV3CBargeInRepeatedDoubleCancel,
			description: "Negative control: double-cancelling one interrupted response must fail the cancel-exactly-once assertion",
		},
	} {
		scenario := Scenario{
			ID:               registration.id,
			Name:             registration.id,
			Description:      registration.description,
			Steps:            v3cSteps(),
			Expectations:     v3cExpectations(registration.positive),
			Expected:         v3cExpectations(registration.positive),
			ExpectedBehavior: v3cExpectations(registration.positive),
		}
		if err := RegisterScenario(scenario); err != nil {
			panic(err)
		}
	}
}

// The two v3c expectation kinds join the scenario validator's per-kind field
// whitelist here rather than in scenario.go: that file already sits at the
// configured file-length lint ceiling on main, and the lane must not add new
// violations. Package-level variables are initialized before any init runs,
// so these entries are in place for every validation below.
func init() {
	typedExpectationFieldsByKind[ExpectBargeInCancelOnce] = map[string]bool{}
	typedExpectationFieldsByKind[ExpectMessageCountsReconcile] = map[string]bool{"value": true}
}
