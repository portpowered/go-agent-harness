package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	webmcptools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

var (
	// ErrBrowserConversationFixtureStartup identifies failure before the
	// scenario's browser fixture was ready for a session.
	ErrBrowserConversationFixtureStartup = errors.New("WebMCP browser conversation fixture startup failed")
	// ErrBrowserConversationSessionStartup identifies failure while establishing
	// the injected session boundary.
	ErrBrowserConversationSessionStartup = errors.New("WebMCP browser conversation session startup failed")
	// ErrBrowserConversationSession identifies failure from an established
	// session runner.
	ErrBrowserConversationSession = errors.New("WebMCP browser conversation session failed")
	// ErrBrowserConversationEvidence identifies malformed or incomplete joined
	// evidence. Mechanical checks remain authoritative over validator prose.
	ErrBrowserConversationEvidence = errors.New("WebMCP browser conversation evidence failed")
	// ErrBrowserConversationTimeout identifies expiration of the run-scoped
	// deadline.
	ErrBrowserConversationTimeout = errors.New("WebMCP browser conversation timed out")
	// ErrBrowserConversationCleanup identifies a resource cleanup failure.
	ErrBrowserConversationCleanup = errors.New("WebMCP browser conversation cleanup failed")
	// ErrBrowserConversationValidator identifies a validator-agent failure.
	ErrBrowserConversationValidator = errors.New("WebMCP browser conversation validator failed")
	// ErrBrowserConversationSessionBoundaryRequired prevents the default runner
	// from falling through to credentials, network, microphone, or speaker
	// setup when no hermetic session boundary was injected.
	ErrBrowserConversationSessionBoundaryRequired = errors.New("WebMCP browser conversation requires an injected session boundary")
)

// BrowserConversationRunPhase identifies the boundary that attributed a run
// failure. It is deliberately narrower than provider-specific error taxonomies.
type BrowserConversationRunPhase string

const (
	BrowserConversationPhaseFixtureStartup BrowserConversationRunPhase = "fixture_startup"
	BrowserConversationPhaseSessionStartup BrowserConversationRunPhase = "session_startup"
	BrowserConversationPhaseSession        BrowserConversationRunPhase = "session"
	BrowserConversationPhaseEvidence       BrowserConversationRunPhase = "evidence"
	BrowserConversationPhaseTimeout        BrowserConversationRunPhase = "timeout"
	BrowserConversationPhaseCleanup        BrowserConversationRunPhase = "cleanup"
	BrowserConversationPhaseValidator      BrowserConversationRunPhase = "validator"
)

// BrowserConversationRunError carries safe phase and step attribution while
// preserving errors.Is support for the stable phase sentinel.
type BrowserConversationRunError struct {
	Phase  BrowserConversationRunPhase
	StepID string
	Cause  error
}

func (e *BrowserConversationRunError) Error() string {
	if e == nil {
		return "WebMCP browser conversation run failed"
	}
	message := "WebMCP browser conversation " + browserConversationPhaseText(e.Phase)
	if e.StepID != "" {
		message += " at step " + safeBrowserConversationText(e.StepID)
	}
	if e.Cause != nil {
		message += ": " + safeBrowserConversationError(e.Cause)
	}
	return message
}

func (e *BrowserConversationRunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(browserConversationPhaseSentinel(e.Phase), e.Cause)
}

func browserConversationPhaseSentinel(phase BrowserConversationRunPhase) error {
	switch phase {
	case BrowserConversationPhaseFixtureStartup:
		return ErrBrowserConversationFixtureStartup
	case BrowserConversationPhaseSessionStartup:
		return ErrBrowserConversationSessionStartup
	case BrowserConversationPhaseSession:
		return ErrBrowserConversationSession
	case BrowserConversationPhaseEvidence:
		return ErrBrowserConversationEvidence
	case BrowserConversationPhaseTimeout:
		return ErrBrowserConversationTimeout
	case BrowserConversationPhaseCleanup:
		return ErrBrowserConversationCleanup
	case BrowserConversationPhaseValidator:
		return ErrBrowserConversationValidator
	default:
		return errors.New("WebMCP browser conversation run failed")
	}
}

func browserConversationPhaseText(phase BrowserConversationRunPhase) string {
	switch phase {
	case BrowserConversationPhaseFixtureStartup:
		return "fixture startup failed"
	case BrowserConversationPhaseSessionStartup:
		return "session startup failed"
	case BrowserConversationPhaseSession:
		return "session failed"
	case BrowserConversationPhaseEvidence:
		return "evidence failed"
	case BrowserConversationPhaseTimeout:
		return "timed out"
	case BrowserConversationPhaseCleanup:
		return "cleanup failed"
	case BrowserConversationPhaseValidator:
		return "validator failed"
	default:
		return "run failed"
	}
}

func browserConversationPhaseError(phase BrowserConversationRunPhase, stepID string, cause error) error {
	if cause == nil {
		cause = errors.New(browserConversationPhaseText(phase))
	}
	var existing *BrowserConversationRunError
	if errors.As(cause, &existing) {
		return cause
	}
	return &BrowserConversationRunError{Phase: phase, StepID: stepID, Cause: cause}
}

// safeBrowserConversationError intentionally drops provider prose and any
// credential-shaped material. Error attribution retains only bounded,
// control-free diagnostic text.
func safeBrowserConversationError(err error) string {
	if err == nil {
		return ""
	}
	return safeBrowserConversationText(err.Error())
}

func safeBrowserConversationText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"authorization:", "bearer ", "api_key", "api-key", "access_token",
		"refresh_token", "client_secret", "password", "-----begin ", "sk-",
	} {
		if strings.Contains(lower, marker) {
			return "[redacted]"
		}
	}
	var builder strings.Builder
	for _, char := range value {
		switch char {
		case '\n', '\r', '\t':
			builder.WriteByte(' ')
		default:
			if char < 0x20 || char == 0x7f {
				builder.WriteByte(' ')
			} else {
				builder.WriteRune(char)
			}
		}
		if builder.Len() >= 256 {
			break
		}
	}
	result := strings.TrimSpace(builder.String())
	if result == "" {
		return "unknown error"
	}
	return result
}

