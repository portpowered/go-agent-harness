package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/operations"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/spf13/cobra"
)

func TestWebMCPDirectSelectReplacesStalePersistedSelectionAndActivateRestoresIt(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	_, oldTarget, oldCandidate, _ := directFixture()
	oldCandidate.BrowserInstanceID = randomizedWebMCPInstanceID(t)
	oldSelection := WebMCPSelection{
		Version:           WebMCPSelectionVersion,
		EndpointID:        string(oldCandidate.ID),
		BrowserID:         string(oldCandidate.ID),
		BrowserInstanceID: oldCandidate.BrowserInstanceID,
		TargetID:          string(oldTarget.ID),
		Origin:            oldTarget.Origin,
		ContinuityMarker:  "old-document",
		Generation:        4,
		SelectedAt:        time.Unix(4, 0).UTC(),
	}
	if err := store.Save(oldSelection); err != nil {
		t.Fatalf("save stale selection: %v", err)
	}
	page, target, candidate, tool := directFixture()
	candidate.ID = webmcp.BrowserID(randomizedWebMCPTestID(t, "browser-new-"))
	candidate.BrowserInstanceID = randomizedWebMCPInstanceID(t)
	target.BrowserID = candidate.ID
	target.ID = webmcp.TargetID(randomizedWebMCPTestID(t, "target-new-"))
	target.ContinuityMarker = "new-document"
	target.Generation = 9
	page.Key = webmcp.PageKey{BrowserID: candidate.ID, TargetID: target.ID}
	page.Generation = target.Generation
	page.Origin = target.Origin
	catalog := webmcp.ToolCatalogSnapshot{
		Context:    page,
		Generation: page.Generation,
		Tools:      []webmcp.ToolDescriptor{tool},
	}
	replacement := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		catalog:    catalog,
	}

	// Supplying the endpoint and single-selection policy is the documented
	// recovery shape after a browser restart. The stale file must not be
	// loaded as a prerequisite for this explicit select operation.
	selected := executeDirectCommand(t, configDir, store, directFactory(replacement),
		"select", "--cdp-url", testCDPURL, "--auto-select", "single", "--json")
	requireDirectSuccess(t, selected)
	updated, err := store.Load()
	if err != nil {
		t.Fatalf("load replacement selection: %v", err)
	}
	if updated.BrowserID != string(candidate.ID) || updated.BrowserInstanceID != candidate.BrowserInstanceID ||
		updated.TargetID != string(target.ID) || updated.Generation != target.Generation || updated.ContinuityMarker != target.ContinuityMarker {
		t.Fatalf("replacement selection = %+v, want live identity over stale=%+v", updated, oldSelection)
	}
	if len(replacement.selectCalls) != 1 || replacement.selectCalls[0] != (webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}) {
		t.Fatalf("replacement select calls = %+v", replacement.selectCalls)
	}

	// A subsequent command with no IDs must consume the newly persisted exact
	// target, just as it would in a fresh process after the recovery command.
	activation := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	activated := executeDirectCommand(t, configDir, store, directFactory(activation), "activate", "--json")
	requireDirectSuccess(t, activated)
	if len(activation.activateCalls) != 1 || activation.activateCalls[0] != (webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}) {
		t.Fatalf("restored activation calls = %+v", activation.activateCalls)
	}
}

