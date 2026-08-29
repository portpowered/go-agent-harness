package probe

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCustomerSimulationResponseBoundariesUseRecordedIdentityAndOutputReads(t *testing.T) {
	scenario := NewFamilyBScenario()
	base := time.Unix(100, 0).UTC()
	responses := []customerSimulationResponse{
		{ID: "tool-continuation", AudioBytes: 2, WallStart: base.Add(time.Second), WallEnd: base.Add(2 * time.Second)},
		{ID: "original", Text: "Created the draft.", AudioBytes: 4, WallStart: base.Add(2 * time.Second), WallEnd: base.Add(3 * time.Second)},
		{ID: "replacement", Text: "Created the final file.", AudioBytes: 4, WallStart: base.Add(4 * time.Second)},
	}
	facts := customerSimulationRecordingFacts{responses: responses}
	ranges := customerSimulationResponseAudioRanges(scenario, responses)
	if len(ranges) != 3 || ranges[0].TurnID != "turn-1" || ranges[1].TurnID != "turn-1" || ranges[2].TurnID != "turn-2" {
		t.Fatalf("response audio ranges = %+v, want continuation/original on turn-1 and replacement on turn-2", ranges)
	}
	if ranges[0].Start != 0 || ranges[0].End != 2 || ranges[1].Start != 2 || ranges[1].End != 6 || ranges[2].Start != 6 || ranges[2].End != 10 {
		t.Fatalf("response byte ranges = %+v, want contiguous [0,2), [2,6), [6,10)", ranges)
	}

	for _, test := range []struct {
		name      string
		timestamp time.Time
		wantIndex int
		wantOK    bool
	}{
		{name: "continuation belongs to original turn", timestamp: base.Add(1500 * time.Millisecond), wantIndex: 0, wantOK: true},
		{name: "replacement response", timestamp: base.Add(4500 * time.Millisecond), wantIndex: 1, wantOK: true},
		{name: "open response remains attributable", timestamp: base.Add(10 * time.Second), wantIndex: 1, wantOK: true},
		{name: "before first response", timestamp: base.Add(500 * time.Millisecond), wantOK: false},
		{name: "zero timestamp", wantOK: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotIndex, gotOK := customerSimulationResponseForOutputTimestamp(scenario, responses, test.timestamp)
			if gotIndex != test.wantIndex || gotOK != test.wantOK {
				t.Fatalf("response attribution = %d, %v; want %d, %v", gotIndex, gotOK, test.wantIndex, test.wantOK)
			}
		})
	}

	result := DuplexRunResult{Output: []DuplexOutputEvent{
		{Read: 1, Bytes: 3, Total: 3, At: time.Millisecond},
		{Read: 2, Bytes: 3, Total: 6, At: 2 * time.Millisecond},
		{Read: 3, Bytes: 4, Total: 10, At: 3 * time.Millisecond},
	}}
	start, end, ok := customerSimulationResponseOutputInterval(scenario, facts, responses[1], result)
	if !ok || start != time.Millisecond || end <= start {
		t.Fatalf("original response output interval = %s-%s, %v; want observed interval across reads", start, end, ok)
	}
	if _, _, ok := customerSimulationResponseOutputInterval(scenario, facts, customerSimulationResponse{ID: "missing", AudioBytes: 2}, result); ok {
		t.Fatal("missing response identity unexpectedly mapped to an output interval")
	}
	if got := customerSimulationOutputPartsForRanges(4, 4, ranges); got != nil {
		t.Fatalf("empty output range parts = %+v, want nil", got)
	}
	if got := customerSimulationPCM16Duration(4); got != 125*time.Microsecond {
		t.Fatalf("PCM16 duration for four bytes = %s, want 125µs", got)
	}
}

