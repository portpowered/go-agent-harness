//go:build live

package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	cdpTarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	cubecadeLiveEnv      = "WEBMCP_CUBECADE_LIVE"
	cubecadeArtifactEnv  = "WEBMCP_CUBECADE_ARTIFACT_DIR"
	cubecadeURL          = "https://cubecade.openai.chatgpt.site/"
	cubecadeOrigin       = "https://cubecade.openai.chatgpt.site"
	cubecadeModel        = "gpt-realtime-2.1-mini"
	cubecadeMaxDuration  = 30 * time.Second
	cubecadeLaunchDelay  = 4 * time.Second
	cubecadeRunGrace     = 20 * time.Second
	cubecadeArtifactMode = 0o700
	cubecadeEvidenceMode = 0o600
)

// TestPinnedChromeCubecadeProductionSessionRecoversLateCatalog is the
// release-facing production-session proof. It is deliberately credentialed
// and opt-in: ordinary test runs never inspect credentials, download Chrome,
// contact the remote page, or call the provider.
func TestPinnedChromeCubecadeProductionSessionRecoversLateCatalog(t *testing.T) {
	if os.Getenv(cubecadeLiveEnv) != "1" {
		t.Skipf("set %s=1 to run the credentialed Cubecade production-session proof", cubecadeLiveEnv)
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skipf("Cubecade proof uses the qualified %s Chrome lock; observed %s/%s", lockedChromePlatform, runtime.GOOS, runtime.GOARCH)
	}

	apiKey, keySource, err := loadGateI2APIKey()
	if errors.Is(err, errGateI2MissingAPIKey) {
		t.Skip("OPENAI_API_KEY or OPENAI_API_KEY_FILE is not set; skipping the credentialed Cubecade proof")
	}
	if err != nil {
		t.Fatalf("load OpenAI API key: %v", err)
	}

	artifactRoot := cubecadeArtifactRoot(t)
	configDir := filepath.Join(artifactRoot, "config")
	if err := os.MkdirAll(configDir, cubecadeArtifactMode); err != nil {
		t.Fatalf("create Cubecade config directory: %v", err)
	}
	systemPromptPath := filepath.Join(artifactRoot, "system-prompt.txt")
	if err := os.WriteFile(systemPromptPath, []byte(cubecadeSystemPrompt), cubecadeEvidenceMode); err != nil {
		t.Fatalf("write Cubecade system prompt: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	workDir := filepath.Join(artifactRoot, "chrome")
	if err := os.MkdirAll(workDir, cubecadeArtifactMode); err != nil {
		t.Fatalf("create Cubecade Chrome directory: %v", err)
	}
	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire qualified Chrome for Testing: %v", err)
	}
	binaryPath := filepath.Join(artifactRoot, "agent")
	if err := buildGateBinary(ctx, root, binaryPath); err != nil {
		t.Fatalf("build production agent CLI: %v", err)
	}

	launchStartedAt := time.Now()
	browser, err := launchPinnedChrome(ctx, pinned, cubecadeURL)
	if err != nil {
		t.Fatalf("launch qualified Chrome for Testing: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if closeErr := browser.Close(); closeErr != nil {
				t.Logf("Cubecade Chrome cleanup: %v", closeErr)
			}
		}
	})

	devToolsReadyAt := time.Now()
	baseURL := browserHTTPURL(browser.endpoint())
	cdpURL := strings.TrimRight(baseURL, "/") + "/json/version"
	if err := writeCubecadeConfig(configDir, cdpURL); err != nil {
		t.Fatalf("write Cubecade browser config: %v", err)
	}
	if err := waitUntilCubecade(ctx, launchStartedAt.Add(cubecadeLaunchDelay)); err != nil {
		t.Fatalf("wait for four-second Chrome launch offset: %v", err)
	}

	capturePath := filepath.Join(artifactRoot, "provider.json")
	recordDir := filepath.Join(artifactRoot, "recording")
	sessionArgs := []string{
		"session",
		"--provider", "openai",
		"--model", cubecadeModel,
		"--browser-tools", "webmcp",
		"--browser-cdp-url", cdpURL,
		"--browser-auto-select", "single",
		"--browser-origin", cubecadeOrigin,
		"--browser-approval", "never",
		"--browser-record", "true",
		"--browser-record-arguments", "true",
		"--browser-record-results", "true",
		"--record", capturePath,
		"--record-dir", recordDir,
		"--system-prompt", systemPromptPath,
		"--prompt", "Scramble the cube and then solve it. Confirm in five words or fewer.",
		"--max-duration", cubecadeMaxDuration.String(),
	}
	sessionStartedAt := time.Now()
	runContext, cancelRun := context.WithTimeout(ctx, cubecadeMaxDuration+cubecadeRunGrace)
	process, err := startGateCommandWithEnvironment(runContext, binaryPath, configDir, []string{"AGENT_MODEL__OPENAI__API_KEY=" + apiKey}, sessionArgs...)
	if err != nil {
		cancelRun()
		t.Fatalf("start production agent CLI: %v", err)
	}
	sessionResult, waitErr := process.wait(runContext)
	cancelRun()
	var runErr error
	if waitErr != nil {
		runErr = waitErr
	} else if sessionResult.Err != nil || sessionResult.ExitCode != 0 {
		runErr = fmt.Errorf("agent session exit=%d err=%v", sessionResult.ExitCode, sessionResult.Err)
	}

	observationContext, cancelObservation := context.WithTimeout(ctx, 20*time.Second)
	defer cancelObservation()
	version, versionErr := readDevToolsVersion(observationContext, baseURL)
	rawTarget, targetErr := waitForCubecadePageTarget(observationContext, baseURL)
	var oracle cubecadePageOracle
	var oracleErr error
	if targetErr == nil {
		oracle, oracleErr = inspectCubecadeTarget(observationContext, browser.endpoint(), rawTarget.ID)
	}
	capture, captureErr := gwtesting.LoadSessionCapture(capturePath)
	var observation cubecadeCaptureObservation
	var observationErr error
	if captureErr == nil {
		observation, observationErr = inspectCubecadeCapture(capture)
	}

	var expectedBrowserID, expectedTargetID string
	if versionErr == nil && targetErr == nil {
		expectedBrowserID, expectedTargetID, err = gateI2PublicIDs(version.WebSocketDebuggerURL, rawTarget.ID)
		if err != nil {
			versionErr = fmt.Errorf("derive public browser/target IDs: %w", err)
		}
	}
	validation := cubecadeValidation{}
	validationErr := runErr
	if validationErr == nil && versionErr != nil {
		validationErr = fmt.Errorf("Chrome post-session version check: %w", versionErr)
	}
	if validationErr == nil && targetErr != nil {
		validationErr = fmt.Errorf("Chrome post-session target check: %w", targetErr)
	}
	if validationErr == nil && captureErr != nil {
		validationErr = fmt.Errorf("load provider capture: %w", captureErr)
	}
	if validationErr == nil && observationErr != nil {
		validationErr = fmt.Errorf("inspect provider capture: %w", observationErr)
	}
	if validationErr == nil {
		validation, validationErr = validateCubecadeCapture(observation, expectedBrowserID, expectedTargetID)
	}
	if validationErr == nil && oracleErr != nil {
		validationErr = fmt.Errorf("inspect independent Cubecade DOM oracle: %w", oracleErr)
	}
	if validationErr == nil {
		validationErr = validateCubecadeOracle(oracle)
	}
	validation.QueueInvocations = strings.Count(oracle.Terminal, "$ queue_cube_moves [6]")

	evidence := cubecadeEvidence{
		Schema:                "webmcp.cubecade.production.evidence.v1",
		ObservedAtUTC:         time.Now().UTC().Format(time.RFC3339Nano),
		ChromeChannel:         pinned.Lock.Channel,
		ChromePlatform:        pinned.Lock.Platform,
		ChromeVersion:         pinned.Lock.Version,
		ChromeRevision:        pinned.Lock.Revision,
		ChromeFlags:           cubecadeEvidenceFlags(pinned, cubecadeURL),
		Provider:              observation.Provider,
		Model:                 observation.Model,
		APIKeySource:          keySource,
		Command:               cubecadeEvidenceCommand(sessionArgs),
		ChromeLaunchAtUTC:     launchStartedAt.UTC().Format(time.RFC3339Nano),
		DevToolsReadyAtUTC:    devToolsReadyAt.UTC().Format(time.RFC3339Nano),
		SessionStartedAtUTC:   sessionStartedAt.UTC().Format(time.RFC3339Nano),
		SessionStartDelayMS:   sessionStartedAt.Sub(launchStartedAt).Milliseconds(),
		FirstListCallMS:       validation.FirstListCallMS,
		FirstListResultMS:     validation.FirstListResultMS,
		RecoveredListResultMS: validation.RecoveredListResultMS,
		RetryableListErrors:   validation.RetryableListErrors,
		SuccessfulListCalls:   validation.SuccessfulListCalls,
		BrowserID:             validation.BrowserID,
		TargetID:              validation.TargetID,
		PageTools:             validation.PageTools,
		BrokerInvocations:     validation.BrokerInvocations,
		QueueInvocations:      validation.QueueInvocations,
		Solved:                oracle.Solved,
		DOMStatus:             oracle.Status,
		DOMTerminal:           oracle.Terminal,
		CapturePath:           "<artifact>/provider.json",
		RecordDir:             "<artifact>/recording",
		Pass:                  validationErr == nil,
	}
	if validationErr != nil {
		evidence.ValidationError = validationErr.Error()
	}
	evidencePath := filepath.Join(artifactRoot, "evidence.json")
	if err := writeCubecadeEvidence(evidencePath, evidence); err != nil {
		t.Logf("write Cubecade evidence: %v", err)
	}

	closeErr := browser.Close()
	closed = true
	if closeErr != nil {
		t.Logf("Cubecade Chrome cleanup returned: %v", closeErr)
	}
	if validationErr != nil {
		t.Fatalf("Cubecade production late-catalog proof failed: %v; evidence=%s capture=%s", validationErr, evidencePath, capturePath)
	}
	t.Logf("WEBMCP_CUBECADE_PASS chrome=%s revision=%s browser=%s target=%s retryable_list_errors=%d successful_list_calls=%d first_list_ms=%d recovered_list_ms=%d broker_invocations=%d queue_invocations=%d capture=%s evidence=%s", pinned.Lock.Version, pinned.Lock.Revision, validation.BrowserID, validation.TargetID, validation.RetryableListErrors, validation.SuccessfulListCalls, validation.FirstListCallMS, validation.RecoveredListResultMS, validation.BrokerInvocations, validation.QueueInvocations, capturePath, evidencePath)
}