func TestWebMCPDirectInvokeSIGINTChildProcess(t *testing.T) {
	if os.Getenv("WEBMCP_DIRECT_SIGINT_CHILD") == "1" {
		runWebMCPDirectInvokeSIGINTChild(t)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestWebMCPDirectInvokeSIGINTChildProcess$", "-test.v=false")
	command.Env = append(os.Environ(), "WEBMCP_DIRECT_SIGINT_CHILD=1")
	stdout := &childProcessOutputBuffer{}
	stderr := newChildProcessStderrBuffer()
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start SIGINT child: %v", err)
	}
	childAlive := true
	defer func() {
		if childAlive {
			killSIGINTChild(t, command)
		}
	}()

	var firstValue string
	select {
	case firstValue = <-stderr.firstLine:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGINT child did not emit a dispatch receipt")
	}
	var receipt WebMCPDirectInvocationReceipt
	if err := json.Unmarshal([]byte(firstValue), &receipt); err != nil {
		t.Fatalf("decode child dispatch receipt: %v; stderr=%q", err, firstValue)
	}
	if receipt.InvocationID != "browser-child-1" || receipt.State != string(webmcp.InvocationDispatched) {
		t.Fatalf("child dispatch receipt = %+v", receipt)
	}

	signalAt := time.Now()
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send SIGINT to child: %v", err)
	}
	exitErr := command.Wait()
	childAlive = false
	if exitErr == nil || command.ProcessState.ExitCode() == 0 {
		t.Fatalf("SIGINT child exited successfully: err=%v exit=%d", exitErr, command.ProcessState.ExitCode())
	}
	if elapsed := time.Since(signalAt); elapsed > operations.InterruptReconciliationTimeout+time.Second {
		t.Fatalf("SIGINT child completion took %s, want <= %s", elapsed, operations.InterruptReconciliationTimeout+time.Second)
	}

	stderrValue := stderr.String()
	stdoutValue := stdout.String()
	envelope := decodeDirectEnvelope(t, stdoutValue)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationCanceled) || envelope.Error.Retryable {
		t.Fatalf("SIGINT child envelope = %+v", envelope)
	}
	if envelope.Error.Details["invocation_id"] != "browser-child-1" || envelope.Error.Details["cancel_source"] != "interrupt" || envelope.Error.Details["side_effect_unknown"] != true {
		t.Fatalf("SIGINT child cancellation details = %#v", envelope.Error.Details)
	}
	if strings.Contains(stdoutValue, "input_secret") || strings.Contains(stdoutValue, "page_output") || strings.Contains(stdoutValue, "credential") {
		t.Fatalf("SIGINT child output leaked sensitive data: %q", stdoutValue)
	}
	if strings.TrimSpace(strings.TrimPrefix(stderrValue, firstValue)) != "" {
		t.Fatalf("SIGINT child wrote unexpected stderr after receipt: %q", stderrValue)
	}
}

type childProcessOutputBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *childProcessOutputBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(value)
}

func (b *childProcessOutputBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

type childProcessStderrBuffer struct {
	childProcessOutputBuffer
	firstLine chan string
	notified  bool
}

func newChildProcessStderrBuffer() *childProcessStderrBuffer {
	return &childProcessStderrBuffer{firstLine: make(chan string, 1)}
}

func (b *childProcessStderrBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	written, err := b.data.Write(value)
	if !b.notified {
		if newline := bytes.IndexByte(b.data.Bytes(), '\n'); newline >= 0 {
			b.notified = true
			line := append([]byte(nil), b.data.Bytes()[:newline+1]...)
			b.mu.Unlock()
			b.firstLine <- string(line)
			return written, err
		}
	}
	b.mu.Unlock()
	return written, err
}

func runWebMCPDirectInvokeSIGINTChild(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, tool := directFixture()
	broker := &sigintChildBroker{directCommandBroker: &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		catalog:    webmcp.ToolCatalogSnapshot{Context: page, Generation: page.Generation, Tools: []webmcp.ToolDescriptor{tool}},
		invokeResult: webmcp.InvokeResult{
			InvocationID:        "broker-child-1",
			BrowserInvocationID: "browser-child-1",
			State:               webmcp.InvocationDispatched,
		},
	}}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	operations := NewWebMCPOperationsCommand(globalFlags, directFactory(broker))
	operations.SelectionStore = store
	root := &cobra.Command{Use: "webmcp", SilenceErrors: true, SilenceUsage: true}
	operations.AddCommands(root)
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	root.SetArgs([]string{"invoke", "--browser", string(candidate.ID), "--tab", string(target.ID), "--tool-ref", string(tool.Ref), "--input-json", `{"input_secret":"do-not-echo"}`, "--json"})
	if err := root.Execute(); err == nil {
		os.Exit(43)
	}
	if broker.cancelRequest.InvocationID != "broker-child-1" {
		os.Exit(44)
	}
	if broker.cancelContextErr != nil {
		os.Exit(45)
	}
	os.Exit(42)
}

