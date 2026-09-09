// Command headless-session is an executable consumer of the public runtime
// session contracts. It intentionally has no flags: all scenario inputs are a
// single, explicit JSON object on stdin.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
)

const (
	reportSchema        = "audio-runtime-c27-headless-session-consumer/v1"
	historyOracleMarker = "history oracle:"
	closeAttempts       = 2
)

type config struct {
	Scenario string `json:"scenario"`
	Instance string `json:"instance,omitempty"`

	StoreDirectory      string `json:"store_directory,omitempty"`
	WorkspaceDirectory  string `json:"workspace_directory,omitempty"`
	StoreDirectoryA     string `json:"store_directory_a,omitempty"`
	WorkspaceDirectoryA string `json:"workspace_directory_a,omitempty"`
	StoreDirectoryB     string `json:"store_directory_b,omitempty"`
	WorkspaceDirectoryB string `json:"workspace_directory_b,omitempty"`

	SessionID       string          `json:"session_id,omitempty"`
	Input           string          `json:"input,omitempty"`
	ExpectedHistory []messageRecord `json:"expected_history,omitempty"`
	RequireHistory  bool            `json:"require_history,omitempty"`
}

type messageRecord struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type requestReport struct {
	Instance      string          `json:"instance"`
	Messages      []messageRecord `json:"messages"`
	HistorySHA256 string          `json:"history_sha256"`
	Response      string          `json:"response"`
	Call          int             `json:"call"`
}

type streamReport struct {
	Types          []string `json:"types,omitempty"`
	Text           string   `json:"text,omitempty"`
	TerminalSeen   bool     `json:"terminal_seen"`
	TerminalSource string   `json:"terminal_source,omitempty"`
	Outcome        string   `json:"outcome"`
	Error          string   `json:"error,omitempty"`
	Partial        bool     `json:"partial"`
	CloseCalls     int      `json:"close_calls"`
}

type historyReport struct {
	Present bool            `json:"present"`
	Items   []messageRecord `json:"items,omitempty"`
	SHA256  string          `json:"sha256,omitempty"`
}

type turnReport struct {
	Label                    string          `json:"label,omitempty"`
	SessionID                string          `json:"session_id,omitempty"`
	Input                    string          `json:"input"`
	ExpectedRequest          []messageRecord `json:"expected_request,omitempty"`
	ProviderRequests         []requestReport `json:"provider_requests,omitempty"`
	Stream                   streamReport    `json:"stream"`
	PersistedHistory         historyReport   `json:"persisted_history"`
	LoadedHistory            historyReport   `json:"loaded_history"`
	ListedSessionIDs         []string        `json:"listed_session_ids,omitempty"`
	LatestSessionID          string          `json:"latest_session_id,omitempty"`
	ConstructorProviderCalls int             `json:"constructor_provider_calls"`
	ProviderCalls            int             `json:"provider_calls"`
	StreamCloseCalls         int             `json:"stream_close_calls"`
	HandleCloseCalls         int             `json:"handle_close_calls"`
	Saved                    bool            `json:"saved"`
	Errors                   []string        `json:"errors,omitempty"`
}

type controlReport struct {
	FailedClosed bool   `json:"failed_closed"`
	Diagnostic   string `json:"diagnostic,omitempty"`
}

type isolationReport struct {
	EntryOrder       []string    `json:"entry_order"`
	BothInFlight     bool        `json:"both_in_flight"`
	PartialAObserved bool        `json:"partial_a_observed"`
	InitialA         *turnReport `json:"initial_a,omitempty"`
	InitialB         *turnReport `json:"initial_b,omitempty"`
	CanceledA        *turnReport `json:"canceled_a,omitempty"`
	CompletedB       *turnReport `json:"completed_b,omitempty"`
	RestartA         *turnReport `json:"restart_a,omitempty"`
	RestartB         *turnReport `json:"restart_b,omitempty"`
	ProviderJoined   bool        `json:"provider_joined"`
}

type report struct {
	Schema                   string                   `json:"schema"`
	Scenario                 string                   `json:"scenario"`
	Status                   string                   `json:"status"`
	Instance                 string                   `json:"instance,omitempty"`
	SessionID                string                   `json:"session_id,omitempty"`
	Error                    string                   `json:"error,omitempty"`
	ConstructorProviderCalls int                      `json:"constructor_provider_calls"`
	ProviderCalls            int                      `json:"provider_calls"`
	Turn                     *turnReport              `json:"turn,omitempty"`
	Isolation                *isolationReport         `json:"isolation,omitempty"`
	BeforeCancel             *turnReport              `json:"before_cancel,omitempty"`
	DuringCancel             *turnReport              `json:"during_cancel,omitempty"`
	Controls                 map[string]controlReport `json:"controls,omitempty"`
	Notes                    []string                 `json:"notes,omitempty"`
}

type providerOptions struct {
	Entry   chan<- string
	Release <-chan struct{}
	Block   bool
	Partial bool
}

// deterministicProvider is deliberately stateful only within one injected
// provider instance. It records the exact request seen by the runtime and
// derives the answer from that request, making a canned PASS impossible.
type deterministicProvider struct {
	mu       sync.Mutex
	instance string
	options  providerOptions
	calls    int
	requests []requestReport
	done     sync.WaitGroup
}

func newDeterministicProvider(instance string, options providerOptions) *deterministicProvider {
	return &deterministicProvider{instance: instance, options: options}
}

func (p *deterministicProvider) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	response := p.observe(req)
	if err := p.wait(ctx); err != nil {
		return messages.InferenceResult{}, err
	}
	return messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, response)}, nil
}