// BrowserConversationFixtureRun owns one declarative fixture, adapter, broker,
// and independent state oracle. Close is idempotent and is the only cleanup
// entry point used by the coordinator.
type BrowserConversationFixtureRun struct {
	Runtime *testkit.BrowserScriptRuntime
	Adapter *testkit.BrowserScriptAdapter
	Broker  webmcp.Broker
	Oracle  *testkit.FixtureStateOracle

	closeOnce  sync.Once
	closeMu    sync.Mutex
	closeErr   error
	closeCount int
}

// NewBrowserConversationFixtureRun creates a fresh fixture boundary without
// consuming any scripted operation.
func NewBrowserConversationFixtureRun(script testkit.BrowserScript, options ...BrowserConversationFixtureOption) (*BrowserConversationFixtureRun, error) {
	if err := script.Validate(); err != nil {
		return nil, errors.Join(ErrBrowserConversationFixtureStartup, err)
	}
	config := browserConversationFixtureConfig{}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	runtime, err := testkit.NewScriptedFixtureRuntime(script, config.runtimeOptions...)
	if err != nil {
		return nil, errors.Join(ErrBrowserConversationFixtureStartup, err)
	}
	adapter, err := testkit.NewBrowserScriptAdapter(script, runtime)
	if err != nil {
		return nil, errors.Join(ErrBrowserConversationFixtureStartup, errors.Join(err, runtime.Close()))
	}
	brokerOptions := config.brokerOptions
	brokerOptions.Runtime = adapter
	brokerOptions.Discoverer = adapter
	if brokerOptions.IDs == nil {
		brokerOptions.IDs = testkit.NewDeterministicIDSource("browser-conversation")
	}
	if brokerOptions.Ownership == "" {
		brokerOptions.Ownership = webmcp.TargetOwnershipExternal
	}
	if brokerOptions.ToolRefFactory == nil {
		brokerOptions.ToolRefFactory = webmcp.StableToolRef
	}
	broker := webmcp.NewBroker(brokerOptions)
	return &BrowserConversationFixtureRun{
		Runtime: runtime,
		Adapter: adapter,
		Broker:  broker,
		Oracle:  runtime.StateOracle(),
	}, nil
}

// Close releases broker and fixture resources exactly once. The broker closes
// the external target attachment; it never closes the browser process.
func (f *BrowserConversationFixtureRun) Close() error {
	if f == nil {
		return nil
	}
	f.closeOnce.Do(func() {
		f.closeMu.Lock()
		f.closeCount++
		f.closeMu.Unlock()
		var closeErr error
		if f.Broker != nil {
			closeErr = errors.Join(closeErr, f.Broker.Close())
		}
		if f.Runtime != nil {
			closeErr = errors.Join(closeErr, f.Runtime.Close())
		}
		f.closeMu.Lock()
		f.closeErr = closeErr
		f.closeMu.Unlock()
	})
	f.closeMu.Lock()
	defer f.closeMu.Unlock()
	return f.closeErr
}

// Cleanup is a descriptive alias for Close.
func (f *BrowserConversationFixtureRun) Cleanup() error {
	return f.Close()
}

// Navigate applies one customer-owned page transition through the existing
// fixture adapter. The adapter, broker, and session continue to share the
// same target generation and event stream.
func (f *BrowserConversationFixtureRun) Navigate(ctx context.Context, navigation BrowserCustomerNavigation) error {
	if err := browserConversationContextError(ctx); err != nil {
		return err
	}
	if f == nil || f.Adapter == nil {
		return errors.New("browser conversation fixture has no navigation adapter")
	}
	if strings.TrimSpace(navigation.URL) == "" {
		return errors.New("browser conversation navigation URL is required")
	}
	return f.Adapter.Navigate(ctx, navigation.URL)
}

// CloseCount exposes the exactly-once cleanup fact for harness diagnostics.
func (f *BrowserConversationFixtureRun) CloseCount() int {
	if f == nil {
		return 0
	}
	f.closeMu.Lock()
	defer f.closeMu.Unlock()
	return f.closeCount
}

// ReadBrowserConversationState implements the independent fixture oracle seam.
func (f *BrowserConversationFixtureRun) ReadBrowserConversationState(ctx context.Context, pageID string) (json.RawMessage, error) {
	if err := browserConversationContextError(ctx); err != nil {
		return nil, err
	}
	if f == nil || f.Oracle == nil {
		return nil, errors.New("browser conversation fixture has no state oracle")
	}
	if strings.TrimSpace(pageID) == "" {
		return nil, errors.New("browser conversation oracle page_id is required")
	}
	return append(json.RawMessage(nil), f.Oracle.Snapshot()...), nil
}