func TestWebMCPDirectCancelWritesBoundedTerminalOutcome(t *testing.T) {
	tests := []struct {
		name    string
		message string
		details map[string]any
	}{
		{
			name:    `completed_anyway`,
			message: `the browser invocation completed despite the cancellation request`,
			details: map[string]any{
				`browser_id`:          `browser-a`,
				`target_id`:           `tab-a`,
				`invocation_id`:       `browser-invocation-9`,
				`phase`:               `cancel`,
				`cancel_phase`:        `cancel_dispatched`,
				`outcome`:             `completed_anyway`,
				`terminal_observed`:   true,
				`side_effect_unknown`: true,
				`terminal_event`:      `tool_responded`,
			},
		},
		{
			name:    `cancellation_unconfirmed`,
			message: `the browser did not provide a correlated terminal cancellation result`,
			details: map[string]any{
				`browser_id`:          `browser-a`,
				`target_id`:           `tab-a`,
				`invocation_id`:       `browser-invocation-9`,
				`phase`:               `cancel`,
				`cancel_phase`:        `cancel_dispatched`,
				`outcome`:             `cancellation_unconfirmed`,
				`terminal_observed`:   false,
				`side_effect_unknown`: true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configDir := writeDirectConfig(t, ``)
			store := NewFileWebMCPSelectionStore(configDir)
			page, target, candidate, _ := directFixture()
			if err := store.Save(WebMCPSelection{
				Version:    WebMCPSelectionVersion,
				EndpointID: string(candidate.ID),
				BrowserID:  string(candidate.ID),
				TargetID:   string(target.ID),
				Origin:     target.Origin,
			}); err != nil {
				t.Fatalf(`seed persisted selection: %v`, err)
			}
			base := &directCommandBroker{
				candidates: []webmcp.BrowserCandidate{candidate},
				targets:    []webmcp.Target{target},
				selected:   page,
			}
			broker := &directCancelCommandBroker{
				directCommandBroker: base,
				directCancelErr: webmcp.NewClassifiedError(
					webmcp.ErrorInvocationFailed,
					test.message,
					test.details,
				),
			}

			result := executeDirectCommand(t, configDir, store, directFactory(broker), `cancel`, `--invocation`, `browser-invocation-9`, `--json`)
			if result.err == nil {
				t.Fatal(`bounded terminal failure unexpectedly succeeded`)
			}
			envelope := decodeDirectEnvelope(t, result.stdout)
			if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationFailed) {
				t.Fatalf(`bounded terminal failure envelope = %+v`, envelope)
			}
			if envelope.Error.Message != test.message {
				t.Fatalf(`bounded terminal failure message = %q, want %q`, envelope.Error.Message, test.message)
			}
			if envelope.Error.Details[`outcome`] != test.details[`outcome`] ||
				envelope.Error.Details[`terminal_observed`] != test.details[`terminal_observed`] ||
				envelope.Error.Details[`side_effect_unknown`] != true {
				t.Fatalf(`bounded terminal failure details = %#v`, envelope.Error.Details)
			}
			if strings.Contains(result.stdout, `page-output`) || strings.Contains(result.stdout, `page-error-output`) {
				t.Fatalf(`bounded terminal failure exposed page output: %q`, result.stdout)
			}
		})
	}
}