const cubecadeSystemPrompt = `You are the production WebMCP late-catalog verification operator. Use only the already selected page; never attach, reconnect, select another tab, or use a hidden shortcut.

Follow this exact protocol:
- Immediately call webmcp_list_tools before any other browser tool. If it returns browser_protocol_invalid with retryable=true, details.reason_code=page_tools_unverified, and details.reason=deadline_exceeded, wait briefly and call webmcp_list_tools again. Do not reattach or select again.
- After a successful list, call webmcp_get_context once and retain its browser_id and target_id. The identity must remain unchanged.
- Find get_cube_state and queue_cube_moves in the page catalog. Use webmcp_invoke with the exact opaque refs returned by webmcp_list_tools.
- Call queue_cube_moves exactly twice. First use the six-move scramble ["R","U","F","L'","D","B2"]. Then, after get_cube_state shows the queue is empty, use the inverse ["B2","D'","L","F'","U'","R'"] exactly once.
- Call get_cube_state as needed to wait for the queue to empty and then confirm solved=true. Do not invoke either page tool more than needed and never duplicate a queue request.
- After the independent page state is solved, reply with five words or fewer.`

type cubecadeCaptureObservation struct {
	Provider        string
	Model           string
	SessionUpdates  int
	AdvertisedTools []string
	Calls           []cubecadeCall
	Outputs         []cubecadeOutput
}