func (p *deterministicProvider) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	response := p.observe(req)
	out := make(chan messages.StreamMessage, 8)
	p.done.Add(1)
	go func() {
		defer p.done.Done()
		defer close(out)
		if p.options.Entry != nil {
			select {
			case p.options.Entry <- p.instance:
			case <-ctx.Done():
				return
			}
		}
		if !sendStream(ctx, out, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}) {
			return
		}
		if !sendStream(ctx, out, messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()}) {
			return
		}

		first := response
		if p.options.Partial && len(first) > 16 {
			first = first[:16]
		}
		if !p.options.Partial {
			first = ""
		}
		if first != "" && !sendStream(ctx, out, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(first)}) {
			return
		}
		if err := p.wait(ctx); err != nil {
			return
		}
		start := len(first)
		for start < len(response) {
			end := start + 16
			if end > len(response) {
				end = len(response)
			}
			if !sendStream(ctx, out, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(response[start:end])}) {
				return
			}
			start = end
		}
		if !sendStream(ctx, out, messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()}) {
			return
		}
		sendStream(ctx, out, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	}()
	return out, nil
}

func sendStream(ctx context.Context, out chan<- messages.StreamMessage, msg messages.StreamMessage) bool {
	select {
	case out <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

func (p *deterministicProvider) wait(ctx context.Context) error {
	if !p.options.Block {
		return ctx.Err()
	}
	if p.options.Release == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case <-p.options.Release:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *deterministicProvider) observe(req messages.InferenceRequest) string {
	records := recordsFromMessages(req.Messages)
	fingerprint := historyFingerprint(records)
	lastUser := ""
	for _, item := range records {
		if item.Role == string(messages.RoleUser) {
			lastUser = item.Text
		}
	}
	p.mu.Lock()
	p.calls++
	call := p.calls
	response := fmt.Sprintf("provider=%s input=%q history=%s count=%d call=%d", p.instance, lastUser, fingerprint, len(records), call)
	p.requests = append(p.requests, requestReport{
		Instance: p.instance, Messages: append([]messageRecord(nil), records...),
		HistorySHA256: fingerprint, Response: response, Call: call,
	})
	p.mu.Unlock()
	return response
}

func (p *deterministicProvider) snapshot() ([]requestReport, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]requestReport, len(p.requests))
	copy(result, p.requests)
	for i := range result {
		result[i].Messages = append([]messageRecord(nil), result[i].Messages...)
	}
	return result, p.calls
}

func (p *deterministicProvider) waitDone(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		p.done.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

type runtimeInstance struct {
	service session.Service
	store   session.ManagedStore
}

func newRuntime(cfg config, provider messages.Inferencer, store session.SessionStore, admin session.ManagedStore) runtimeInstance {
	resolver := session.ResolverFunc(func(ctx context.Context, _ session.Request) (session.Resolution, error) {
		if err := ctx.Err(); err != nil {
			return session.Resolution{}, err
		}
		return session.Resolution{
			Store: store, WorkspaceDir: cfg.WorkspaceDirectory,
			SystemPromptResolved: true,
		}, nil
	})
	serviceInstance := sessionwire.NewService(sessionwire.Dependencies{
		Inferencer: provider, RelaxValidation: true,
		Resolver: resolver, Store: store,
	})
	return runtimeInstance{service: serviceInstance, store: admin}
}

func openFileStore(cfg config) (session.ManagedStore, error) {
	return sessionwire.NewFileStoreFactory().Open(session.FileStoreOptions{
		Directory: cfg.StoreDirectory, WorkspaceDirectory: cfg.WorkspaceDirectory,
	})
}

type noOpSaveStore struct{ inner session.SessionStore }

func (s noOpSaveStore) Load(ctx context.Context, id string) ([]session.Message, error) {
	return s.inner.Load(ctx, id)
}
func (s noOpSaveStore) Latest(ctx context.Context) (string, error) {
	return s.inner.Latest(ctx)
}
func (s noOpSaveStore) NewSessionID(ctx context.Context) (string, error) {
	return s.inner.NewSessionID(ctx)
}
func (s noOpSaveStore) Save(context.Context, string, []session.Message) error { return nil }

func runTurn(ctx context.Context, cfg config, runtimeInstance runtimeInstance, provider *deterministicProvider, expectedPrevious []messageRecord, save bool, requireSaved bool, label string) (turnReport, error) {
	result := turnReport{Label: label, Input: cfg.Input, ConstructorProviderCalls: 0}
	if cfg.SessionID != "" {
		loaded, err := runtimeInstance.store.Load(ctx, cfg.SessionID)
		if err != nil {
			result.Errors = append(result.Errors, "preload: "+err.Error())
		} else {
			result.LoadedHistory = makeHistoryReport(loaded, loaded != nil)
		}
	}

	request := session.Request{
		Input:     agentloop.ExecuteInput{Message: cfg.Input},
		SessionID: cfg.SessionID,
	}
	// Open must only compose the invocation; the provider's call count is
	// sampled immediately after it returns to prove construction is inert.
	handle, err := runtimeInstance.service.Open(ctx, request)
	if err != nil {
		result.Errors = append(result.Errors, "open: "+err.Error())
		result.ProviderRequests, result.ProviderCalls = provider.snapshot()
		return result, errors.Join(errorFromStrings(result.Errors)...)
	}
	result.SessionID = handle.SessionID()
	result.ConstructorProviderCalls = providerCount(provider)

	stream, err := handle.Stream(ctx, agentloop.ExecuteInput{Message: cfg.Input})
	if err != nil {
		result.Errors = append(result.Errors, "stream: "+err.Error())
	} else {
		result.Stream = collectStream(stream)
		if closeErr := closeStreamTwice(stream, &result.StreamCloseCalls); closeErr != nil {
			result.Errors = append(result.Errors, closeErr.Error())
		}
	}

	if err == nil && result.Stream.Error == "" && save {
		if saveErr := handle.Save(); saveErr != nil {
			result.Errors = append(result.Errors, "save: "+saveErr.Error())
		} else {
			result.Saved = true
		}
	}
	if closeErr := closeHandleTwice(handle, &result.HandleCloseCalls); closeErr != nil {
		result.Errors = append(result.Errors, closeErr.Error())
	}

	result.ProviderRequests, result.ProviderCalls = provider.snapshot()
	if len(result.ProviderRequests) > 0 {
		result.ExpectedRequest = append([]messageRecord(nil), expectedPrevious...)
		result.ExpectedRequest = append(result.ExpectedRequest, messageRecord{Role: string(messages.RoleUser), Text: cfg.Input})
		if cfg.RequireHistory || len(expectedPrevious) > 0 {
			actual := result.ProviderRequests[0].Messages
			if !sameRecords(actual, result.ExpectedRequest) {
				result.Errors = append(result.Errors, fmt.Sprintf("history oracle: provider observed %s, expected %s", formatRecords(actual), formatRecords(result.ExpectedRequest)))
			}
		}
		if result.Stream.Error == "" && result.Stream.Text != result.ProviderRequests[0].Response {
			result.Errors = append(result.Errors, fmt.Sprintf("stream oracle: observed %q, provider response %q", result.Stream.Text, result.ProviderRequests[0].Response))
		}
	}

	if result.SessionID != "" {
		loaded, loadErr := runtimeInstance.store.Load(ctx, result.SessionID)
		if loadErr != nil {
			result.Errors = append(result.Errors, "load after turn: "+loadErr.Error())
		} else {
			result.PersistedHistory = makeHistoryReport(loaded, loaded != nil)
		}
		infos, listErr := runtimeInstance.store.List(ctx, session.SessionListOptions{Limit: session.DefaultSessionListLimit})
		if listErr != nil {
			result.Errors = append(result.Errors, "list: "+listErr.Error())
		} else {
			for _, info := range infos {
				result.ListedSessionIDs = append(result.ListedSessionIDs, info.ID)
			}
		}
		latest, latestErr := runtimeInstance.store.Latest(ctx)
		if latestErr != nil {
			result.Errors = append(result.Errors, "latest: "+latestErr.Error())
		} else {
			result.LatestSessionID = latest
		}
	}
	if requireSaved && (!result.PersistedHistory.Present || len(result.PersistedHistory.Items) == 0) {
		result.Errors = append(result.Errors, "persistence oracle: Save did not create readable history")
	}
	return result, errors.Join(errorFromStrings(result.Errors)...)
}

func closeTwice(name string, closeFunc func() error, calls *int) error {
	var closeErrors []error
	for attempt := 1; attempt <= closeAttempts; attempt++ {
		*calls = *calls + 1
		if err := closeFunc(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("%s close #%d: %w", name, attempt, err))
		}
	}
	return errors.Join(closeErrors...)
}