func TestWebMCPDirectRetainedSelectionDistinguishesFreshReplacementFromEndpointLoss(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, oldCandidate, _ := directFixture()
	oldCandidate.BrowserInstanceID = randomizedWebMCPInstanceID(t)
	selected := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{oldCandidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	seed := executeDirectCommand(t, configDir, store, directFactory(selected), "select", "--browser", string(oldCandidate.ID), "--tab", string(target.ID), "--json")
	if seed.err != nil {
		t.Fatalf("seed persisted selection: %v\nstdout=%s", seed.err, seed.stdout)
	}
	oldRecord, err := store.Load()
	if err != nil {
		t.Fatalf("load old selection: %v", err)
	}
	if oldRecord.BrowserInstanceID != oldCandidate.BrowserInstanceID || oldRecord.Generation != page.Generation {
		t.Fatalf("persisted identity = %+v, want instance=%q generation=%d", oldRecord, oldCandidate.BrowserInstanceID, page.Generation)
	}

	replacement := oldCandidate
	replacement.ID = webmcp.BrowserID(randomizedWebMCPTestID(t, "browser-"))
	replacement.BrowserInstanceID = randomizedWebMCPInstanceID(t)
	replacementTarget := target
	replacementTarget.BrowserID = replacement.ID
	replacementBroker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{replacement},
		targets:    []webmcp.Target{replacementTarget},
	}
	replaced := executeDirectCommand(t, configDir, store, directFactory(replacementBroker), "context", "--json")
	if replaced.err == nil {
		t.Fatal("context unexpectedly selected a reachable fresh browser replacement")
	}
	replacedEnvelope := decodeDirectEnvelope(t, replaced.stdout)
	if replacedEnvelope.OK || replacedEnvelope.Error == nil || replacedEnvelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("replacement envelope = %+v", replacedEnvelope)
	}
	if details := replacedEnvelope.Error.Details; details["browser_id"] != oldRecord.BrowserID || details["target_id"] != oldRecord.TargetID || details["selected_generation"] != float64(oldRecord.Generation) || details["reason"] != "browser_instance_changed" {
		t.Fatalf("replacement details = %#v", details)
	}
	if len(replacementBroker.selectCalls) != 0 || len(replacementBroker.activateCalls) != 0 {
		t.Fatalf("replacement received selection work: select=%+v activate=%+v", replacementBroker.selectCalls, replacementBroker.activateCalls)
	}
	if oldAfterReplacement, loadErr := store.Load(); loadErr != nil || oldAfterReplacement != oldRecord {
		t.Fatalf("replacement changed persisted selection: before=%+v after=%+v err=%v", oldRecord, oldAfterReplacement, loadErr)
	}

	lostBroker := &directCommandBroker{
		discoverErr: webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, "browser endpoint could not be reached", map[string]any{
			"phase": "discovery",
		}),
	}
	lost := executeDirectCommand(t, configDir, store, directFactory(lostBroker), "context", "--json")
	if lost.err == nil {
		t.Fatal("context unexpectedly succeeded after endpoint loss")
	}
	lostEnvelope := decodeDirectEnvelope(t, lost.stdout)
	if lostEnvelope.OK || lostEnvelope.Error == nil || lostEnvelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("lost endpoint envelope = %+v", lostEnvelope)
	}
	if details := lostEnvelope.Error.Details; details["browser_id"] != oldRecord.BrowserID || details["target_id"] != oldRecord.TargetID || details["phase"] != "discovery" || details["reconnect_required"] != true {
		t.Fatalf("lost endpoint details = %#v", details)
	}
}