type cubecadeCall struct {
	Index          int
	TimestampMS    int64
	ArgumentsIndex int
	Name           string
	CallID         string
	Arguments      string
}

type cubecadeOutput struct {
	Index       int
	TimestampMS int64
	CallID      string
	Envelope    webmcp.ToolResultEnvelope
}

type cubecadeValidation struct {
	BrowserID             string
	TargetID              string
	PageTools             []string
	FirstListCallMS       int64
	FirstListResultMS     int64
	RecoveredListResultMS int64
	RetryableListErrors   int
	SuccessfulListCalls   int
	BrokerInvocations     int
	QueueInvocations      int
}

type cubecadePageOracle struct {
	URL                   string `json:"url"`
	ModelContextAvailable bool   `json:"modelContextAvailable"`
	Solved                bool   `json:"solved"`
	Status                string `json:"status"`
	Terminal              string `json:"terminal"`
	State                 string `json:"state"`
}

type cubecadeEvidence struct {
	Schema                string   `json:"schema"`
	ObservedAtUTC         string   `json:"observed_at_utc"`
	ChromeChannel         string   `json:"chrome_channel"`
	ChromePlatform        string   `json:"chrome_platform"`
	ChromeVersion         string   `json:"chrome_version"`
	ChromeRevision        string   `json:"chrome_revision"`
	ChromeFlags           []string `json:"chrome_flags"`
	Provider              string   `json:"provider"`
	Model                 string   `json:"model"`
	APIKeySource          string   `json:"api_key_source"`
	Command               string   `json:"command"`
	ChromeLaunchAtUTC     string   `json:"chrome_launch_at_utc"`
	DevToolsReadyAtUTC    string   `json:"devtools_ready_at_utc"`
	SessionStartedAtUTC   string   `json:"session_started_at_utc"`
	SessionStartDelayMS   int64    `json:"session_start_delay_ms"`
	FirstListCallMS       int64    `json:"first_list_call_ms"`
	FirstListResultMS     int64    `json:"first_list_result_ms"`
	RecoveredListResultMS int64    `json:"recovered_list_result_ms"`
	RetryableListErrors   int      `json:"retryable_list_errors"`
	SuccessfulListCalls   int      `json:"successful_list_calls"`
	BrokerInvocations     int      `json:"broker_invocations"`
	BrowserID             string   `json:"browser_id"`
	TargetID              string   `json:"target_id"`
	PageTools             []string `json:"page_tools"`
	QueueInvocations      int      `json:"queue_invocations"`
	Solved                bool     `json:"solved"`
	DOMStatus             string   `json:"dom_status"`
	DOMTerminal           string   `json:"dom_terminal"`
	CapturePath           string   `json:"capture_path"`
	RecordDir             string   `json:"record_dir"`
	Pass                  bool     `json:"pass"`
	ValidationError       string   `json:"validation_error,omitempty"`
}