// ProbeBrowserConversationTab provides the default post-session probe. Real
// callers can replace it with a stronger independent tab probe.
func (f *BrowserConversationFixtureRun) ProbeBrowserConversationTab(ctx context.Context, pageID string) (BrowserConversationTabStateProbeResult, error) {
	if err := browserConversationContextError(ctx); err != nil {
		return BrowserConversationTabStateProbeResult{}, err
	}
	if f == nil || f.Runtime == nil {
		return BrowserConversationTabStateProbeResult{}, errors.New("browser conversation fixture is unavailable")
	}
	if strings.TrimSpace(pageID) == "" {
		return BrowserConversationTabStateProbeResult{}, errors.New("browser conversation post-session page_id is required")
	}
	targetClosed := false
	for _, operation := range f.Runtime.Operations() {
		if operation.Type == testkit.OperationCloseTarget {
			targetClosed = true
		}
	}
	outcome := f.Runtime.Outcome()
	responsive := outcome.Status == testkit.BrowserScriptCompleted
	state := f.Runtime.PageState()
	if len(state) == 0 {
		return BrowserConversationTabStateProbeResult{}, errors.New("browser conversation post-session tab read returned no state")
	}
	if f.Oracle == nil {
		return BrowserConversationTabStateProbeResult{}, errors.New("browser conversation post-session tab has no writable state oracle")
	}
	if err := f.Oracle.SetJSON(state); err != nil {
		return BrowserConversationTabStateProbeResult{}, fmt.Errorf("browser conversation post-session tab mutation: %w", err)
	}
	target := f.Runtime.Target()
	return BrowserConversationTabStateProbeResult{
		PageID:            pageID,
		BrowserID:         webmcp.BrowserID(f.Runtime.BrowserID()),
		TargetID:          webmcp.TargetID(target.ID),
		Alive:             !targetClosed,
		Responsive:        responsive,
		AllowsMutation:    !targetClosed && responsive,
		ReadSucceeded:     true,
		MutationSucceeded: true,
	}, nil
}