func TestCustomerSimulationEvidenceHelpersPreserveFallbackStates(t *testing.T) {
	withTranscript := []customerSimulationResponse{{ID: "text-1", Text: "one"}, {ID: "text-2", Text: "two", AudioBytes: 2}}
	if got := customerSimulationResponseCandidates(withTranscript); len(got) != 2 || got[0].ID != "text-1" {
		t.Fatalf("transcript response candidates = %+v, want transcript-bearing responses", got)
	}
	withAudio := []customerSimulationResponse{{ID: "audio-1", AudioBytes: 2}, {ID: "audio-2", AudioBytes: 4}}
	if got := customerSimulationResponseCandidates(withAudio); len(got) != 2 || got[1].ID != "audio-2" {
		t.Fatalf("audio response candidates = %+v, want audio-bearing responses", got)
	}
	original := []customerSimulationResponse{{ID: "only"}}
	if got := customerSimulationResponseCandidates(original); len(got) != 1 || got[0].ID != "only" {
		t.Fatalf("fallback response candidates = %+v, want original responses", got)
	}
	if got := customerSimulationRecordedResponse(customerSimulationRecordingFacts{responses: withTranscript}, 5); got.ID != "" {
		t.Fatalf("out-of-range recorded response = %+v, want zero response", got)
	}
	if customerSimulationResponseStatus(customerSimulationResponse{Cancelled: true}) != "cancelled" ||
		customerSimulationResponseStatus(customerSimulationResponse{Complete: true}) != "completed" ||
		customerSimulationResponseStatus(customerSimulationResponse{}) != "incomplete" {
		t.Fatal("recorded response statuses did not preserve cancelled, completed, and incomplete states")
	}

	base := time.Unix(200, 0).UTC()
	result := DuplexRunResult{
		Input: []DuplexInputEvent{{SegmentID: "first", At: 2 * time.Second, Timestamp: base.Add(2 * time.Second)}, {SegmentID: "first", At: 3 * time.Second, Timestamp: base.Add(3 * time.Second)}, {SegmentID: "second", At: 4 * time.Second, Timestamp: base.Add(4 * time.Second)}},
	}
	response := customerSimulationResponse{WallStart: base.Add(5 * time.Second), WallEnd: base.Add(6 * time.Second), Start: time.Second, End: 2 * time.Second}
	if got := customerSimulationResponseTime(response, response.Start, result, true); got != 5*time.Second {
		t.Fatalf("wall-clock response start = %s, want 5s", got)
	}
	if got := customerSimulationResponseTime(response, response.End, result, false); got != 6*time.Second {
		t.Fatalf("wall-clock response end = %s, want 6s", got)
	}
	if got := customerSimulationRecordedTime(time.Time{}, 7*time.Second, result); got != 7*time.Second {
		t.Fatalf("missing wall-clock fallback = %s, want 7s", got)
	}
	if got, ok := customerSimulationRecordedTimeOK(base.Add(-time.Second), result); ok || got != 0 {
		t.Fatalf("pre-origin recorded time = %s, %v; want unavailable", got, ok)
	}
	if got, ok := customerSimulationRecordedTimeOK(base.Add(5*time.Second), result); !ok || got != 5*time.Second {
		t.Fatalf("valid recorded time = %s, %v; want 5s", got, ok)
	}
	if origin, ok := customerSimulationDuplexWallOrigin(result); !ok || !origin.Equal(base) {
		t.Fatalf("input wall origin = %s, %v; want %s", origin, ok, base)
	}
	if origin, ok := customerSimulationDuplexWallOrigin(DuplexRunResult{Output: []DuplexOutputEvent{{At: time.Second, Timestamp: base.Add(time.Second)}}}); !ok || !origin.Equal(base) {
		t.Fatalf("output wall origin = %s, %v; want %s", origin, ok, base)
	}
	if _, ok := customerSimulationDuplexWallOrigin(DuplexRunResult{}); ok {
		t.Fatal("empty duplex result unexpectedly had a wall origin")
	}
	if got := customerSimulationInputStart(result, "second", 0); got != 4*time.Second {
		t.Fatalf("named input start = %s, want 4s", got)
	}
	if got := customerSimulationInputStart(result, "missing", 1); got != 4*time.Second {
		t.Fatalf("ordinal input start = %s, want second segment at 4s", got)
	}
	if got := customerSimulationInputStart(result, "missing", 8); got != 0 {
		t.Fatalf("missing ordinal input start = %s, want zero", got)
	}
	product := []TranscriptEvent{{At: 3 * time.Second}, {At: 5 * time.Second}}
	if start, end := customerSimulationResponseInterval(product, 0); start != 3*time.Second || end != 5*time.Second {
		t.Fatalf("first product interval = %s-%s, want 3s-5s", start, end)
	}
	if start, end := customerSimulationResponseInterval(product, 1); start != 5*time.Second || end != 5*time.Second+time.Millisecond {
		t.Fatalf("last product interval = %s-%s, want one-millisecond fallback", start, end)
	}
	if start, end := customerSimulationResponseInterval(product, -1); start != 0 || end != 0 {
		t.Fatalf("invalid product interval = %s-%s, want zero interval", start, end)
	}

	mixed := customerSimulationMixedModalEvidence(NewFamilyCScenario(), PairedTranscripts{Product: product}, DuplexRunResult{})
	if mixed.PriorActionCompletedAt != 5*time.Second || mixed.Delivery != MixedModalDeliveryUnsupported || mixed.Supported {
		t.Fatalf("mixed-modal fallback evidence = %+v, want explicit unsupported gap", mixed)
	}

	dFacts := customerSimulationRecordingFacts{tools: []ToolObservation{
		{ID: "done", Status: "completed", ResultSeen: true},
		{ID: "pending", Status: "started", ResultSeen: false},
	}}
	natural := NewFamilyDScenario(TerminationNatural)
	naturalEvidence := customerSimulationTerminationEvidence(natural, []TranscriptEvent{{At: 2 * time.Second, Final: true}}, ProcessFacts{}, DuplexRunResult{}, dFacts)
	if naturalEvidence.ActiveResponseStatus != "completed" || !naturalEvidence.SatisfactionDeclared || naturalEvidence.SatisfactionAt == 0 || len(naturalEvidence.OutstandingToolIDs) != 1 {
		t.Fatalf("natural termination fallback evidence = %+v, want completed satisfaction and pending tool", naturalEvidence)
	}
	sigint := NewFamilyDScenario(TerminationSIGINT)
	sigintEvidence := customerSimulationTerminationEvidence(sigint, []TranscriptEvent{{At: time.Second}}, ProcessFacts{SignalSent: true, Signal: "SIGINT", SignalAt: 1500 * time.Millisecond}, DuplexRunResult{}, dFacts)
	if sigintEvidence.ActiveResponseStatus != "interrupted" {
		t.Fatalf("SIGINT termination evidence = %+v, want interrupted", sigintEvidence)
	}
	dFacts.cancelObserved = true
	sigintEvidence = customerSimulationTerminationEvidence(sigint, []TranscriptEvent{{At: time.Second}}, ProcessFacts{SignalSent: true, Signal: "SIGINT", SignalAt: 1500 * time.Millisecond}, DuplexRunResult{}, dFacts)
	if sigintEvidence.ActiveResponseStatus != "cancelled" {
		t.Fatalf("cancelled termination evidence = %+v, want cancelled", sigintEvidence)
	}

	eScenario := NewFamilyEScenario()
	fallback := customerSimulationPatienceEvidence(eScenario, nil, ProcessFacts{ExitClassification: "normal", EndedAt: 5 * time.Millisecond}, DuplexRunResult{Output: []DuplexOutputEvent{{Read: 1, Bytes: 4, At: 2 * time.Millisecond}}}, nil, customerSimulationRecordingFacts{}, nil)
	if fallback.Outcome == PatienceOutcomeCompleted || fallback.FirstProgressAt != 2*time.Millisecond || len(fallback.Events) < 3 {
		t.Fatalf("patience fallback evidence = %+v, want observed progress without fabricated completion", fallback)
	}
	timedOut := customerSimulationPatienceEvidence(eScenario, nil, ProcessFacts{ExitClassification: "timeout", EndedAt: 5 * time.Millisecond}, DuplexRunResult{TimedOut: true}, nil, customerSimulationRecordingFacts{}, nil)
	if timedOut.Outcome != PatienceOutcomeTimeout || timedOut.DeadAirAt != 0 {
		t.Fatalf("timeout patience fallback evidence = %+v, want timeout without dead-air fields", timedOut)
	}
	cancelled := customerSimulationPatienceEvidence(eScenario, nil, ProcessFacts{ExitClassification: "cancelled", EndedAt: 5 * time.Millisecond}, DuplexRunResult{Cancelled: true}, nil, customerSimulationRecordingFacts{}, nil)
	if cancelled.Outcome != PatienceOutcomeCancelled {
		t.Fatalf("cancelled patience fallback evidence = %+v, want cancelled", cancelled)
	}

	clock := NewPatienceTestClock()
	controller := customerSimulationTestPatienceController(t, eScenario, clock)
	clock.Advance(time.Millisecond)
	if err := controller.ObserveResponseStart("response started"); err != nil {
		t.Fatalf("ObserveResponseStart: %v", err)
	}
	clock.Advance(time.Millisecond)
	if err := controller.ObserveProductSpeech(time.Millisecond, "progress"); err != nil {
		t.Fatalf("ObserveProductSpeech: %v", err)
	}
	clock.Advance(time.Millisecond)
	if err := controller.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	controllerEvidence := customerSimulationPatienceEvidence(eScenario, nil, familyEProcess(evidenceTerminalAt(controller), "normal"), DuplexRunResult{}, []ToolObservation{{ID: "pending", Status: "started"}}, customerSimulationRecordingFacts{}, controller)
	if controllerEvidence.Outcome != PatienceOutcomeCompleted || len(controllerEvidence.OutstandingToolIDs) != 1 {
		t.Fatalf("controller patience evidence = %+v, want completed evidence with pending tool", controllerEvidence)
	}
}