func inspectCubecadeCapture(capture gwtesting.SessionCapture) (cubecadeCaptureObservation, error) {
	observation := cubecadeCaptureObservation{
		Provider: capture.Provider.Name,
		Model:    capture.Provider.Model,
	}
	for index, record := range capture.Records {
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		if len(payload) == 0 {
			return observation, fmt.Errorf("record %d (%s) has an empty payload", index, record.Type)
		}
		if record.Direction == gwtesting.DirectionClientToServer {
			switch record.Type {
			case "session.update":
				observation.SessionUpdates++
				var event struct {
					Session struct {
						Tools []struct {
							Name string `json:"name"`
						} `json:"tools"`
					} `json:"session"`
				}
				if err := json.Unmarshal(payload, &event); err != nil {
					return observation, fmt.Errorf("decode session.update: %w", err)
				}
				observation.AdvertisedTools = observation.AdvertisedTools[:0]
				for _, tool := range event.Session.Tools {
					observation.AdvertisedTools = append(observation.AdvertisedTools, tool.Name)
				}
			case "conversation.item.create":
				var event struct {
					Item struct {
						Type   string `json:"type"`
						CallID string `json:"call_id"`
						Output string `json:"output"`
					} `json:"item"`
				}
				if err := json.Unmarshal(payload, &event); err != nil {
					return observation, fmt.Errorf("decode function_call_output: %w", err)
				}
				if event.Item.Type != "function_call_output" {
					continue
				}
				envelope, err := webmcp.UnmarshalToolResult([]byte(event.Item.Output))
				if err != nil {
					return observation, fmt.Errorf("decode tool result for call %q: %w", event.Item.CallID, err)
				}
				observation.Outputs = append(observation.Outputs, cubecadeOutput{
					Index:       index,
					TimestampMS: record.TimestampMs,
					CallID:      event.Item.CallID,
					Envelope:    envelope,
				})
			}
			continue
		}
		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.output_item.added":
			var event struct {
				Item struct {
					Type   string `json:"type"`
					Name   string `json:"name"`
					CallID string `json:"call_id"`
				} `json:"item"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode response.output_item.added: %w", err)
			}
			if event.Item.Type == "function_call" {
				observation.Calls = append(observation.Calls, cubecadeCall{Index: index, TimestampMS: record.TimestampMs, ArgumentsIndex: -1, Name: event.Item.Name, CallID: event.Item.CallID})
			}
		case "response.function_call_arguments.done":
			var event struct {
				Name      string `json:"name"`
				CallID    string `json:"call_id"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode response.function_call_arguments.done: %w", err)
			}
			callIndex := -1
			for candidate := len(observation.Calls) - 1; candidate >= 0; candidate-- {
				if observation.Calls[candidate].ArgumentsIndex >= 0 {
					continue
				}
				if event.CallID == "" || observation.Calls[candidate].CallID == event.CallID {
					callIndex = candidate
					break
				}
			}
			if callIndex < 0 {
				return observation, fmt.Errorf("function-call arguments have no correlating call_id=%q", event.CallID)
			}
			observation.Calls[callIndex].ArgumentsIndex = index
			observation.Calls[callIndex].Arguments = event.Arguments
			if observation.Calls[callIndex].CallID == "" {
				observation.Calls[callIndex].CallID = event.CallID
			}
			if observation.Calls[callIndex].Name == "" {
				observation.Calls[callIndex].Name = event.Name
			}
		}
	}
	return observation, nil
}