// RunBrowserConversation executes one bounded, joined browser conversation.
// It validates the scenario and audio before constructing any fixture or
// session boundary.
func RunBrowserConversation(ctx context.Context, out io.Writer, options BrowserConversationRunOptions) (BrowserConversationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	scenario, err := NewBrowserConversationScenario(options.Scenario)
	if err != nil {
		return BrowserConversationResult{}, err
	}
	audioInputs, err := scenario.ScheduleAudioInputs(options.AudioByStep)
	if err != nil {
		return BrowserConversationResult{}, err
	}
	sessionAudioInputs, interruptionAudio := partitionBrowserConversationAudio(scenario, audioInputs)
	run, err := NewBrowserConversationRun(scenario)
	if err != nil {
		return BrowserConversationResult{}, err
	}
	if out == nil {
		out = options.Output
	}
	if out == nil {
		out = io.Discard
	}

	runContext, cancel := context.WithTimeout(ctx, scenario.RunTimeout)
	defer cancel()
	tracker := newBrowserConversationEvidenceTracker(run, scenario)
	tracker.configure(runContext, cancel, nil, options.CustomerNavigate)
	interruptionController := newBrowserConversationInterruptionController(run, tracker, scenario, interruptionAudio)
	if interruptionController != nil {
		defer interruptionController.Close()
	}
	var fixture *BrowserConversationFixtureRun
	customerNavigate := options.CustomerNavigate
	var observedBroker *browserConversationBroker
	var rootErr error
	lifecycle := BrowserConversationLifecycleEvidence{
		Outcome: BrowserConversationLifecycleNotStarted,
	}

	addRootError := func(value error) {
		if value != nil {
			rootErr = errors.Join(rootErr, value)
		}
	}
	addContextError := func(phase BrowserConversationRunPhase, stepID string, value error) {
		if value == nil {
			return
		}
		if errors.Is(value, context.DeadlineExceeded) || errors.Is(runContext.Err(), context.DeadlineExceeded) {
			addRootError(browserConversationPhaseError(BrowserConversationPhaseTimeout, stepID, value))
			return
		}
		if errors.Is(value, context.Canceled) || errors.Is(runContext.Err(), context.Canceled) {
			addRootError(browserConversationPhaseError(phase, stepID, value))
			return
		}
		addRootError(browserConversationPhaseError(phase, stepID, value))
	}

	if contextErr := runContext.Err(); contextErr != nil {
		addContextError(BrowserConversationPhaseFixtureStartup, "", contextErr)
	} else {
		factory := options.FixtureFactory
		if factory == nil {
			factory = func(factoryContext context.Context, _ BrowserConversationScenario) (*BrowserConversationFixtureRun, error) {
				return NewBrowserConversationFixtureRun(options.FixtureScript, options.FixtureOptions...)
			}
		}
		fixture, err = factory(runContext, scenario)
		if err != nil {
			addContextError(BrowserConversationPhaseFixtureStartup, "", err)
		}
		if fixture == nil && rootErr == nil {
			addRootError(browserConversationPhaseError(
				BrowserConversationPhaseFixtureStartup,
				"",
				errors.New("fixture factory returned no fixture"),
			))
		}
		if fixture != nil && fixture.Broker == nil {
			addRootError(browserConversationPhaseError(
				BrowserConversationPhaseFixtureStartup,
				"",
				errors.New("fixture has no broker"),
			))
		}
		if fixture != nil {
			if customerNavigate == nil {
				customerNavigate = func(navigationContext context.Context, runFixture *BrowserConversationFixtureRun, navigation BrowserCustomerNavigation) error {
					return runFixture.Navigate(navigationContext, navigation)
				}
			}
			tracker.configure(runContext, cancel, fixture, customerNavigate)
		}
		if fixture != nil && rootErr == nil {
			observedBroker = newBrowserConversationBroker(fixture.Broker, run, tracker, scenario, options.Oracle, fixture, interruptionController)
			tracker.setCancelInvocation(func(cancelContext context.Context, invocationID webmcp.InvocationID, reason string) error {
				return observedBroker.Cancel(cancelContext, webmcp.CancelRequest{InvocationID: invocationID, Reason: reason})
			})
			if err := prepareBrowserConversationFixture(runContext, scenario, observedBroker); err != nil {
				addContextError(BrowserConversationPhaseFixtureStartup, "", err)
			}
		}
		if fixture != nil && rootErr == nil {
			toolSet := webmcptools.NewBrokerToolSet(observedBroker)
			sessionRequest := BrowserConversationSessionRequest{
				Scenario:        cloneBrowserConversationScenario(scenario),
				Fixture:         fixture,
				Broker:          observedBroker,
				ToolExecutor:    toolSet.Executor(),
				ToolDefinitions: toolSet.Definitions(),
				AudioInputs:     cloneScheduledAudioInputs(sessionAudioInputs),
				AudioInterruptions: func() <-chan ScheduledAudioInput {
					if interruptionController == nil {
						return nil
					}
					return interruptionController.AudioInterruptions()
				}(),
				SessionOptions:   options.SessionOptions,
				StreamObserver:   tracker.observe,
				CustomerNavigate: customerNavigate,
			}
			lifecycle.SessionStarted = true
			sessionRunner := options.SessionRunner
			if sessionRunner == nil {
				sessionRunner = runBrowserConversationSession
			}
			if sessionRequest.AudioInterruptions != nil {
				sessionRequest.SessionOptions.AudioInterruptions = sessionRequest.AudioInterruptions
			}
			sessionErr := sessionRunner(runContext, out, sessionRequest)
			lifecycle.SessionTerminated = true
			if sessionErr != nil {
				phase := BrowserConversationPhaseSession
				if errors.Is(sessionErr, ErrBrowserConversationSessionBoundaryRequired) || strings.Contains(strings.ToLower(sessionErr.Error()), "connect session") {
					phase = BrowserConversationPhaseSessionStartup
				}
				addContextError(phase, tracker.currentStep(), sessionErr)
			}
		}
	}

	if fixture != nil {
		// Detach before probing. The probe is intentionally independent of the
		// session-owned broker and runs even after the session context was
		// canceled, proving that external tab ownership survived termination.
		cleanupErr := fixture.Close()
		if cleanupErr != nil {
			addRootError(browserConversationPhaseError(BrowserConversationPhaseCleanup, "", cleanupErr))
		}
		detachCount, targetClosed := browserConversationFixtureLifecycle(fixture)
		lifecycle.DetachCount = detachCount
		lifecycle.Detached = detachCount == 1
		lifecycle.DetachRequired = fixture.Runtime != nil
		lifecycle.TargetClosed = targetClosed

		probe := options.PostSessionProbe
		if probe == nil {
			probe = func(probeContext context.Context, runFixture *BrowserConversationFixtureRun, pageID string) (BrowserConversationTabStateProbeResult, error) {
				return runFixture.ProbeBrowserConversationTab(probeContext, pageID)
			}
		}
		probeContext, probeCancel := context.WithTimeout(context.Background(), scenario.RunTimeout)
		probeResult, probeErr := probe(probeContext, fixture, scenario.PostSession.PageID)
		if probeErr != nil {
			addContextError(BrowserConversationPhaseEvidence, "", probeErr)
		} else {
			lifecycle.ExternalTabAlive = probeResult.Alive
			lifecycle.ExternalTabResponsive = probeResult.Responsive
			lifecycle.ExternalTabAllowsMutation = probeResult.AllowsMutation
			lifecycle.ExternalTabRead = probeResult.ReadSucceeded
			lifecycle.ExternalTabMutation = probeResult.MutationSucceeded
			lifecycle.ExternalBrowserID = probeResult.BrowserID
			lifecycle.ExternalTargetID = probeResult.TargetID
			if fixture.Runtime != nil && probeResult.BrowserID != "" && probeResult.BrowserID != webmcp.BrowserID(fixture.Runtime.BrowserID()) {
				addRootError(browserConversationPhaseError(BrowserConversationPhaseEvidence, "", errors.New("post-session probe returned a different browser")))
			}
			if fixture.Runtime != nil && probeResult.TargetID != "" && probeResult.TargetID != webmcp.TargetID(fixture.Runtime.Target().ID) {
				addRootError(browserConversationPhaseError(BrowserConversationPhaseEvidence, "", errors.New("post-session probe returned a different target")))
			}
			if probeResult.PageID != "" && probeResult.PageID != scenario.PostSession.PageID {
				addRootError(browserConversationPhaseError(
					BrowserConversationPhaseEvidence,
					"",
					errors.New("post-session probe returned the wrong page"),
				))
			}
			reader := options.Oracle
			if reader == nil {
				reader = fixture
			}
			if reader != nil {
				state, stateErr := reader.ReadBrowserConversationState(probeContext, scenario.PostSession.PageID)
				if stateErr != nil {
					addContextError(BrowserConversationPhaseEvidence, "", stateErr)
				} else if observeErr := run.ObserveOracleSnapshot(BrowserConversationOracleSnapshot{
					StepID: "",
					PageID: scenario.PostSession.PageID,
					Phase:  BrowserConversationOraclePostSession,
					State:  state,
				}); observeErr != nil {
					addRootError(browserConversationPhaseError(BrowserConversationPhaseEvidence, "", observeErr))
				}
			}
		}
		probeCancel()
	}

	tracker.stopDeadline()
	if evidenceErr := tracker.err(); evidenceErr != nil {
		addRootError(browserConversationPhaseError(BrowserConversationPhaseEvidence, tracker.currentStep(), evidenceErr))
	}
	if lateEvents := tracker.lateEventCount(); lateEvents > 0 {
		if err := run.RecordCancellation(BrowserConversationCancellationEvidence{LateEventsSuppressed: lateEvents}); err != nil {
			addRootError(browserConversationPhaseError(BrowserConversationPhaseEvidence, "", err))
		}
	}
	if rootErr == nil {
		if contextErr := runContext.Err(); contextErr != nil {
			addContextError(BrowserConversationPhaseSession, tracker.currentStep(), contextErr)
		}
	}

	lifecycle.Outcome = browserConversationLifecycleOutcome(rootErr, runContext)
	if rootErr != nil {
		lifecycle.Error = safeBrowserConversationError(rootErr)
	}
	provisional := run.Snapshot()
	corrections := deriveBrowserConversationCorrections(scenario, provisional)
	if err := run.RecordCorrections(corrections); err != nil {
		addRootError(browserConversationPhaseError(BrowserConversationPhaseEvidence, "", err))
	}
	provisional = run.Snapshot()
	recoveries := deriveBrowserConversationRecovery(scenario, provisional)
	if err := run.RecordRecovery(recoveries); err != nil {
		addRootError(browserConversationPhaseError(BrowserConversationPhaseEvidence, "", err))
	}
	provisional = run.Snapshot()
	provisional.Lifecycle = lifecycle
	evaluation := evaluateBrowserConversation(scenario, provisional, rootErr)
	if !evaluation.Passed && lifecycle.Outcome == BrowserConversationLifecycleCompleted {
		lifecycle.Outcome = BrowserConversationLifecycleFailed
		lifecycle.Error = "mechanical evidence failed"
	}
	if err := run.RecordLifecycle(lifecycle); err != nil {
		addRootError(browserConversationPhaseError(BrowserConversationPhaseEvidence, "", err))
	}
	if err := run.RecordMechanicalEvaluation(evaluation); err != nil {
		addRootError(browserConversationPhaseError(BrowserConversationPhaseEvidence, "", err))
	}
	if !evaluation.Passed {
		addRootError(browserConversationPhaseError(
			BrowserConversationPhaseEvidence,
			"",
			errors.New("mechanical checks did not pass"),
		))
	}

	validatorErr := recordBrowserConversationValidator(run, options.Validator)
	if validatorErr != nil {
		addRootError(browserConversationPhaseError(BrowserConversationPhaseValidator, "", validatorErr))
	}
	result, finalizeErr := run.Finalize()
	if finalizeErr != nil {
		addRootError(browserConversationPhaseError(BrowserConversationPhaseEvidence, "", finalizeErr))
	}
	if validateErr := result.Validate(); validateErr != nil {
		addRootError(browserConversationPhaseError(BrowserConversationPhaseEvidence, "", validateErr))
	}
	return result, rootErr
}