func TestWebMCPDirectBrowserAndTabListingsRemainBoundedOnChurn(t *testing.T) {
	t.Run("browsers discovery timeout", func(t *testing.T) {
		configDir := writeDirectConfig(t, "")
		broker := &blockingDirectDiscoveryBroker{directCommandBroker: &directCommandBroker{}}
		started := time.Now()
		result := executeDirectCommand(t, configDir, nil, directFactory(broker), "browsers", "--command-timeout", "40ms", "--json")
		if result.err == nil {
			t.Fatal("browsers unexpectedly succeeded after discovery timeout")
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("browsers discovery took %s after its 40ms deadline", elapsed)
		}
		envelope := decodeDirectEnvelope(t, result.stdout)
		if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationTimedOut) {
			t.Fatalf("browsers timeout envelope = %+v", envelope)
		}
	})

	t.Run("tabs browser disconnect", func(t *testing.T) {
		configDir := writeDirectConfig(t, "")
		_, target, candidate, _ := directFixture()
		runtime := testkit.NewScriptedBrowserRuntime(testkit.BrowserConfig{
			Candidate: candidate,
			Targets:   []testkit.TargetConfig{testkit.NewTargetConfig(target)},
		})
		defer closeForTest(t, runtime.Close)
		handle := runtime.Browser(candidate.ID)
		if handle == nil {
			t.Fatal("scripted browser handle is nil")
		}
		handle.BlockOpen()
		broker := webmcp.NewBroker(webmcp.BrokerOptions{
			Runtime:    runtime,
			Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
		})
		resultDone := make(chan directCommandResult, 1)
		started := time.Now()
		go func() {
			resultDone <- executeDirectCommand(t, configDir, nil, directFactory(broker),
				"tabs", "--browser", string(candidate.ID), "--command-timeout", "250ms", "--json")
		}()
		waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
		_, waitErr := runtime.WaitForOperationAdmitted(waitCtx, testkit.OperationOpen)
		cancelWait()
		if waitErr != nil {
			t.Fatalf("wait for tabs open admission: %v", waitErr)
		}
		if err := runtime.Disconnect(candidate.ID, "transport_lost"); err != nil {
			var classified *webmcp.ClassifiedError
			if !errors.As(err, &classified) || classified.Code != webmcp.ErrorBrowserDisconnected {
				t.Fatalf("disconnect: %v", err)
			}
		}
		var result directCommandResult
		select {
		case result = <-resultDone:
		case <-time.After(2 * time.Second):
			t.Fatal("tabs remained blocked after browser disconnect")
		}
		if result.err == nil {
			t.Fatalf("tabs unexpectedly succeeded after browser death: %s", result.stdout)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("tabs took %s after browser death", elapsed)
		}
		envelope := decodeDirectEnvelope(t, result.stdout)
		if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
			t.Fatalf("tabs disconnect envelope = %+v", envelope)
		}
		if envelope.Error.Details["browser_id"] != string(candidate.ID) {
			t.Fatalf("tabs disconnect browser_id = %#v", envelope.Error.Details["browser_id"])
		}
	})
}

func TestWebMCPDirectMalformedInputReturnsSelectedSchema(t *testing.T) {
	schema := `{"type":"object","properties":{"profile":{"type":"object","properties":{"count":{"type":"integer","minimum":1},"mode":{"enum":["fast","safe"]}},"required":["count"],"additionalProperties":false},"tags":{"type":"array","items":{"type":"string"}}},"required":["profile","tags"],"additionalProperties":false}`
	const toolRef = webmcp.ToolRef("webmcp.tool-ref.v1:AAAAAAAAAAAAAAAAAAAAAA")
	wantGolden, err := os.ReadFile(filepath.Join("testdata", "webmcp-invoke-invalid-input.golden.json"))
	if err != nil {
		t.Fatalf("read malformed-input golden: %v", err)
	}

	for _, testCase := range []struct {
		name       string
		positional bool
	}{
		{name: "exact ref"},
		{name: "unique positional name", positional: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			configDir := writeDirectConfig(t, "")
			store := NewFileWebMCPSelectionStore(configDir)
			_, target, candidate, tool := directFixture()
			tool.InputSchema = json.RawMessage(schema)
			runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
				testkit.NewTargetConfig(target, testkit.WithInitialCatalog(tool)),
			))
			broker := webmcp.NewBroker(webmcp.BrokerOptions{
				Runtime:    runtime,
				Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
				ToolRefFactory: func(webmcp.ToolDescriptor) (webmcp.ToolRef, error) {
					return toolRef, nil
				},
			})

			args := []string{"invoke", "--browser", string(candidate.ID), "--tab", string(target.ID)}
			if testCase.positional {
				args = append(args, "read_state")
			} else {
				args = append(args, "--tool-ref", string(toolRef))
			}
			args = append(args, "--input-json", `{"profile":{"mode":"fast","secret":"do-not-echo"}`, "--json")

			result := executeDirectCommand(t, configDir, store, directFactory(broker), args...)
			if result.err == nil {
				t.Fatal("malformed invocation unexpectedly succeeded")
			}
			if got := strings.TrimSpace(result.stdout); got != strings.TrimSpace(string(wantGolden)) {
				t.Fatalf("malformed-input envelope = %s, want golden %s", got, strings.TrimSpace(string(wantGolden)))
			}
			if strings.Contains(result.stdout, "do-not-echo") {
				t.Fatalf("malformed input leaked into result: %s", result.stdout)
			}
			envelope := decodeDirectEnvelope(t, result.stdout)
			if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvalidToolInput) || !envelope.Error.Retryable {
				t.Fatalf("malformed-input envelope = %+v", envelope)
			}
			operations := runtime.Operations()
			if hasTestkitOperation(operations, testkit.OperationInvoke) {
				t.Fatalf("malformed input was dispatched: %+v", operations)
			}
		})
	}
}