func closeStreamTwice(stream agentloop.Stream, calls *int) error {
	if stream == nil {
		return nil
	}
	return closeTwice("stream", stream.Close, calls)
}

func closeHandleTwice(handle session.SessionHandle, calls *int) error {
	if handle == nil {
		return nil
	}
	return closeTwice("handle", handle.Close, calls)
}

func providerCount(provider *deterministicProvider) int {
	_, count := provider.snapshot()
	return count
}

func collectStream(stream agentloop.Stream) streamReport {
	return collectStreamObserved(stream, nil)
}

func collectStreamObserved(stream agentloop.Stream, onTextDelta func()) streamReport {
	result := streamReport{}
	for stream.HasNext() {
		msg := stream.Response()
		result.Types = append(result.Types, string(msg.Type))
		switch msg.Type {
		case messages.StreamTypeTextDelta:
			if value, ok := msg.Value.(*messages.TextDeltaValue); ok {
				result.Text += value.Content
				if onTextDelta != nil {
					onTextDelta()
				}
			}
		case messages.StreamTypeMessageEnd:
			result.TerminalSeen = true
			if value, ok := msg.Value.(*messages.MessageEndValue); ok {
				result.TerminalSource = string(messages.MessageEndTerminalSource(value))
			}
		}
	}
	outcome := stream.Outcome()
	result.Outcome = string(outcome.Status)
	result.Partial = outcome.Partial
	if outcome.Err != nil {
		result.Error = outcome.Err.Error()
	} else if stream.Err() != nil {
		result.Error = stream.Err().Error()
	}
	return result
}

func makeHistoryReport(items []messages.Message, present bool) historyReport {
	records := recordsFromMessages(items)
	result := historyReport{Present: present, Items: records}
	if present {
		result.SHA256 = historyFingerprint(records)
	}
	return result
}

func recordsFromMessages(items []messages.Message) []messageRecord {
	result := make([]messageRecord, 0, len(items))
	for _, item := range items {
		result = append(result, messageRecord{Role: string(item.Role), Text: item.TextContent()})
	}
	return result
}