// RunBrowserConversationScenario is the scenario-first spelling.
func RunBrowserConversationScenario(ctx context.Context, out io.Writer, scenario BrowserConversationScenario, options BrowserConversationRunOptions) (BrowserConversationResult, error) {
	options.Scenario = scenario
	return RunBrowserConversation(ctx, out, options)
}

// RunWebMCPConversationScenario is the product-name alias.
func RunWebMCPConversationScenario(ctx context.Context, out io.Writer, scenario BrowserConversationScenario, options BrowserConversationRunOptions) (BrowserConversationResult, error) {
	return RunBrowserConversationScenario(ctx, out, scenario, options)
}

// RunWebMCPConversation is the options-first product-name alias.
func RunWebMCPConversation(ctx context.Context, out io.Writer, options BrowserConversationRunOptions) (BrowserConversationResult, error) {
	return RunBrowserConversation(ctx, out, options)
}

func runBrowserConversationSession(ctx context.Context, out io.Writer, request BrowserConversationSessionRequest) error {
	if request.SessionOptions.SessionInferencer == nil {
		return ErrBrowserConversationSessionBoundaryRequired
	}
	sessionOptions := request.SessionOptions
	// An injected inferencer is the hermetic provider boundary. Clear capture
	// paths so this runner cannot open files or select a transport-backed
	// provider runtime around it.
	sessionOptions.RecordPath = ""
	sessionOptions.ReplayPath = ""
	sessionOptions.BrowserToolsEnabled = true
	sessionOptions.ToolExecutor = request.ToolExecutor
	sessionOptions.ToolDefinitions = append([]messages.ToolDefinition(nil), request.ToolDefinitions...)
	sessionOptions.AudioInputs = cloneScheduledAudioInputs(request.AudioInputs)
	sessionOptions.AudioInterruptions = request.AudioInterruptions
	sessionOptions.WaitForClose = true
	sessionOptions.StreamObserver = combineBrowserConversationStreamObservers(
		sessionOptions.StreamObserver,
		request.StreamObserver,
	)
	if strings.TrimSpace(sessionOptions.Provider) == "" && sessionOptions.LoadedConfig == nil {
		sessionOptions.Provider = config.ProviderGrok
		sessionOptions.LoadedConfig = &config.Config{
			Model: config.ModelConfig{
				Provider: config.ProviderGrok,
				Grok: &config.GrokConfig{
					Model:  "browser-conversation-fixture",
					APIKey: "fixture-session",
				},
			},
		}
	}
	return RunSession(ctx, out, sessionOptions)
}