func TestCustomerSimulationPatienceRunnerUsesObservableProgressAndBounds(t *testing.T) {
	scenario := NewFamilyEScenario()

	if err := waitForCustomerSimulationPatienceReprompt(context.Background(), nil, nil, nil, nil); !errors.Is(err, ErrCustomerSimulationRun) {
		t.Fatalf("nil reprompt runner error = %v, want ErrCustomerSimulationRun", err)
	}
	if err := waitForCustomerSimulationPatienceCompletion(context.Background(), nil, nil, nil, -1); !errors.Is(err, ErrCustomerSimulationRun) {
		t.Fatalf("nil completion runner error = %v, want ErrCustomerSimulationRun", err)
	}

	clock := NewPatienceTestClock()
	controller := customerSimulationTestPatienceController(t, scenario, clock)
	state := newDuplexProgressState()
	state.setStartedAt(clock.Now())
	progress := &DuplexProgress{state: state}
	state.noteOutput(DuplexOutputEvent{Read: 1, Bytes: 4, Total: 4, At: time.Millisecond})
	state.noteOutputClosed()
	outputIndex := 0
	repromptOutputIndex := -1
	if err := waitForCustomerSimulationPatienceReprompt(context.Background(), controller, progress, &outputIndex, &repromptOutputIndex); !errors.Is(err, errDuplexInputComplete) {
		t.Fatalf("closed-output reprompt result = %v, want input-complete boundary", err)
	}
	if controller.outcome != PatienceOutcomeCompleted || outputIndex != 1 {
		t.Fatalf("closed-output controller = %+v, output index %d; want completed after one output event", controller, outputIndex)
	}
	if err := completeCustomerSimulationPatience(controller); err != nil {
		t.Fatalf("complete already terminal patience: %v", err)
	}

	clock = NewPatienceTestClock()
	controller = customerSimulationTestPatienceController(t, scenario, clock)
	state = newDuplexProgressState()
	state.setStartedAt(clock.Now())
	progress = &DuplexProgress{state: state}
	clock.Advance(scenario.Patience.Reprompt)
	outputIndex = 0
	repromptOutputIndex = -1
	if err := waitForCustomerSimulationPatienceReprompt(context.Background(), controller, progress, &outputIndex, &repromptOutputIndex); err != nil {
		t.Fatalf("bounded reprompt result = %v", err)
	}
	if len(controller.reprompts) != 1 || repromptOutputIndex != 0 {
		t.Fatalf("reprompt state = %+v, output index %d; want one reprompt at current output boundary", controller.reprompts, repromptOutputIndex)
	}

	clock = NewPatienceTestClock()
	controller = customerSimulationTestPatienceController(t, scenario, clock)
	state = newDuplexProgressState()
	state.setStartedAt(clock.Now())
	progress = &DuplexProgress{state: state}
	clock.Advance(scenario.Patience.AbsoluteDeadAir)
	if err := waitForCustomerSimulationPatienceReprompt(context.Background(), controller, progress, new(int), new(int)); err == nil || controller.outcome != PatienceOutcomeDeadAir {
		t.Fatalf("dead-air reprompt result = %v, controller = %+v; want bounded dead-air failure", err, controller)
	}

	clock = NewPatienceTestClock()
	controller = customerSimulationTestPatienceController(t, scenario, clock)
	state = newDuplexProgressState()
	state.setStartedAt(clock.Now())
	progress = &DuplexProgress{state: state}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForCustomerSimulationPatienceReprompt(ctx, controller, progress, new(int), new(int)); !errors.Is(err, context.Canceled) || controller.outcome != PatienceOutcomeCancelled {
		t.Fatalf("cancelled reprompt result = %v, controller = %+v; want cancellation", err, controller)
	}

	clock = NewPatienceTestClock()
	controller = customerSimulationTestPatienceController(t, scenario, clock)
	state = newDuplexProgressState()
	state.setStartedAt(clock.Now())
	progress = &DuplexProgress{state: state}
	state.noteOutput(DuplexOutputEvent{Read: 1, Bytes: 4, Total: 4, At: time.Millisecond})
	state.noteOutputClosed()
	outputIndex = 0
	if err := waitForCustomerSimulationPatienceCompletion(context.Background(), controller, progress, &outputIndex, -1); err != nil {
		t.Fatalf("completion after output close = %v", err)
	}
	if controller.outcome != PatienceOutcomeCompleted {
		t.Fatalf("completion controller outcome = %q, want completed", controller.outcome)
	}

	clock = NewPatienceTestClock()
	controller = customerSimulationTestPatienceController(t, scenario, clock)
	state = newDuplexProgressState()
	state.setStartedAt(clock.Now())
	progress = &DuplexProgress{state: state}
	state.noteOutputClosed()
	outputIndex = 0
	if err := waitForCustomerSimulationPatienceCompletion(context.Background(), controller, progress, &outputIndex, 0); err == nil || controller.outcome != PatienceOutcomeCancelled {
		t.Fatalf("missing post-reprompt output result = %v, controller = %+v; want cancellation", err, controller)
	}

	clock = NewPatienceTestClock()
	controller = customerSimulationTestPatienceController(t, scenario, clock)
	state = newDuplexProgressState()
	state.setStartedAt(clock.Now())
	progress = &DuplexProgress{state: state}
	clock.Advance(scenario.Patience.AbsoluteDeadAir)
	if err := waitForCustomerSimulationPatienceCompletion(context.Background(), controller, progress, new(int), 0); !errors.Is(err, errDuplexInputComplete) || controller.outcome != PatienceOutcomeDeadAir {
		t.Fatalf("dead-air completion result = %v, controller = %+v; want input-complete dead-air boundary", err, controller)
	}
}