func TestWebMCPDirectFailedSelectionPreservesPriorSelection(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	_, target, candidate, _ := directFixture()
	prior := WebMCPSelection{
		Version:          WebMCPSelectionVersion,
		EndpointID:       string(candidate.ID),
		BrowserID:        string(candidate.ID),
		TargetID:         string(target.ID),
		Origin:           target.Origin,
		ContinuityMarker: "prior-selection",
		Generation:       3,
		SelectedAt:       time.Unix(3, 0).UTC(),
	}
	if err := store.Save(prior); err != nil {
		t.Fatalf("save prior selection: %v", err)
	}

	page, _, _, _ := directFixture()
	runtime := testkit.NewScriptedBrowserRuntime(testkit.BrowserConfig{
		Candidate: candidate,
		Targets:   []testkit.TargetConfig{testkit.NewTargetConfig(target, testkit.WithContext(page), testkit.WithBlockedEnable())},
	})
	defer closeForTest(t, runtime.Close)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
	})

	resultDone := make(chan directCommandResult, 1)
	go func() {
		resultDone <- executeDirectCommand(t, configDir, store, directFactory(broker),
			"select", "--browser", string(candidate.ID), "--tab", string(target.ID),
			"--persist-selection", "--command-timeout", "250ms", "--json")
	}()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if _, err := runtime.WaitForOperationAdmitted(waitCtx, testkit.OperationEnableWebMCP); err != nil {
		t.Fatalf("wait for enable admission: %v", err)
	}
	requireFixtureStep(t, "disconnect scripted browser", runtime.Disconnect(candidate.ID, "transport_lost"))
	resultCtx, cancelResult := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelResult()
	var result directCommandResult
	select {
	case result = <-resultDone:
	case <-resultCtx.Done():
		t.Fatalf("failed selection remained blocked: %v", resultCtx.Err())
	}
	if result.err == nil {
		t.Fatalf("failed selection unexpectedly succeeded: %s", result.stdout)
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("failed selection envelope = %+v", envelope)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("load preserved selection: %v", err)
	}
	if !reflect.DeepEqual(got, prior) {
		t.Fatalf("failed selection changed persisted state: got=%+v want=%+v", got, prior)
	}

	followUpBroker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
	})
	followUp := executeDirectCommand(t, configDir, store, directFactory(followUpBroker), "context", "--json")
	if followUp.err == nil {
		t.Fatal("follow-up context unexpectedly succeeded after browser loss")
	}
	followUpEnvelope := decodeDirectEnvelope(t, followUp.stdout)
	if followUpEnvelope.OK || followUpEnvelope.Error == nil || followUpEnvelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("follow-up context envelope = %+v", followUpEnvelope)
	}
}