func combineBrowserConversationStreamObservers(observers ...SessionStreamObserver) SessionStreamObserver {
	var active []SessionStreamObserver
	for _, observer := range observers {
		if observer != nil {
			active = append(active, observer)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return func(message messages.StreamMessage) {
		for _, observer := range active {
			observer(message)
		}
	}
}

func prepareBrowserConversationFixture(ctx context.Context, scenario BrowserConversationScenario, broker webmcp.Broker) error {
	if broker == nil {
		return errors.New("fixture broker is nil")
	}
	candidates, err := broker.Discover(ctx, webmcp.DiscoverOptions{ExplicitOnly: true})
	if err != nil {
		return err
	}
	if len(candidates) != 1 {
		return fmt.Errorf("fixture discovery returned %d candidates, want one", len(candidates))
	}
	targets, err := broker.ListTargets(ctx, webmcp.BrowserSelector{BrowserID: candidates[0].ID})
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return errors.New("fixture discovery returned no target")
	}
	sort.SliceStable(targets, func(i, j int) bool {
		return targets[i].ID < targets[j].ID
	})
	targetID := targets[0].ID
	for _, target := range targets {
		if string(target.ID) == scenario.Fixture.InitialPage {
			targetID = target.ID
			break
		}
	}
	if _, err := broker.Select(ctx, webmcp.TargetSelector{BrowserID: candidates[0].ID, TargetID: targetID}); err != nil {
		return err
	}
	_, err = broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
	return err
}

type browserConversationBroker struct {
	inner         webmcp.Broker
	run           *BrowserConversationRun
	tracker       *browserConversationEvidenceTracker
	scenario      BrowserConversationScenario
	oracle        BrowserConversationOracleReader
	fixture       *BrowserConversationFixtureRun
	interruptions *browserConversationInterruptionController
	catalogMu     sync.Mutex
	catalog       map[webmcp.ToolRef]webmcp.ToolDescriptor
}

func newBrowserConversationBroker(
	inner webmcp.Broker,
	run *BrowserConversationRun,
	tracker *browserConversationEvidenceTracker,
	scenario BrowserConversationScenario,
	oracle BrowserConversationOracleReader,
	fixture *BrowserConversationFixtureRun,
	interruptions *browserConversationInterruptionController,
) *browserConversationBroker {
	return &browserConversationBroker{
		inner: inner, run: run, tracker: tracker, scenario: scenario,
		oracle: oracle, fixture: fixture, interruptions: interruptions,
		catalog: make(map[webmcp.ToolRef]webmcp.ToolDescriptor),
	}
}

func (b *browserConversationBroker) Discover(ctx context.Context, options webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return b.inner.Discover(ctx, options)
}

func (b *browserConversationBroker) ListTargets(ctx context.Context, selector webmcp.BrowserSelector) ([]webmcp.Target, error) {
	return b.inner.ListTargets(ctx, selector)
}

func (b *browserConversationBroker) Select(ctx context.Context, selector webmcp.TargetSelector) (webmcp.PageContext, error) {
	result, err := b.inner.Select(ctx, selector)
	b.recordOperation(BrowserConversationBrokerCall{
		StepID:     b.tracker.currentStep(),
		Operation:  BrowserConversationSelectPage,
		InputJSON:  browserConversationJSON(selector),
		Generation: result.Generation,
		ErrorCode:  browserConversationErrorCode(err),
	})
	return result, err
}

func (b *browserConversationBroker) Selected(ctx context.Context) (webmcp.PageContext, error) {
	result, err := b.inner.Selected(ctx)
	b.recordOperation(BrowserConversationBrokerCall{
		StepID:     b.tracker.currentStep(),
		Operation:  BrowserConversationWaitReady,
		Generation: result.Generation,
		ErrorCode:  browserConversationErrorCode(err),
	})
	return result, err
}

func (b *browserConversationBroker) ListTools(ctx context.Context, options webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	result, err := b.inner.ListTools(ctx, options)
	if err == nil {
		b.catalogMu.Lock()
		for _, descriptor := range result.Tools {
			b.catalog[descriptor.Ref] = cloneWebMCPToolDescriptor(descriptor)
		}
		b.catalogMu.Unlock()
	}
	b.recordOperation(BrowserConversationBrokerCall{
		StepID:     b.tracker.currentStep(),
		Operation:  BrowserConversationListTools,
		InputJSON:  browserConversationJSON(options),
		Generation: result.Generation,
		ToolRefs:   browserConversationToolRefs(result.Tools),
		Output:     browserConversationJSONRaw(result),
		ErrorCode:  browserConversationErrorCode(err),
	})
	return result, err
}

func (b *browserConversationBroker) Invoke(ctx context.Context, request webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	stepID := b.tracker.currentStep()
	if b.tracker != nil {
		resolvedStepID, stepErr := b.tracker.invocationStep(ctx)
		if stepErr != nil {
			return webmcp.InvokeResult{State: webmcp.InvocationError}, stepErr
		}
		stepID = resolvedStepID
	}
	step := browserConversationStepByID(b.scenario, stepID)
	if step != nil && browserConversationExpectedState(step) != nil {
		b.observeOracle(ctx, step, BrowserConversationOracleBefore)
	}
	toolGeneration := b.toolGeneration(request.ToolRef)
	result, err := b.inner.Invoke(ctx, request)
	if err != nil {
		b.recordOperation(BrowserConversationBrokerCall{
			StepID: stepID, Operation: BrowserConversationInvoke,
			ToolRef: request.ToolRef, ToolName: b.toolName(request.ToolRef),
			InputJSON: string(request.Input), State: webmcp.InvocationError,
			Generation: toolGeneration,
			Terminal:   true, ErrorCode: browserConversationErrorCode(err),
		})
		return result, err
	}
	b.recordOperation(b.callFromInvoke(stepID, request, result))
	if !browserConversationInvocationStateTerminal(result.State) && result.InvocationID != "" {
		if b.tracker != nil {
			b.tracker.noteInFlight(stepID, result.InvocationID)
		}
		if b.interruptions != nil {
			b.interruptions.observeInFlight(stepID, result.InvocationID, b.toolName(request.ToolRef))
		}
		if waiter, ok := b.inner.(interface {
			WaitInvocation(context.Context, webmcp.InvocationID) (webmcp.InvokeResult, error)
		}); ok {
			terminal, waitErr := waiter.WaitInvocation(ctx, result.InvocationID)
			if waitErr == nil {
				result = terminal
				b.recordOperation(b.callFromInvoke(stepID, request, result))
				if result.State == webmcp.InvocationCanceled && b.run != nil {
					current := b.run.Snapshot().Cancellation
					if current.Interrupted || current.Requested {
						if cancellationErr := b.run.RecordCancellation(BrowserConversationCancellationEvidence{
							InvocationID: result.InvocationID,
							FinalState:   result.State,
						}); cancellationErr != nil && b.tracker != nil {
							b.tracker.setError(cancellationErr)
						}
					}
				}
			} else if b.tracker != nil {
				b.tracker.setError(errors.Join(
					ErrBrowserConversationEvidence,
					fmt.Errorf("terminal invocation result unavailable: %w", waitErr),
				))
			}
		}
	}
	if result.State == webmcp.InvocationCompleted && browserConversationInvocationStateTerminal(result.State) && step != nil && browserConversationExpectedState(step) != nil {
		b.observeOracle(ctx, step, BrowserConversationOracleAfter)
	}
	return result, nil
}

func (b *browserConversationBroker) Cancel(ctx context.Context, request webmcp.CancelRequest) error {
	err := b.inner.Cancel(ctx, request)
	b.recordOperation(BrowserConversationBrokerCall{
		StepID:       b.tracker.currentStep(),
		Operation:    BrowserConversationCancel,
		InputJSON:    browserConversationJSON(request),
		Terminal:     false,
		State:        webmcp.InvocationCanceled,
		ErrorCode:    browserConversationErrorCode(err),
		InvocationID: request.InvocationID,
	})
	if err == nil && b.run != nil {
		if cancellationErr := b.run.RecordCancellation(BrowserConversationCancellationEvidence{
			Requested:    true,
			InvocationID: request.InvocationID,
			Reason:       safeBrowserConversationText(request.Reason),
		}); cancellationErr != nil && !errors.Is(cancellationErr, ErrBrowserConversationDuplicateObservation) {
			if b.tracker != nil {
				b.tracker.setError(cancellationErr)
			}
		}
	}
	return err
}

func (b *browserConversationBroker) Watch(ctx context.Context) <-chan webmcp.BrokerEvent {
	return b.inner.Watch(ctx)
}

func (b *browserConversationBroker) Close() error {
	return b.inner.Close()
}

func (b *browserConversationBroker) callFromInvoke(stepID string, request webmcp.InvokeRequest, result webmcp.InvokeResult) BrowserConversationBrokerCall {
	return BrowserConversationBrokerCall{
		StepID: stepID, Operation: BrowserConversationInvoke,
		ToolRef: request.ToolRef, ToolName: b.toolName(request.ToolRef),
		InvocationID: result.InvocationID, InputJSON: string(request.Input),
		Generation: b.toolGeneration(request.ToolRef),
		State:      result.State, Terminal: browserConversationInvocationStateTerminal(result.State),
		Output: append(json.RawMessage(nil), result.Output...), ErrorCode: result.ErrorCode,
	}
}

func (b *browserConversationBroker) toolName(ref webmcp.ToolRef) string {
	b.catalogMu.Lock()
	defer b.catalogMu.Unlock()
	return b.catalog[ref].Name
}

func (b *browserConversationBroker) toolGeneration(ref webmcp.ToolRef) uint64 {
	b.catalogMu.Lock()
	defer b.catalogMu.Unlock()
	return b.catalog[ref].Generation
}

func (b *browserConversationBroker) observeOracle(ctx context.Context, step *BrowserConversationStep, phase BrowserConversationOraclePhase) {
	transition := browserConversationExpectedState(step)
	if b == nil || b.run == nil || step == nil || transition == nil {
		return
	}
	reader := b.oracle
	if reader == nil {
		reader = b.fixture
	}
	if reader == nil {
		if b.tracker != nil {
			b.tracker.setError(errors.New("expected-state oracle is unavailable"))
		}
		return
	}
	state, err := reader.ReadBrowserConversationState(ctx, transition.PageID)
	if err != nil {
		if b.tracker != nil {
			b.tracker.setError(err)
		}
		return
	}
	if err := b.run.ObserveOracleSnapshot(BrowserConversationOracleSnapshot{
		StepID: step.ID, PageID: transition.PageID, Phase: phase, State: state,
		Generation: browserConversationFixtureGeneration(b.fixture),
	}); err != nil && b.tracker != nil {
		b.tracker.setError(err)
	}
}

func (b *browserConversationBroker) recordOperation(call BrowserConversationBrokerCall) {
	if b == nil || b.run == nil {
		return
	}
	if err := b.run.ObserveBrokerCall(call); err != nil && b.tracker != nil {
		b.tracker.setError(err)
	}
}

var _ webmcp.Broker = (*browserConversationBroker)(nil)

func browserConversationFixtureGeneration(fixture *BrowserConversationFixtureRun) uint64 {
	if fixture == nil || fixture.Runtime == nil {
		return 0
	}
	return fixture.Runtime.Generation()
}

func browserConversationFixtureLifecycle(fixture *BrowserConversationFixtureRun) (int, bool) {
	if fixture == nil || fixture.Runtime == nil {
		return 0, false
	}
	detachCount := 0
	targetClosed := false
	for _, operation := range fixture.Runtime.Operations() {
		switch operation.Type {
		case testkit.OperationDetachTarget:
			detachCount++
		case testkit.OperationCloseTarget:
			targetClosed = true
		}
	}
	return detachCount, targetClosed
}

func browserConversationLifecycleOutcome(rootErr error, ctx context.Context) BrowserConversationLifecycleOutcome {
	if errors.Is(rootErr, ErrBrowserConversationTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return BrowserConversationLifecycleTimedOut
	}
	if errors.Is(rootErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return BrowserConversationLifecycleCanceled
	}
	if rootErr != nil {
		return BrowserConversationLifecycleFailed
	}
	return BrowserConversationLifecycleCompleted
}

func browserConversationContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func browserConversationErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil {
		return string(classified.Code)
	}
	code := webmcp.ContextErrorCode(err)
	if code != "" {
		return string(code)
	}
	return "operation_failed"
}