func TestCustomerSimulationOptionsRejectInvalidAudioAndRunRoots(t *testing.T) {
	scenario := NewFamilyAScenario()
	script := FamilyASpokenScript()
	base := CustomerSimulationSuiteOptions{
		BinaryPath: "/bin/true", Provider: "openai", Model: "gpt-realtime",
		Runs: []CustomerSimulationRunSpec{{Scenario: scenario, Script: script, Audio: [][]byte{{1, 0}}}},
	}
	for _, test := range []struct {
		name string
		edit func(*CustomerSimulationSuiteOptions)
		want error
	}{
		{name: "missing binary", edit: func(options *CustomerSimulationSuiteOptions) { options.BinaryPath = "" }, want: ErrCustomerSimulationSelection},
		{name: "missing runs", edit: func(options *CustomerSimulationSuiteOptions) { options.Runs = nil }, want: ErrCustomerSimulationSelection},
		{name: "missing provider", edit: func(options *CustomerSimulationSuiteOptions) { options.Provider = "" }, want: ErrCustomerSimulationSelection},
		{name: "unsupported provider", edit: func(options *CustomerSimulationSuiteOptions) { options.Provider = "unsupported" }, want: ErrCustomerSimulationSelection},
		{name: "invalid scenario", edit: func(options *CustomerSimulationSuiteOptions) { options.Runs[0].Scenario.Actions = nil }, want: ErrCustomerSimulationSelection},
		{name: "script count mismatch", edit: func(options *CustomerSimulationSuiteOptions) { options.Runs[0].Script = nil }, want: ErrCustomerSimulationAudio},
		{name: "audio count mismatch", edit: func(options *CustomerSimulationSuiteOptions) { options.Runs[0].Audio = nil }, want: ErrCustomerSimulationAudio},
		{name: "odd PCM16", edit: func(options *CustomerSimulationSuiteOptions) { options.Runs[0].Audio = [][]byte{{1}} }, want: ErrCustomerSimulationAudio},
		{name: "missing action wording", edit: func(options *CustomerSimulationSuiteOptions) {
			options.Runs[0].Script = []CustomerScriptTurn{{ActionID: script[0].ActionID}}
		}, want: ErrCustomerSimulationAudio},
		{name: "missing action ID", edit: func(options *CustomerSimulationSuiteOptions) {
			options.Runs[0].Script = []CustomerScriptTurn{{Text: script[0].Text}}
		}, want: ErrCustomerSimulationAudio},
		{name: "family E needs reprompt audio", edit: func(options *CustomerSimulationSuiteOptions) {
			options.Runs[0].Scenario = NewFamilyEScenario()
			options.Runs[0].Script = FamilyESpokenScript()
		}, want: ErrCustomerSimulationAudio},
		{name: "non-family E reprompt audio", edit: func(options *CustomerSimulationSuiteOptions) { options.Runs[0].PatienceRepromptAudio = []byte{1, 0} }, want: ErrCustomerSimulationAudio},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := base
			options.Runs = append([]CustomerSimulationRunSpec(nil), base.Runs...)
			options.Runs[0].Audio = append([][]byte(nil), base.Runs[0].Audio...)
			options.Runs[0].Script = append([]CustomerScriptTurn(nil), base.Runs[0].Script...)
			test.edit(&options)
			if err := validateCustomerSimulationOptions(options); !errors.Is(err, test.want) {
				t.Fatalf("validate options error = %v, want %v", err, test.want)
			}
		})
	}

	custom := scenario
	custom.ID = "custom"
	custom.Actions = []ActionIntent{custom.Actions[0]}
	custom.Actions[0].Description = ""
	custom.Actions[0].Intent = "perform the custom action"
	customScript := CustomerSimulationScenarioScript(custom)
	if len(customScript) != 1 || customScript[0].Text != custom.Actions[0].Intent {
		t.Fatalf("custom fallback script = %+v, want intent wording", customScript)
	}

	root, cleanup, err := customerSimulationRunRoot("")
	if err != nil {
		t.Fatalf("empty run root: %v", err)
	}
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		t.Fatalf("empty run root path = %q, stat = %v, want directory", root, statErr)
	}
	cleanup()
	createdRoot := filepath.Join(t.TempDir(), "created")
	if _, cleanup, err := customerSimulationRunRoot(createdRoot); err != nil {
		t.Fatalf("create run root: %v", err)
	} else {
		cleanup()
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocked run root: %v", err)
	}
	if _, _, err := customerSimulationRunRoot(blocked); !errors.Is(err, ErrCustomerSimulationRun) {
		t.Fatalf("file run root error = %v, want ErrCustomerSimulationRun", err)
	}
	failed := failedCustomerSimulationResult("failed-001", scenario, "bundle", "record", "workspace", errors.New("child failed"))
	if failed.RunID != "failed-001" || failed.Process.ExitClassification != "failed" || !strings.Contains(failed.Error, "child failed") {
		t.Fatalf("failed customer simulation result = %+v, want bounded failed result", failed)
	}
}