func historyFingerprint(items []messageRecord) string {
	digest := sha256.New()
	for _, item := range items {
		_, _ = io.WriteString(digest, item.Role)
		_, _ = io.WriteString(digest, "\x00")
		_, _ = io.WriteString(digest, item.Text)
		_, _ = io.WriteString(digest, "\n")
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func sameRecords(a, b []messageRecord) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func formatRecords(items []messageRecord) string {
	data, _ := json.Marshal(items)
	return string(data)
}

func errorFromStrings(items []string) []error {
	result := make([]error, 0, len(items))
	for _, item := range items {
		if item != "" {
			result = append(result, errors.New(item))
		}
	}
	return result
}

func runNormal(ctx context.Context, cfg config) (report, error) {
	admin, err := openFileStore(cfg)
	if err != nil {
		return report{}, err
	}
	provider := newDeterministicProvider(defaultInstance(cfg, "normal"), providerOptions{})
	runtimeInstance := newRuntime(cfg, provider, admin, admin)
	turn, turnErr := runTurn(ctx, cfg, runtimeInstance, provider, cfg.ExpectedHistory, true, true, cfg.Scenario)
	result := report{
		Schema: reportSchema, Scenario: cfg.Scenario, Status: "ok", Instance: provider.instance,
		SessionID: turn.SessionID, ConstructorProviderCalls: turn.ConstructorProviderCalls,
		ProviderCalls: turn.ProviderCalls, Turn: &turn,
	}
	if turnErr != nil {
		result.Status = "failed"
		result.Error = turnErr.Error()
	}
	return result, turnErr
}

func runCancellation(ctx context.Context, cfg config) (report, error) {
	admin, err := openFileStore(cfg)
	if err != nil {
		return report{}, err
	}
	beforeProvider := newDeterministicProvider("before-cancel", providerOptions{})
	beforeRuntime := newRuntime(cfg, beforeProvider, admin, admin)
	beforeCtx, cancelBefore := context.WithCancel(ctx)
	cancelBefore()
	beforeCfg := cfg
	beforeCfg.Input = "must-not-call-provider"
	before, beforeErr := runTurn(beforeCtx, beforeCfg, beforeRuntime, beforeProvider, nil, false, false, "before-cancel")
	if beforeErr == nil || beforeProviderCount(beforeProvider) != 0 || !strings.Contains(beforeErr.Error(), "context canceled") {
		beforeErr = errors.Join(beforeErr, errors.New("before-cancel oracle: canceled Open made provider calls or did not return context cancellation"))
	} else {
		// The rejected Open is the expected result of a pre-canceled context;
		// only the provider-call and persistence assertions determine probe health.
		beforeErr = nil
	}

	entry := make(chan string, 1)
	duringProvider := newDeterministicProvider("during-cancel", providerOptions{Entry: entry, Partial: true, Block: true})
	duringRuntime := newRuntime(cfg, duringProvider, admin, admin)
	duringCfg := cfg
	duringCfg.Input = "cancel-during-provider-wait"
	turn := turnReport{Label: "during-cancel", Input: duringCfg.Input}
	turnCtx, cancelTurn := context.WithCancel(ctx)
	handle, openErr := duringRuntime.service.Open(turnCtx, session.Request{Input: agentloop.ExecuteInput{Message: duringCfg.Input}})
	var stream agentloop.Stream
	if openErr == nil {
		turn.SessionID = handle.SessionID()
		stream, openErr = handle.Stream(turnCtx, agentloop.ExecuteInput{Message: duringCfg.Input})
	}
	streamDone := make(chan streamReport, 1)
	if openErr == nil {
		go func() { streamDone <- collectStream(stream) }()
		select {
		case <-entry:
		case <-time.After(5 * time.Second):
			openErr = errors.New("during-cancel oracle: provider entry was not observed")
		}
		cancelTurn()
		select {
		case turn.Stream = <-streamDone:
		case <-time.After(5 * time.Second):
			openErr = errors.Join(openErr, errors.New("during-cancel oracle: stream did not terminate after cancellation"))
		}
		if closeErr := closeStreamTwice(stream, &turn.StreamCloseCalls); closeErr != nil {
			openErr = errors.Join(openErr, closeErr)
		}
	} else {
		cancelTurn()
	}
	if handle != nil {
		if closeErr := closeHandleTwice(handle, &turn.HandleCloseCalls); closeErr != nil {
			openErr = errors.Join(openErr, closeErr)
		}
	}
	joined := providerJoined(duringProvider, ctx, 5*time.Second)
	if !joined {
		openErr = errors.Join(openErr, errors.New("during-cancel oracle: provider goroutine was not joined"))
	}
	turn.ProviderRequests, turn.ProviderCalls = duringProvider.snapshot()
	if duringProviderCount(duringProvider) != 1 {
		openErr = errors.Join(openErr, fmt.Errorf("during-cancel oracle: provider calls=%d, want 1", duringProviderCount(duringProvider)))
	}
	if turn.Stream.Outcome != string(agentloop.StreamCanceled) || turn.Stream.Error == "" || !turn.Stream.Partial {
		openErr = errors.Join(openErr, fmt.Errorf("during-cancel oracle: outcome=%q error=%q partial=%v", turn.Stream.Outcome, turn.Stream.Error, turn.Stream.Partial))
	}
	infos, listErr := admin.List(ctx, session.SessionListOptions{Limit: session.DefaultSessionListLimit})
	if listErr != nil {
		openErr = errors.Join(openErr, listErr)
	} else if len(infos) != 0 {
		openErr = errors.Join(openErr, fmt.Errorf("during-cancel oracle: canceled turn persisted %d sessions", len(infos)))
	}

	result := report{
		Schema: reportSchema, Scenario: cfg.Scenario, Status: "ok",
		BeforeCancel: &before, DuringCancel: &turn,
		ConstructorProviderCalls: before.ConstructorProviderCalls,
		ProviderCalls:            before.ProviderCalls + turn.ProviderCalls,
		Notes:                    []string{"cancellation was driven by context and entry channels; no sleep-based synchronization"},
	}
	if beforeErr != nil || openErr != nil {
		result.Status = "failed"
		result.Error = errors.Join(beforeErr, openErr).Error()
		return result, errors.Join(beforeErr, openErr)
	}
	return result, nil
}

func runIsolation(ctx context.Context, cfg config) (report, error) {
	cfgA := cfg
	cfgA.StoreDirectory, cfgA.WorkspaceDirectory = cfg.StoreDirectoryA, cfg.WorkspaceDirectoryA
	cfgB := cfg
	cfgB.StoreDirectory, cfgB.WorkspaceDirectory = cfg.StoreDirectoryB, cfg.WorkspaceDirectoryB
	storeA, err := openFileStore(cfgA)
	if err != nil {
		return report{}, err
	}
	storeB, err := openFileStore(cfgB)
	if err != nil {
		return report{}, err
	}
	providerA := newDeterministicProvider("A-initial", providerOptions{})
	providerB := newDeterministicProvider("B-initial", providerOptions{})
	initialCfgA, initialCfgB := cfgA, cfgB
	initialCfgA.Input, initialCfgB.Input = "A first history", "B first history"
	initialA, errA := runTurn(ctx, initialCfgA, newRuntime(cfgA, providerA, storeA, storeA), providerA, nil, true, true, "initial-a")
	initialB, errB := runTurn(ctx, initialCfgB, newRuntime(cfgB, providerB, storeB, storeB), providerB, nil, true, true, "initial-b")
	if err := errors.Join(errA, errB); err != nil {
		return report{Schema: reportSchema, Scenario: cfg.Scenario, Status: "failed", Isolation: &isolationReport{InitialA: &initialA, InitialB: &initialB}}, err
	}

	entry := make(chan string, 2)
	release := make(chan struct{})
	concurrentA := newDeterministicProvider("A-concurrent", providerOptions{Entry: entry, Release: release, Block: true, Partial: true})
	concurrentB := newDeterministicProvider("B-concurrent", providerOptions{Entry: entry, Release: release, Block: true, Partial: true})
	runtimeA := newRuntime(cfgA, concurrentA, storeA, storeA)
	runtimeB := newRuntime(cfgB, concurrentB, storeB, storeB)
	ctxA, cancelA := context.WithCancel(ctx)
	ctxB, cancelB := context.WithCancel(ctx)
	handleA, openAErr := runtimeA.service.Open(ctxA, session.Request{SessionID: initialA.SessionID})
	handleB, openBErr := runtimeB.service.Open(ctxB, session.Request{SessionID: initialB.SessionID})
	if openAErr != nil || openBErr != nil {
		cancelA()
		cancelB()
		var cleanupCalls int
		cleanupErr := errors.Join(closeHandleTwice(handleA, &cleanupCalls), closeHandleTwice(handleB, &cleanupCalls))
		return report{Schema: reportSchema, Scenario: cfg.Scenario, Status: "failed", Isolation: &isolationReport{InitialA: &initialA, InitialB: &initialB}}, errors.Join(openAErr, openBErr, cleanupErr)
	}
	streamA, streamAErr := handleA.Stream(ctxA, agentloop.ExecuteInput{Message: "A concurrent"})
	streamB, streamBErr := handleB.Stream(ctxB, agentloop.ExecuteInput{Message: "B concurrent"})
	if streamAErr != nil || streamBErr != nil {
		cancelA()
		cancelB()
		var cleanupCalls int
		cleanupErr := errors.Join(
			closeStreamTwice(streamA, &cleanupCalls),
			closeStreamTwice(streamB, &cleanupCalls),
			closeHandleTwice(handleA, &cleanupCalls),
			closeHandleTwice(handleB, &cleanupCalls),
		)
		return report{Schema: reportSchema, Scenario: cfg.Scenario, Status: "failed", Isolation: &isolationReport{InitialA: &initialA, InitialB: &initialB}}, errors.Join(streamAErr, streamBErr, cleanupErr)
	}
	resultA := make(chan streamReport, 1)
	resultB := make(chan streamReport, 1)
	partialAObserved := make(chan struct{})
	var partialAOnce sync.Once
	go func() {
		resultA <- collectStreamObserved(streamA, func() {
			partialAOnce.Do(func() { close(partialAObserved) })
		})
	}()
	go func() { resultB <- collectStream(streamB) }()
	entryOrder := make([]string, 0, 2)
	entryErr := error(nil)
	for len(entryOrder) < 2 {
		select {
		case name := <-entry:
			entryOrder = append(entryOrder, name)
		case <-time.After(5 * time.Second):
			entryErr = errors.New("isolation oracle: both provider entry signals were not observed")
			break
		}
		if entryErr != nil {
			break
		}
	}
	partialAReady := false
	if entryErr == nil {
		select {
		case <-partialAObserved:
			partialAReady = true
		case <-time.After(5 * time.Second):
			entryErr = errors.Join(entryErr, errors.New("isolation oracle: A partial delta was not observed before cancellation"))
		}
	}
	iso := isolationReport{
		EntryOrder: entryOrder, BothInFlight: len(entryOrder) == 2, PartialAObserved: partialAReady,
		InitialA: &initialA, InitialB: &initialB,
	}
	cancelA()
	var canceledStream streamReport
	select {
	case canceledStream = <-resultA:
	case <-time.After(5 * time.Second):
		entryErr = errors.Join(entryErr, errors.New("isolation oracle: A did not cancel and join"))
	}
	close(release)
	var completedStream streamReport
	select {
	case completedStream = <-resultB:
	case <-time.After(5 * time.Second):
		entryErr = errors.Join(entryErr, errors.New("isolation oracle: B did not complete after A cancellation"))
	}
	canceledA := turnReport{Label: "canceled-a", SessionID: initialA.SessionID, Input: "A concurrent", Stream: canceledStream}
	completedB := turnReport{Label: "completed-b", SessionID: initialB.SessionID, Input: "B concurrent", Stream: completedStream}
	entryErr = errors.Join(entryErr, closeStreamTwice(streamA, &canceledA.StreamCloseCalls))
	entryErr = errors.Join(entryErr, closeStreamTwice(streamB, &completedB.StreamCloseCalls))
	if saveErr := handleB.Save(); saveErr != nil {
		entryErr = errors.Join(entryErr, saveErr)
	} else {
		completedB.Saved = true
	}
	entryErr = errors.Join(entryErr, closeHandleTwice(handleA, &canceledA.HandleCloseCalls))
	entryErr = errors.Join(entryErr, closeHandleTwice(handleB, &completedB.HandleCloseCalls))
	cancelB()

	canceledA.ProviderRequests, canceledA.ProviderCalls = concurrentA.snapshot()
	completedB.ProviderRequests, completedB.ProviderCalls = concurrentB.snapshot()
	if persisted, loadErr := storeA.Load(ctx, initialA.SessionID); loadErr == nil {
		canceledA.PersistedHistory = makeHistoryReport(persisted, persisted != nil)
	} else {
		entryErr = errors.Join(entryErr, loadErr)
	}
	if persisted, loadErr := storeB.Load(ctx, initialB.SessionID); loadErr == nil {
		completedB.PersistedHistory = makeHistoryReport(persisted, persisted != nil)
	} else {
		entryErr = errors.Join(entryErr, loadErr)
	}
	iso.CanceledA, iso.CompletedB = &canceledA, &completedB
	if canceledStream.Outcome != string(agentloop.StreamCanceled) || !canceledStream.Partial {
		entryErr = errors.Join(entryErr, fmt.Errorf("isolation oracle: A outcome=%q partial=%v", canceledStream.Outcome, canceledStream.Partial))
	}
	if completedStream.Outcome != string(agentloop.StreamDrained) || !completedStream.TerminalSeen {
		entryErr = errors.Join(entryErr, fmt.Errorf("isolation oracle: B outcome=%q terminal=%v", completedStream.Outcome, completedStream.TerminalSeen))
	}
	if !canceledA.PersistedHistory.Present || !sameRecords(canceledA.PersistedHistory.Items, initialA.PersistedHistory.Items) {
		entryErr = errors.Join(entryErr, errors.New("isolation oracle: canceled A changed its store"))
	}
	if !completedB.PersistedHistory.Present || len(completedB.PersistedHistory.Items) != len(initialB.PersistedHistory.Items)+2 {
		entryErr = errors.Join(entryErr, errors.New("isolation oracle: B continuation was not saved"))
	}

	restartAProvider := newDeterministicProvider("A-restart", providerOptions{})
	restartBProvider := newDeterministicProvider("B-restart", providerOptions{})
	restartCfgA, restartCfgB := cfgA, cfgB
	restartCfgA.SessionID, restartCfgB.SessionID = initialA.SessionID, initialB.SessionID
	restartCfgA.Input, restartCfgB.Input = "A restart", "B restart"
	restartA, restartErrA := runTurn(ctx, restartCfgA, newRuntime(cfgA, restartAProvider, storeA, storeA), restartAProvider, initialA.PersistedHistory.Items, true, true, "restart-a")
	restartB, restartErrB := runTurn(ctx, restartCfgB, newRuntime(cfgB, restartBProvider, storeB, storeB), restartBProvider, completedB.PersistedHistory.Items, true, true, "restart-b")
	iso.RestartA, iso.RestartB = &restartA, &restartB
	iso.ProviderJoined = providerJoined(concurrentA, ctx, 5*time.Second) && providerJoined(concurrentB, ctx, 5*time.Second) && providerJoined(restartAProvider, ctx, 5*time.Second) && providerJoined(restartBProvider, ctx, 5*time.Second)
	if !iso.ProviderJoined {
		entryErr = errors.Join(entryErr, errors.New("isolation oracle: provider goroutine did not join"))
	}
	if strings.Contains(formatRecords(restartA.ProviderRequests[0].Messages), "B first") || strings.Contains(formatRecords(restartB.ProviderRequests[0].Messages), "A first") {
		entryErr = errors.Join(entryErr, errors.New("crossed A/B output/history: restart request crossed store boundary"))
	}
	if strings.Contains(restartA.Stream.Text, "B-") || strings.Contains(restartB.Stream.Text, "A-") {
		entryErr = errors.Join(entryErr, errors.New("crossed A/B output/history: response identity crossed service boundary"))
	}
	entryErr = errors.Join(entryErr, restartErrA, restartErrB)
	result := report{Schema: reportSchema, Scenario: cfg.Scenario, Status: "ok", Isolation: &iso, Notes: []string{"A and B used separate resolver, provider, store, workspace, and session IDs"}}
	if entryErr != nil {
		result.Status = "failed"
		result.Error = entryErr.Error()
	}
	return result, entryErr
}

func runNegativeControls(ctx context.Context, cfg config) (report, error) {
	controls := make(map[string]controlReport)
	baseStore, err := openFileStore(cfg)
	if err != nil {
		return report{}, err
	}
	initialProvider := newDeterministicProvider("negative-initial", providerOptions{})
	initialCfg := cfg
	initialCfg.Input = "negative saved history"
	initial, initialErr := runTurn(ctx, initialCfg, newRuntime(cfg, initialProvider, baseStore, baseStore), initialProvider, nil, true, true, "negative-setup")
	if initialErr != nil {
		return report{Schema: reportSchema, Scenario: cfg.Scenario, Status: "failed", Turn: &initial}, initialErr
	}
	expected := initial.PersistedHistory.Items

	if deleteErr := baseStore.Delete(ctx, initial.SessionID); deleteErr != nil {
		controls["missing_saved_file"] = controlReport{Diagnostic: "delete setup failed: " + deleteErr.Error()}
	} else {
		missingProvider := newDeterministicProvider("negative-missing", providerOptions{})
		missingCfg := cfg
		missingCfg.SessionID, missingCfg.Input, missingCfg.RequireHistory = initial.SessionID, "after missing file", true
		turn, runErr := runTurn(ctx, missingCfg, newRuntime(cfg, missingProvider, baseStore, baseStore), missingProvider, expected, false, false, "missing-saved-file")
		controls["missing_saved_file"] = closedControl(runErr, historyOracleMarker)
		if runErr == nil {
			controls["missing_saved_file"] = controlReport{Diagnostic: "missing saved file was accepted"}
		}
		_ = turn
	}

	noOpStoreAdmin, err := openFileStore(config{StoreDirectory: filepath.Join(cfg.StoreDirectory, "noop"), WorkspaceDirectory: filepath.Join(cfg.WorkspaceDirectory, "noop")})
	if err != nil {
		return report{}, err
	}
	noOpProvider := newDeterministicProvider("negative-noop", providerOptions{})
	noOpCfg := cfg
	noOpCfg.StoreDirectory = filepath.Join(cfg.StoreDirectory, "noop")
	noOpCfg.WorkspaceDirectory = filepath.Join(cfg.WorkspaceDirectory, "noop")
	noOpRuntime := newRuntime(noOpCfg, noOpProvider, noOpSaveStore{inner: noOpStoreAdmin}, noOpStoreAdmin)
	noOpCfg.Input = "no-op save history"
	noOpInitial, noOpInitialErr := runTurn(ctx, noOpCfg, noOpRuntime, noOpProvider, nil, true, false, "no-op-save-setup")
	if noOpInitialErr != nil {
		controls["no_op_save"] = controlReport{Diagnostic: "setup failed: " + noOpInitialErr.Error()}
	} else {
		noOpContinuationProvider := newDeterministicProvider("negative-noop-continuation", providerOptions{})
		noOpContinuationCfg := noOpCfg
		noOpContinuationCfg.SessionID, noOpContinuationCfg.Input, noOpContinuationCfg.RequireHistory = noOpInitial.SessionID, "after no-op save", true
		_, continuationErr := runTurn(ctx, noOpContinuationCfg, newRuntime(noOpCfg, noOpContinuationProvider, noOpStoreAdmin, noOpStoreAdmin), noOpContinuationProvider, []messageRecord{{Role: string(messages.RoleUser), Text: noOpCfg.Input}, {Role: string(messages.RoleAssistant), Text: noOpInitial.Stream.Text}}, false, false, "no-op-save")
		controls["no_op_save"] = closedControl(continuationErr, historyOracleMarker)
	}

	crossAConfig := cfg
	crossAConfig.StoreDirectory = filepath.Join(cfg.StoreDirectory, "cross-a")
	crossAConfig.WorkspaceDirectory = filepath.Join(cfg.WorkspaceDirectory, "cross-a")
	crossBConfig := cfg
	crossBConfig.StoreDirectory = filepath.Join(cfg.StoreDirectory, "cross-b")
	crossBConfig.WorkspaceDirectory = filepath.Join(cfg.WorkspaceDirectory, "cross-b")
	crossAStore, err := openFileStore(crossAConfig)
	if err != nil {
		return report{}, err
	}
	crossBStore, err := openFileStore(crossBConfig)
	if err != nil {
		return report{}, err
	}
	crossAP := newDeterministicProvider("A-cross", providerOptions{})
	crossBP := newDeterministicProvider("B-cross", providerOptions{})
	crossAConfig.Input, crossBConfig.Input = "A only", "B only"
	crossATurn, crossAErr := runTurn(ctx, crossAConfig, newRuntime(crossAConfig, crossAP, crossAStore, crossAStore), crossAP, nil, true, true, "cross-setup-a")
	crossBTurn, crossBErr := runTurn(ctx, crossBConfig, newRuntime(crossBConfig, crossBP, crossBStore, crossBStore), crossBP, nil, true, true, "cross-setup-b")
	if crossAErr != nil || crossBErr != nil {
		return report{Schema: reportSchema, Scenario: cfg.Scenario, Status: "failed", Controls: controls}, errors.Join(crossAErr, crossBErr)
	}
	crossOutputProvider := newDeterministicProvider("A-cross-output", providerOptions{})
	crossOutputCfg := crossAConfig
	crossOutputCfg.SessionID, crossOutputCfg.Input, crossOutputCfg.RequireHistory = crossATurn.SessionID, "A output checked against B", true
	_, crossOutputErr := runTurn(ctx, crossOutputCfg, newRuntime(crossAConfig, crossOutputProvider, crossAStore, crossAStore), crossOutputProvider, crossBTurn.PersistedHistory.Items, false, false, "crossed-output-history")
	controls["crossed_output_history"] = closedControl(crossOutputErr, historyOracleMarker)

	crossStoreProvider := newDeterministicProvider("B-cross-store", providerOptions{})
	crossStoreCfg := crossBConfig
	crossStoreCfg.SessionID, crossStoreCfg.Input, crossStoreCfg.RequireHistory = crossATurn.SessionID, "B store checked against A", true
	_, crossStoreErr := runTurn(ctx, crossStoreCfg, newRuntime(crossBConfig, crossStoreProvider, crossBStore, crossBStore), crossStoreProvider, crossATurn.PersistedHistory.Items, false, false, "crossed-store-history")
	controls["crossed_store_history"] = closedControl(crossStoreErr, historyOracleMarker)

	result := report{Schema: reportSchema, Scenario: cfg.Scenario, Status: "expected_negative_controls", Controls: controls, Notes: []string{"each control must fail closed with an observable history or cross-boundary diagnostic"}}
	var failed []string
	for name, control := range controls {
		if !control.FailedClosed {
			failed = append(failed, name+": "+control.Diagnostic)
		}
	}
	if len(failed) != 0 {
		result.Status = "failed"
		result.Error = strings.Join(failed, "; ")
		return result, errors.New(result.Error)
	}
	// Expected negative controls intentionally return a non-zero process status;
	// the verifier proves that each bad setup was rejected rather than treating
	// these probes as a passing runtime scenario.
	return result, errors.New("expected negative controls rejected all invalid setups")
}

func closedControl(err error, marker string) controlReport {
	if err == nil {
		return controlReport{FailedClosed: false, Diagnostic: "invalid setup was accepted"}
	}
	diagnostic := err.Error()
	if marker == "" {
		return controlReport{FailedClosed: false, Diagnostic: "control did not declare a diagnostic marker"}
	}
	if !strings.Contains(diagnostic, marker) {
		return controlReport{FailedClosed: false, Diagnostic: fmt.Sprintf("diagnostic %q did not contain required marker %q", diagnostic, marker)}
	}
	return controlReport{FailedClosed: true, Diagnostic: diagnostic}
}

func defaultInstance(cfg config, fallback string) string {
	if strings.TrimSpace(cfg.Instance) != "" {
		return cfg.Instance
	}
	return fallback
}

func beforeProviderCount(provider *deterministicProvider) int { return providerCount(provider) }
func duringProviderCount(provider *deterministicProvider) int { return providerCount(provider) }

func providerJoined(provider *deterministicProvider, parent context.Context, duration time.Duration) bool {
	ctx, cancel := context.WithTimeout(parent, duration)
	defer cancel()
	return provider.waitDone(ctx)
}

func validateConfig(cfg config) error {
	allowed := map[string]bool{"boundary": true, "lifecycle": true, "continuation": true, "isolation": true, "negative-controls": true, "cancellation": true, "cleanup": true}
	if !allowed[cfg.Scenario] {
		return fmt.Errorf("unsupported scenario %q", cfg.Scenario)
	}
	if cfg.Scenario == "isolation" {
		for name, path := range map[string]string{
			"store_directory_a": cfg.StoreDirectoryA, "workspace_directory_a": cfg.WorkspaceDirectoryA,
			"store_directory_b": cfg.StoreDirectoryB, "workspace_directory_b": cfg.WorkspaceDirectoryB,
		} {
			if err := validateExplicitPath(name, path); err != nil {
				return err
			}
		}
		if cfg.StoreDirectoryA == cfg.StoreDirectoryB || cfg.WorkspaceDirectoryA == cfg.WorkspaceDirectoryB {
			return errors.New("isolation paths must be distinct")
		}
		return nil
	}
	if err := validateExplicitPath("store_directory", cfg.StoreDirectory); err != nil {
		return err
	}
	if err := validateExplicitPath("workspace_directory", cfg.WorkspaceDirectory); err != nil {
		return err
	}
	if cfg.Scenario != "negative-controls" && strings.TrimSpace(cfg.Input) == "" {
		return errors.New("input is required")
	}
	if cfg.Scenario == "continuation" && strings.TrimSpace(cfg.SessionID) == "" {
		return errors.New("continuation requires an explicit session_id")
	}
	return nil
}

func validateExplicitPath(name, path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be a non-empty absolute path", name)
	}
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) || clean == "." {
		return fmt.Errorf("%s must identify an isolated path", name)
	}
	return nil
}