func validateCubecadeCapture(observation cubecadeCaptureObservation, expectedBrowserID, expectedTargetID string) (cubecadeValidation, error) {
	validation := cubecadeValidation{}
	if observation.Provider != "openai" || observation.Model != cubecadeModel {
		return validation, fmt.Errorf("provider identity=(%q,%q), want (openai,%q)", observation.Provider, observation.Model, cubecadeModel)
	}
	if observation.SessionUpdates != 1 || !sameCubecadeStrings(observation.AdvertisedTools, webmcp.StableToolNames()) {
		return validation, fmt.Errorf("session update count/tools=(%d,%v), want one update with stable broker tools", observation.SessionUpdates, observation.AdvertisedTools)
	}
	if len(observation.Calls) == 0 || observation.Calls[0].Name != webmcp.ListToolsToolName {
		return validation, fmt.Errorf("first model browser call = %#v, want %s", observation.Calls, webmcp.ListToolsToolName)
	}
	outputs := make(map[string]cubecadeOutput, len(observation.Outputs))
	for _, output := range observation.Outputs {
		if output.CallID == "" {
			return validation, fmt.Errorf("tool result at record %d has no call_id", output.Index)
		}
		if _, exists := outputs[output.CallID]; exists {
			return validation, fmt.Errorf("tool result call_id=%q occurred more than once", output.CallID)
		}
		outputs[output.CallID] = output
	}
	firstList := true
	listSuccessSeen := false
	retryableListSeen := false
	recoveredListSeen := false
	for _, call := range observation.Calls {
		if call.CallID == "" || call.ArgumentsIndex <= call.Index {
			return validation, fmt.Errorf("call order/correlation invalid: %+v", call)
		}
		output, ok := outputs[call.CallID]
		if !ok || output.Index <= call.ArgumentsIndex {
			return validation, fmt.Errorf("call %q has no terminal result", call.CallID)
		}
		switch call.Name {
		case webmcp.ListToolsToolName:
			if firstList {
				validation.FirstListCallMS = call.TimestampMS
				firstList = false
			}
			if output.Envelope.OK {
				validation.SuccessfulListCalls++
				if validation.FirstListResultMS == 0 {
					validation.FirstListResultMS = output.TimestampMS
				}
				if retryableListSeen && !recoveredListSeen {
					validation.RecoveredListResultMS = output.TimestampMS
					recoveredListSeen = true
				}
				var data struct {
					Generation uint64 `json:"generation"`
					Tools      []struct {
						Name string `json:"name"`
					} `json:"tools"`
				}
				if err := json.Unmarshal(output.Envelope.Data, &data); err != nil {
					return validation, fmt.Errorf("decode successful catalog: %w", err)
				}
				if data.Generation == 0 {
					return validation, errors.New("successful catalog has no generation")
				}
				validation.PageTools = validation.PageTools[:0]
				for _, tool := range data.Tools {
					validation.PageTools = append(validation.PageTools, tool.Name)
				}
				listSuccessSeen = true
			} else if output.Envelope.Error == nil {
				return validation, errors.New("failed list_tools result has no error")
			} else if output.Envelope.Error.Details["reason_code"] == "page_tools_unverified" {
				if listSuccessSeen {
					return validation, errors.New("catalog evidence became unverified after a successful list")
				}
				failure := output.Envelope.Error
				if failure.Code != string(webmcp.ErrorBrowserProtocol) || !failure.Retryable || failure.Details["reason"] != "deadline_exceeded" {
					return validation, fmt.Errorf("catalog evidence failure is not the retryable deadline contract: %+v", failure)
				}
				validation.RetryableListErrors++
				retryableListSeen = true
			} else {
				return validation, fmt.Errorf("list_tools failed with unexpected error: %+v", output.Envelope.Error)
			}
		case webmcp.GetContextToolName:
			if !output.Envelope.OK {
				return validation, fmt.Errorf("get_context failed: %+v", output.Envelope.Error)
			}
			var data struct {
				BrowserID string `json:"browser_id"`
				TargetID  string `json:"target_id"`
				Origin    string `json:"origin"`
				URL       string `json:"url"`
				Connected bool   `json:"connected"`
			}
			if err := json.Unmarshal(output.Envelope.Data, &data); err != nil {
				return validation, fmt.Errorf("decode selected context: %w", err)
			}
			if data.BrowserID != expectedBrowserID || data.TargetID != expectedTargetID || data.Origin != cubecadeOrigin || !strings.HasPrefix(data.URL, cubecadeOrigin) || !data.Connected {
				return validation, fmt.Errorf("selected context=(%+v), want exact connected Cubecade identity", data)
			}
			validation.BrowserID, validation.TargetID = data.BrowserID, data.TargetID
		case webmcp.InvokeToolName:
			if !output.Envelope.OK {
				return validation, fmt.Errorf("page invocation failed: %+v", output.Envelope.Error)
			}
		}
	}
	if validation.SuccessfulListCalls == 0 || len(validation.PageTools) == 0 {
		return validation, errors.New("no successful page catalog was observed")
	}
	if !containsCubecadeString(validation.PageTools, "get_cube_state") || !containsCubecadeString(validation.PageTools, "queue_cube_moves") {
		return validation, fmt.Errorf("page catalog=%v, want get_cube_state and queue_cube_moves", validation.PageTools)
	}
	if validation.RetryableListErrors > 0 && validation.RecoveredListResultMS == 0 {
		return validation, errors.New("retryable catalog failure was not followed by a successful list")
	}
	for _, call := range observation.Calls {
		if call.Name == webmcp.InvokeToolName {
			validation.BrokerInvocations++
		}
	}
	if validation.BrowserID == "" || validation.TargetID == "" {
		return validation, errors.New("capture did not include a connected exact selected context")
	}
	return validation, nil
}