func browserConversationJSON(value any) string {
	raw := browserConversationJSONRaw(value)
	return string(raw)
}

func browserConversationJSONRaw(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}

func cloneWebMCPToolDescriptor(descriptor webmcp.ToolDescriptor) webmcp.ToolDescriptor {
	descriptor.InputSchema = append(json.RawMessage(nil), descriptor.InputSchema...)
	descriptor.Annotations.Raw = append(json.RawMessage(nil), descriptor.Annotations.Raw...)
	return descriptor
}

func browserConversationToolRefs(tools []webmcp.ToolDescriptor) []webmcp.ToolRef {
	if len(tools) == 0 {
		return nil
	}
	refs := make([]webmcp.ToolRef, 0, len(tools))
	for _, descriptor := range tools {
		if descriptor.Ref != "" {
			refs = append(refs, descriptor.Ref)
		}
	}
	return refs
}

func cloneScheduledAudioInputs(inputs []ScheduledAudioInput) []ScheduledAudioInput {
	if inputs == nil {
		return nil
	}
	result := make([]ScheduledAudioInput, len(inputs))
	for index, input := range inputs {
		result[index] = input
		result[index].PCM = append([]byte(nil), input.PCM...)
	}
	return result
}

func browserConversationStepByID(scenario BrowserConversationScenario, stepID string) *BrowserConversationStep {
	for index := range scenario.Steps {
		if scenario.Steps[index].ID == stepID {
			return &scenario.Steps[index]
		}
	}
	return nil
}