func readConfig() (config, error) {
	return readConfigFrom(os.Stdin)
}

func readConfigFrom(input io.Reader) (config, error) {
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	var cfg config
	if err := decoder.Decode(&cfg); err != nil {
		return config{}, fmt.Errorf("decode stdin JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return config{}, errors.New("stdin must contain exactly one JSON object")
		}
		return config{}, fmt.Errorf("trailing stdin JSON: %w", err)
	}
	if err := validateConfig(cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func run(ctx context.Context, cfg config) (report, error) {
	switch cfg.Scenario {
	case "isolation":
		return runIsolation(ctx, cfg)
	case "negative-controls":
		return runNegativeControls(ctx, cfg)
	case "cancellation":
		return runCancellation(ctx, cfg)
	default:
		return runNormal(ctx, cfg)
	}
}

func writeReport(result report) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(data))
	return err
}

func main() {
	cfg, err := readConfig()
	if err != nil {
		_ = writeReport(report{Schema: reportSchema, Scenario: "unknown", Status: "failed", Error: err.Error()})
		fmt.Fprintln(os.Stderr, "headless-session:", err)
		os.Exit(1)
	}
	result, runErr := run(context.Background(), cfg)
	if result.Schema == "" {
		result.Schema = reportSchema
	}
	if result.Scenario == "" {
		result.Scenario = cfg.Scenario
	}
	if result.Status == "" {
		result.Status = "failed"
	}
	if err := writeReport(result); err != nil {
		fmt.Fprintln(os.Stderr, "headless-session report:", err)
		os.Exit(1)
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "headless-session:", runErr)
		os.Exit(1)
	}
}