func validateCubecadeOracle(oracle cubecadePageOracle) error {
	if oracle.URL == "" || !strings.HasPrefix(oracle.URL, cubecadeOrigin) {
		return fmt.Errorf("DOM URL=%q, want Cubecade origin", oracle.URL)
	}
	if !oracle.ModelContextAvailable {
		return errors.New("DOM oracle could not observe the WebMCP modelContext producer")
	}
	if !oracle.Solved || !strings.Contains(strings.ToUpper(oracle.Status), "SOLVED") {
		return fmt.Errorf("DOM solved state=(%t,%q), want solved", oracle.Solved, oracle.Status)
	}
	if count := strings.Count(oracle.Terminal, "$ queue_cube_moves [6]"); count != 2 {
		return fmt.Errorf("DOM queue invocation log count=%d, want exactly two", count)
	}
	if !strings.Contains(oracle.Terminal, "$ get_cube_state") || !strings.Contains(oracle.State, "solved: true") {
		return fmt.Errorf("DOM terminal/state=%q/%q, want final get_cube_state solved=true", oracle.Terminal, oracle.State)
	}
	return nil
}

func inspectCubecadeTarget(ctx context.Context, endpoint, targetID string) (oracle cubecadePageOracle, err error) {
	rootContext, cancelRoot := context.WithTimeout(ctx, 20*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	targetContext, cancelTarget := chromedp.NewContext(allocatorContext, chromedp.WithTargetID(cdpTarget.ID(targetID)))
	defer func() {
		cleanupErr := detachExternalIntegrationTarget(targetContext, cancelTarget)
		cancelAllocator()
		if err == nil && cleanupErr != nil {
			err = cleanupErr
		}
	}()
	if err := chromedp.Run(targetContext, chromedp.WaitReady("main")); err != nil {
		return oracle, fmt.Errorf("wait for Cubecade DOM: %w", err)
	}
	if err := chromedp.Run(targetContext, chromedp.Evaluate(cubecadeOracleExpression(), &oracle)); err != nil {
		return oracle, fmt.Errorf("read Cubecade DOM oracle: %w", err)
	}
	return oracle, nil
}

func cubecadeOracleExpression() string {
	return `(() => {
  const context = document.modelContext || navigator.modelContext;
  const solved = document.querySelector(".solved");
  const terminal = document.querySelector(".term-lines");
  const state = document.querySelector("code");
  return {
    url: location.href,
    modelContextAvailable: Boolean(context && typeof context.registerTool === "function"),
    solved: Boolean(solved && solved.classList.contains("yes")),
    status: solved ? String(solved.textContent || "") : "",
    terminal: terminal ? String(terminal.textContent || "") : "",
    state: state ? String(state.textContent || "") : ""
  };
})()`
}

func waitForCubecadePageTarget(ctx context.Context, baseURL string) (devToolsTarget, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		targets, err := readDevToolsTargets(ctx, baseURL)
		if err == nil {
			for _, target := range targets {
				parsed, parseErr := url.Parse(target.URL)
				if target.Type == "page" && parseErr == nil && parsed.Scheme == "https" && parsed.Host == "cubecade.openai.chatgpt.site" {
					return target, nil
				}
			}
			lastErr = errors.New("Cubecade page target is not present")
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return devToolsTarget{}, fmt.Errorf("wait for Cubecade page target: %w (last error: %v)", ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func writeCubecadeConfig(configDir, cdpURL string) error {
	contents := fmt.Sprintf(`browser:
  tools:
    enabled: true
    backend: webmcp
  connection:
    cdp_url: %q
    allow_remote_cdp: false
  selection:
    auto_select: single
    activate_tab: false
    persist: false
  policy:
    allowed_origins:
      - %q
    approval: never
    cancel_on_interrupt: read-only
  limits:
    invocation_timeout: 20s
  recording:
    enabled: true
    include_arguments: true
    include_results: true
`, cdpURL, cubecadeOrigin)
	return os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(contents), cubecadeEvidenceMode)
}

func cubecadeArtifactRoot(t *testing.T) string {
	t.Helper()
	if root := strings.TrimSpace(os.Getenv(cubecadeArtifactEnv)); root != "" {
		if err := os.MkdirAll(root, cubecadeArtifactMode); err != nil {
			t.Fatalf("create Cubecade artifact directory: %v", err)
		}
		return root
	}
	return t.TempDir()
}

func waitUntilCubecade(ctx context.Context, deadline time.Time) error {
	delay := time.Until(deadline)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func cubecadeEvidenceFlags(pinned pinnedChrome, pageURL string) []string {
	flags := pinnedChromeLaunchFlags(filepath.Join(pinned.WorkDir, "profile"), pageURL, 0)
	for index, flag := range flags {
		if strings.HasPrefix(flag, "--user-data-dir=") {
			flags[index] = "--user-data-dir=<temp>/profile"
		}
	}
	return flags
}

func cubecadeEvidenceCommand(args []string) string {
	return strings.Join(append([]string{"<temp>/agent", "--config-dir", "<temp>/config"}, args...), " ")
}

func writeCubecadeEvidence(path string, evidence cubecadeEvidence) error {
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, cubecadeEvidenceMode)
}

func sameCubecadeStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func containsCubecadeString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