func TestDuplexProgressExposesOutputAndExpectedCloseBoundaries(t *testing.T) {
	state := newDuplexProgressState()
	base := time.Now()
	state.setStartedAt(base)
	progress := &DuplexProgress{state: state}
	state.noteInputSegment()
	state.noteInput([]byte{1, 2})
	state.noteOutput(DuplexOutputEvent{Read: 1, Bytes: 2, Total: 2, At: time.Millisecond, Timestamp: base.Add(time.Millisecond)})
	if snapshot := progress.Snapshot(); snapshot.InputBytes != 2 || snapshot.InputFrames != 1 || snapshot.InputSegments != 1 || snapshot.OutputBytes != 2 || snapshot.OutputReads != 1 {
		t.Fatalf("progress snapshot = %+v, want input/output counters", snapshot)
	}
	if progress.Elapsed() < 0 || len(progress.OutputEvents()) != 1 {
		t.Fatalf("progress elapsed/events = %s/%+v, want elapsed and one output event", progress.Elapsed(), progress.OutputEvents())
	}
	if err := progress.WaitForOutputBytes(context.Background(), 2); err != nil {
		t.Fatalf("WaitForOutputBytes: %v", err)
	}
	if err := progress.WaitForOutputReads(context.Background(), 1); err != nil {
		t.Fatalf("WaitForOutputReads: %v", err)
	}
	state.noteOutputClosed()
	if !progress.OutputClosed() {
		t.Fatal("OutputClosed = false after output pump close")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	waitingProgress := &DuplexProgress{state: newDuplexProgressState()}
	if err := waitingProgress.WaitForChange(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitForChange error = %v, want context cancellation", err)
	}
	if err := (&DuplexProgress{}).WaitForOutputBytes(context.Background(), 1); !errors.Is(err, ErrDuplexPipe) {
		t.Fatalf("nil progress output wait = %v, want ErrDuplexPipe", err)
	}
	if err := (&DuplexProgress{}).WaitForOutputReads(context.Background(), 1); !errors.Is(err, ErrDuplexPipe) {
		t.Fatalf("nil progress read wait = %v, want ErrDuplexPipe", err)
	}
	if err := (&DuplexProgress{}).WaitForChange(context.Background()); !errors.Is(err, ErrDuplexPipe) {
		t.Fatalf("nil progress change wait = %v, want ErrDuplexPipe", err)
	}
	if (&DuplexProgress{}).OutputClosed() || (&DuplexProgress{}).Elapsed() != 0 {
		t.Fatal("nil progress exposed non-zero state")
	}
	if err := progress.WaitForOutputBytes(context.Background(), 0); err != nil {
		t.Fatalf("non-positive output wait: %v", err)
	}

	result := DuplexRunResult{ExitCode: 0, ChildWaited: true}
	if !isExpectedDuplexWaitClose(result, os.ErrClosed) || !isExpectedDuplexWaitClose(result, io.ErrClosedPipe) {
		t.Fatal("closed wait errors were not recognized as expected zero-exit cleanup")
	}
	if isExpectedDuplexWaitClose(result, errors.New("other wait failure")) {
		t.Fatal("unrelated wait error was recognized as expected cleanup")
	}
	if !isExpectedDuplexPipeClosure(errors.New("write: broken pipe")) || !isExpectedDuplexPipeClosure(io.ErrClosedPipe) {
		t.Fatal("closed input pipe errors were not recognized")
	}
	if isExpectedDuplexPipeClosure(errors.New("permission denied")) {
		t.Fatal("unrelated pipe error was recognized as closed input")
	}
	if got := duplexExitClassification(result, TerminationNatural, io.ErrClosedPipe); got != "normal" {
		t.Fatalf("zero-exit close classification = %q, want normal", got)
	}
	if got := duplexExitClassification(DuplexRunResult{ExitCode: 1, ChildWaited: true}, TerminationNatural, nil); got != "failed" {
		t.Fatalf("non-zero classification = %q, want failed", got)
	}
}

func customerSimulationTestPatienceController(t *testing.T, scenario CustomerScenario, clock *ManualPatienceClock) *PatienceController {
	t.Helper()
	controller, err := NewPatienceController(scenario, FamilyEActionID, FamilyETurnID, clock)
	if err != nil {
		t.Fatalf("NewPatienceController: %v", err)
	}
	if err := controller.StartListening(); err != nil {
		t.Fatalf("StartListening: %v", err)
	}
	return controller
}