func TestWebMCPDirectCommandDeadlineRemainsBoundedTimeoutClass(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	page, target, candidate, _ := directFixture()
	broker := &blockingDirectSelectBroker{
		directCommandBroker: &directCommandBroker{
			candidates: []webmcp.BrowserCandidate{candidate},
			targets:    []webmcp.Target{target},
			selected:   page,
		},
	}
	started := time.Now()
	result := executeDirectCommand(t, configDir, NewFileWebMCPSelectionStore(configDir), directFactory(broker),
		"select", "--browser", string(candidate.ID), "--tab", string(target.ID), "--command-timeout", "40ms", "--json")
	if result.err == nil {
		t.Fatal("deadline-bound select unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("deadline-bound select took %s", elapsed)
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationTimedOut) {
		t.Fatalf("deadline-bound select envelope = %+v", envelope)
	}
	if strings.Contains(result.stdout, string(webmcp.ErrorBrowserDisconnected)) {
		t.Fatalf("genuine deadline was classified as browser loss: %s", result.stdout)
	}
}

type blockingDirectSelectBroker struct {
	*directCommandBroker
	started chan struct{}
}

type blockingDirectDiscoveryBroker struct {
	*directCommandBroker
}

func (b *blockingDirectDiscoveryBroker) Discover(ctx context.Context, _ webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingDirectSelectBroker) SelectWithOptions(ctx context.Context, _ webmcp.TargetSelector, _ webmcp.SelectOptions) (webmcp.PageContext, error) {
	if b.started == nil {
		b.started = make(chan struct{})
	}
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	<-ctx.Done()
	return webmcp.PageContext{}, ctx.Err()
}

type directCancelCommandBroker struct {
	*directCommandBroker

	directCancelRequest webmcp.DirectCancelRequest
	directCancelErr     error
}

type sigintChildBroker struct {
	*directCommandBroker
	cancelContextErr error
}

func (b *sigintChildBroker) WaitInvocation(ctx context.Context, _ webmcp.InvocationID) (webmcp.InvokeResult, error) {
	<-ctx.Done()
	return webmcp.InvokeResult{}, ctx.Err()
}

func (b *sigintChildBroker) Cancel(ctx context.Context, request webmcp.CancelRequest) error {
	b.cancelContextErr = ctx.Err()
	return b.directCommandBroker.Cancel(ctx, request)
}

func (b *directCancelCommandBroker) CancelDirect(_ context.Context, request webmcp.DirectCancelRequest) error {
	b.directCancelRequest = request
	return b.directCancelErr
}

// browserDeathCase blocks one select stage and then kills the browser.
type browserDeathCase struct {
	name        string
	operation   testkit.OperationKind
	phase       string
	targetKnown bool
	activate    bool
	block       func(*testkit.ScriptedBrowserHandle)
	blockEnable bool
}

func TestWebMCPDirectSelectBrowserDeathAtEveryStage(t *testing.T) {
	tests := []browserDeathCase{
		{name: "discovery_dial", operation: testkit.OperationOpen, phase: "open", block: func(handle *testkit.ScriptedBrowserHandle) { handle.BlockOpen() }},
		{name: "target_resolution", operation: testkit.OperationListTargets, phase: "list_targets", block: func(handle *testkit.ScriptedBrowserHandle) { handle.BlockListTargets() }},
		{name: "attach", operation: testkit.OperationAttach, phase: "attach", targetKnown: true, block: func(handle *testkit.ScriptedBrowserHandle) { handle.BlockAttach() }},
		{name: "activation", operation: testkit.OperationActivate, phase: "activate", targetKnown: true, activate: true, block: func(handle *testkit.ScriptedBrowserHandle) { handle.BlockActivate() }},
		{name: "enable_acknowledgement", operation: testkit.OperationEnableWebMCP, phase: "enable_webmcp", targetKnown: true, blockEnable: true},
		{name: "catalog_ready", operation: testkit.OperationEnableAcknowledged, phase: "catalog", targetKnown: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			forEachDirectOutputMode(t, func(t *testing.T, jsonMode bool) {
				runSelectBrowserDeathCase(t, testCase, jsonMode)
			})
		})
	}
}