func browserConversationExpectedState(step *BrowserConversationStep) *BrowserStateTransition {
	if step == nil {
		return nil
	}
	if step.ExpectedState != nil {
		return step.ExpectedState
	}
	if step.Correction != nil {
		return &step.Correction.ExpectedState
	}
	return nil
}

func recordBrowserConversationValidator(run *BrowserConversationRun, validator BrowserConversationValidator) error {
	if run == nil {
		return errors.New("browser conversation run is nil")
	}
	if validator == nil {
		return run.RecordValidator(BrowserConversationValidatorVerdict{
			Version: BrowserConversationValidatorVersion,
			Status:  BrowserConversationValidatorNotRun,
			Passed:  false,
		})
	}
	candidate := run.Snapshot()
	candidate.Finalized = true
	candidate = SanitizeBrowserConversationResult(candidate)
	verdict, err := validator.ValidateBrowserConversation(candidate)
	if err != nil {
		fallback := BrowserConversationValidatorVerdict{
			Version: BrowserConversationValidatorVersion,
			Status:  BrowserConversationValidatorFail,
			Summary: "validator returned an error",
		}
		_ = run.RecordValidator(fallback)
		return err
	}
	verdict = sanitizeBrowserConversationVerdict(verdict)
	if verdict.Version == "" {
		verdict.Version = BrowserConversationValidatorVersion
	}
	if verdict.Status == "" {
		if verdict.Passed {
			verdict.Status = BrowserConversationValidatorPass
		} else {
			verdict.Status = BrowserConversationValidatorFail
		}
	}
	if verdict.Status == BrowserConversationValidatorPass && !verdict.Passed {
		_ = run.RecordValidator(BrowserConversationValidatorVerdict{
			Version: BrowserConversationValidatorVersion,
			Status:  BrowserConversationValidatorFail,
			Summary: "validator pass status contradicted passed=false",
		})
		return errors.New("validator pass status contradicted passed=false")
	}
	if verdict.Status == BrowserConversationValidatorFail && verdict.Passed {
		_ = run.RecordValidator(BrowserConversationValidatorVerdict{
			Version: BrowserConversationValidatorVersion,
			Status:  BrowserConversationValidatorFail,
			Summary: "validator fail status contradicted passed=true",
		})
		return errors.New("validator fail status contradicted passed=true")
	}
	if err := run.RecordValidator(verdict); err != nil {
		return err
	}
	if verdict.Status == BrowserConversationValidatorFail {
		return errors.New("validator returned a failed verdict")
	}
	return nil
}

func sanitizeBrowserConversationVerdict(verdict BrowserConversationValidatorVerdict) BrowserConversationValidatorVerdict {
	verdict.Version = safeBrowserConversationText(verdict.Version)
	verdict.Summary = safeBrowserConversationText(verdict.Summary)
	verdict.Checks = append([]BrowserConversationValidatorCheck(nil), verdict.Checks...)
	for index := range verdict.Checks {
		verdict.Checks[index].Name = safeBrowserConversationText(verdict.Checks[index].Name)
		verdict.Checks[index].Detail = safeBrowserConversationText(verdict.Checks[index].Detail)
	}
	return verdict
}

func browserConversationTurnsForStep(turns []BrowserConversationTurn, stepID string) (*BrowserConversationTurn, *BrowserConversationTurn) {
	var customer, assistant *BrowserConversationTurn
	for index := range turns {
		if turns[index].StepID != stepID {
			continue
		}
		switch turns[index].Direction {
		case BrowserConversationCustomerTurn:
			if customer == nil {
				customer = &turns[index]
			}
		case BrowserConversationAssistantTurn:
			if assistant == nil {
				assistant = &turns[index]
			}
		}
	}
	return customer, assistant
}

func browserConversationOracleForStep(oracles []BrowserConversationOracleSnapshot, stepID string, phase BrowserConversationOraclePhase) *BrowserConversationOracleSnapshot {
	var match *BrowserConversationOracleSnapshot
	for index := range oracles {
		if oracles[index].StepID == stepID && oracles[index].Phase == phase {
			match = &oracles[index]
		}
	}
	return match
}

func browserConversationTerminalInvokeForStep(calls []BrowserConversationBrokerCall, stepID string) *BrowserConversationBrokerCall {
	for _, call := range calls {
		if call.StepID == stepID &&
			call.Operation == BrowserConversationInvoke &&
			call.Terminal &&
			call.State == webmcp.InvocationCompleted &&
			call.ErrorCode == "" {
			candidate := call
			return &candidate
		}
	}
	return nil
}

func browserConversationJSONEqual(left, right json.RawMessage) bool {
	leftValue, leftOK := decodeBrowserConversationJSON(left)
	rightValue, rightOK := decodeBrowserConversationJSON(right)
	return leftOK && rightOK && reflect.DeepEqual(leftValue, rightValue)
}

func decodeBrowserConversationJSON(raw json.RawMessage) (any, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, false
	}
	return value, true
}