func newBrowserDeathRuntime(t *testing.T, testCase browserDeathCase) *testkit.ScriptedBrowserRuntime {
	t.Helper()
	page, target, candidate, tool := directFixture()
	sessionOptions := []testkit.ScriptedTargetSessionOption{testkit.WithContext(page)}
	if testCase.blockEnable {
		sessionOptions = append(sessionOptions, testkit.WithBlockedEnable())
	}
	if testCase.activate {
		sessionOptions = append(sessionOptions, testkit.WithInitialCatalog(tool))
	}
	runtime := testkit.NewScriptedBrowserRuntime(testkit.BrowserConfig{
		Candidate: candidate,
		Targets:   []testkit.TargetConfig{testkit.NewTargetConfig(target, sessionOptions...)},
	})
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Logf("close scripted runtime: %v", err)
		}
	})
	handle := runtime.Browser(candidate.ID)
	if handle == nil {
		t.Fatal("scripted browser handle is nil")
	}
	if testCase.block != nil {
		testCase.block(handle)
	}
	return runtime
}

func runSelectBrowserDeathCase(t *testing.T, testCase browserDeathCase, jsonMode bool) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	_, target, candidate, _ := directFixture()
	runtime := newBrowserDeathRuntime(t, testCase)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
	})
	args := []string{"select", "--browser", string(candidate.ID), "--tab", string(target.ID), "--command-timeout", "250ms"}
	if testCase.activate {
		args = append(args, "--activate")
	}
	started := time.Now()
	resultDone := make(chan directCommandResult, 1)
	go func() {
		resultDone <- executeDirectCommand(t, configDir, store, directFactory(broker), withDirectOutputMode(args, jsonMode)...)
	}()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	_, err := runtime.WaitForOperationAdmitted(waitCtx, testCase.operation)
	cancelWait()
	if err != nil {
		t.Fatalf("wait for %s admission: %v", testCase.operation, err)
	}
	if err := runtime.Disconnect(candidate.ID, "transport_lost"); err != nil {
		t.Logf("disconnect scripted browser: %v", err)
	}
	result := awaitDirectResult(t, resultDone, testCase.name)
	if result.err == nil {
		t.Fatalf("select succeeded after browser death: stdout=%s", result.stdout)
	}
	if elapsed := time.Since(started); elapsed > DefaultWebMCPDirectCommandTimeout {
		t.Fatalf("select took %s after browser death", elapsed)
	}
	if jsonMode {
		requireBrowserDeathJSON(t, result, testCase, candidate, target)
	} else {
		requireOutputContains(t, result.stdout, "Error: browser_disconnected")
	}
	runtimeOperations := runtime.Operations()
	if hasTestkitOperation(runtimeOperations, testkit.OperationCloseTarget) {
		t.Fatalf("browser death caused an external target close: %+v", runtimeOperations)
	}
	if countTestkitOperations(runtimeOperations, testkit.OperationDetach) > 1 {
		t.Fatalf("browser death caused duplicate detach: %+v", runtimeOperations)
	}
}

func awaitDirectResult(t *testing.T, resultDone <-chan directCommandResult, name string) directCommandResult {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case result := <-resultDone:
		return result
	case <-timer.C:
		t.Fatalf("select remained blocked after %s", name)
		return directCommandResult{}
	}
}

func requireBrowserDeathJSON(t *testing.T, result directCommandResult, testCase browserDeathCase, candidate webmcp.BrowserCandidate, target webmcp.Target) {
	t.Helper()
	details := requireDirectErrorCode(t, result, webmcp.ErrorBrowserDisconnected).Details
	if got := details["browser_id"]; got != string(candidate.ID) {
		t.Errorf("browser_id = %#v, want %q", got, candidate.ID)
	}
	if got := details["target_id"]; testCase.targetKnown && got != string(target.ID) {
		t.Errorf("target_id = %#v, want %q", got, target.ID)
	}
	if got := details["phase"]; got != testCase.phase {
		t.Errorf("phase = %#v, want %q", got, testCase.phase)
	}
	if got := details["reconnect_required"]; got != true {
		t.Errorf("reconnect_required = %#v, want true", got)
	}
}

func killSIGINTChild(t *testing.T, command *exec.Cmd) {
	t.Helper()
	if err := command.Process.Kill(); err != nil {
		t.Logf("kill SIGINT child: %v", err)
	}
}
