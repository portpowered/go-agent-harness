package chrome

// Fixture, event-collection, and recording-evidence support for
// TestPinnedChromeWebMCPConversationalCustomerLive.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	looptranscript "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	browserconversation "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

type conversationalCustomerOracle struct {
	Page        string   `json:"page"`
	Ready       bool     `json:"ready"`
	Label       string   `json:"label"`
	Theme       string   `json:"theme"`
	Priority    string   `json:"priority"`
	Pending     bool     `json:"pending"`
	VisibleText string   `json:"visibleText"`
	Invocations []string `json:"invocations"`
}

type conversationalCustomerPageState struct {
	Page        string `json:"page"`
	Ready       bool   `json:"ready"`
	Label       string `json:"label"`
	Theme       string `json:"theme"`
	Priority    string `json:"priority"`
	Pending     bool   `json:"pending"`
	VisibleText string `json:"visibleText"`
}

type conversationalCustomerFixtureServer struct {
	server *httptest.Server
	mu     sync.Mutex
	oracle conversationalCustomerOracle
}

func newConversationalCustomerFixtureServer() *conversationalCustomerFixtureServer {
	fixture := &conversationalCustomerFixtureServer{oracle: conversationalCustomerOracle{
		Page: conversationalCustomerHomePage, Label: "unset", Theme: "default", Priority: "normal", VisibleText: "unset/default",
	}}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/", "/settings":
			if request.Method != http.MethodGet {
				writer.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			writer.Header().Set("Origin-Agent-Cluster", "?1")
			writer.Header().Set("Permissions-Policy", "tools=(self)")
			if _, err := writer.Write(conversationalCustomerFixtureHTML); err != nil {
				// The browser went away mid-response; the oracle observes that.
				return
			}
		case "/__test/conversational-state":
			fixture.handleOracle(writer, request)
		default:
			http.NotFound(writer, request)
		}
	}))
	return fixture
}

func (f *conversationalCustomerFixtureServer) Origin() string { return f.server.URL }

func (f *conversationalCustomerFixtureServer) URL(page string) string {
	if page == conversationalCustomerSettingsPage {
		return f.server.URL + "/settings"
	}
	return f.server.URL + "/"
}

func (f *conversationalCustomerFixtureServer) StateURL() string {
	return f.server.URL + "/__test/conversational-state"
}

func (f *conversationalCustomerFixtureServer) Close() {
	if f != nil && f.server != nil {
		f.server.Close()
	}
}

func (f *conversationalCustomerFixtureServer) handleOracle(writer http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch request.Method {
	case http.MethodGet:
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(f.oracle); err != nil {
			// The polling client went away; its next read reports the failure.
			return
		}
	case http.MethodPost:
		var oracle conversationalCustomerOracle
		if err := json.NewDecoder(io.LimitReader(request.Body, 64<<10)).Decode(&oracle); err != nil {
			http.Error(writer, "invalid oracle", http.StatusBadRequest)
			return
		}
		oracle.Invocations = append([]string(nil), oracle.Invocations...)
		f.oracle = oracle
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newConversationalCustomerScenario(homeURL, settingsURL string) browserconversation.BrowserConversationScenario {
	homeBefore := conversationalCustomerState(homeURL, conversationalCustomerHomePage, true, "unset", "default", "normal", false, "unset/default")
	labelAfter := conversationalCustomerState(homeURL, conversationalCustomerHomePage, true, conversationalCustomerLabel, "default", "normal", false, conversationalCustomerLabel+"/default")
	themeAfter := conversationalCustomerState(homeURL, conversationalCustomerHomePage, true, conversationalCustomerLabel, conversationalCustomerTheme, "normal", false, conversationalCustomerLabel+"/"+conversationalCustomerTheme)
	settingsBefore := conversationalCustomerState(settingsURL, conversationalCustomerSettingsPage, true, conversationalCustomerLabel, conversationalCustomerTheme, "normal", false, "normal")
	settingsAfter := conversationalCustomerState(settingsURL, conversationalCustomerSettingsPage, true, conversationalCustomerLabel, conversationalCustomerTheme, conversationalCustomerPriority, false, conversationalCustomerPriority)
	correctionBefore := conversationalCustomerState(homeURL, conversationalCustomerHomePage, true, conversationalCustomerLabel, conversationalCustomerTheme, conversationalCustomerPriority, false, conversationalCustomerLabel+"/"+conversationalCustomerTheme)
	correctionAfter := conversationalCustomerState(homeURL, conversationalCustomerHomePage, true, conversationalCustomerCorrected, conversationalCustomerTheme, conversationalCustomerPriority, false, conversationalCustomerCorrected+"/"+conversationalCustomerTheme)
	return browserconversation.BrowserConversationScenario{
		Version: browserconversation.BrowserConversationScenarioVersion,
		ID:      "canonical-webmcp-conversational-customer",
		Name:    "canonical WebMCP conversational customer",
		Fixture: browserconversation.BrowserConversationFixture{
			ID:          "declarative-conversational-customer",
			Pages:       []browserconversation.BrowserConversationPage{{ID: conversationalCustomerHomePage, URL: homeURL}, {ID: conversationalCustomerSettingsPage, URL: settingsURL}},
			InitialPage: conversationalCustomerHomePage,
		},
		RunTimeout: 10 * time.Minute,
		Steps: []browserconversation.BrowserConversationStep{
			{ID: "initial_action", Utterance: "Set the customer label to live alpha.", PageID: conversationalCustomerHomePage, ExpectedState: &browserconversation.BrowserStateTransition{PageID: conversationalCustomerHomePage, Before: homeBefore, After: labelAfter}, Deadline: 90 * time.Second},
			{ID: "second_action", Utterance: "Now set the customer theme to live dark.", PageID: conversationalCustomerHomePage, ExpectedState: &browserconversation.BrowserStateTransition{PageID: conversationalCustomerHomePage, Before: labelAfter, After: themeAfter}, Deadline: 90 * time.Second},
			{ID: "stale_recovery", Utterance: "Set the customer priority to high.", PageID: conversationalCustomerSettingsPage, ExpectedState: &browserconversation.BrowserStateTransition{PageID: conversationalCustomerSettingsPage, Before: settingsBefore, After: settingsAfter}, Navigation: &browserconversation.BrowserCustomerNavigation{FromPageID: conversationalCustomerHomePage, ToPageID: conversationalCustomerSettingsPage, URL: settingsURL}, Deadline: 120 * time.Second},
			{ID: "correction", Utterance: "Actually change the customer label to live corrected.", PageID: conversationalCustomerHomePage, Navigation: &browserconversation.BrowserCustomerNavigation{FromPageID: conversationalCustomerSettingsPage, ToPageID: conversationalCustomerHomePage, URL: homeURL}, Correction: &browserconversation.BrowserConversationCorrection{TargetStepID: "initial_action", ExpectedState: browserconversation.BrowserStateTransition{PageID: conversationalCustomerHomePage, Before: correctionBefore, After: correctionAfter}}, Deadline: 90 * time.Second},
			{ID: "interrupt", Utterance: "Hold this customer request while I decide.", PageID: conversationalCustomerHomePage, Interrupt: &browserconversation.BrowserConversationInterrupt{Trigger: browserconversation.BrowserInterruptOnInFlightInvocation, ToolName: "webmcp_customer_pending"}, Deadline: 90 * time.Second},
			{ID: "cancel", Utterance: "Stop and cancel that request.", PageID: conversationalCustomerHomePage, Cancel: &browserconversation.BrowserConversationCancelRequest{Reason: "customer explicitly stopped the pending request"}, Deadline: 90 * time.Second},
		},
		PostSession: browserconversation.BrowserConversationTabStateRequired{PageID: conversationalCustomerHomePage, MustRemainAlive: true, MustBeResponsive: true, MustAllowMutation: true},
	}
}

func conversationalCustomerState(_ string, page string, ready bool, label, theme, priority string, pending bool, visible string) json.RawMessage {
	state, err := json.Marshal(conversationalCustomerPageState{Page: page, Ready: ready, Label: label, Theme: theme, Priority: priority, Pending: pending, VisibleText: visible})
	if err != nil {
		return nil
	}
	return state
}

func conversationalCustomerSystemPrompt() string {
	return `You are operating a real declarative WebMCP customer fixture through the browser tools in this session. Follow each customer request in order. Use webmcp_list_tools to discover current page tools and use only the exact current tool_ref and a syntactically valid JSON object string in webmcp_invoke. Never invent, reuse, or receive tool references or encoded arguments out of band. After customer navigation, list tools again; if a stale_tool_ref error occurs, retain that failed attempt as evidence and retry only with a freshly listed reference. Perform the requested page mutation before speaking confirmation, and ground confirmation in the resulting page state. A customer interruption or stop request cancels in-flight work; never claim a canceled action completed.`
}

func conversationalCustomerModel() string {
	if value := strings.TrimSpace(os.Getenv(conversationalCustomerModelEnv)); value != "" {
		return value
	}
	return "gpt-realtime"
}

type conversationalCustomerEventCollector struct {
	mu      sync.Mutex
	events  []webmcp.BrowserEvent
	changed chan struct{}
}

func newConversationalCustomerEventCollector() *conversationalCustomerEventCollector {
	return &conversationalCustomerEventCollector{changed: make(chan struct{})}
}

func (c *conversationalCustomerEventCollector) consume(session webmcp.TargetSession) {
	for event := range session.Events() {
		c.mu.Lock()
		c.events = append(c.events, event)
		close(c.changed)
		c.changed = make(chan struct{})
		c.mu.Unlock()
	}
}

func (c *conversationalCustomerEventCollector) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *conversationalCustomerEventCollector) snapshot() []webmcp.BrowserEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]webmcp.BrowserEvent(nil), c.events...)
}

func (c *conversationalCustomerEventCollector) wait(ctx context.Context, start int, match func(webmcp.BrowserEvent) bool) (webmcp.BrowserEvent, error) {
	for {
		c.mu.Lock()
		for index := start; index < len(c.events); index++ {
			if match(c.events[index]) {
				event := c.events[index]
				c.mu.Unlock()
				return event, nil
			}
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return webmcp.BrowserEvent{}, ctx.Err()
		}
	}
}

type conversationalCustomerSessionLogEntry struct {
	TurnIndex int `json:"turn_index"`
	Input     struct {
		Text string `json:"text"`
	} `json:"input"`
	Response struct {
		Text     string `json:"text"`
		Complete bool   `json:"complete"`
	} `json:"response"`
}

func readConversationalCustomerSessionLog(path string) ([]conversationalCustomerSessionLogEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer closeAfterRead(file)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var entries []conversationalCustomerSessionLogEntry
	for scanner.Scan() {
		var entry conversationalCustomerSessionLogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

type conversationalCustomerProviderCall struct {
	Name      string
	Arguments string
	ToolRef   webmcp.ToolRef
	ToolName  string
	InputJSON string
	CallID    string
}

func readConversationalCustomerProviderCalls(path string) ([]conversationalCustomerProviderCall, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer closeAfterRead(file)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var calls []conversationalCustomerProviderCall
	for scanner.Scan() {
		var record looptranscript.Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, err
		}
		var event struct {
			Type      string          `json:"type"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Item      json.RawMessage `json:"item"`
		}
		if err := json.Unmarshal(record.Payload, &event); err != nil || event.Type != "response.function_call_arguments.done" {
			continue
		}
		arguments := conversationalCustomerFunctionCallArguments(event.Arguments)
		call := conversationalCustomerProviderCall{Name: event.Name, Arguments: arguments, CallID: event.CallID}
		if call.Name == "" && len(event.Item) > 0 {
			var item struct {
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			}
			if json.Unmarshal(event.Item, &item) == nil {
				call.Name = item.Name
				if call.CallID == "" {
					call.CallID = item.CallID
				}
			}
		}
		if call.Name == webmcp.InvokeToolName {
			call.ToolRef, call.InputJSON = conversationalCustomerInvokeArguments(arguments)
		}
		calls = append(calls, call)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return calls, nil
}

func conversationalCustomerFunctionCallArguments(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, `"`) {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err == nil {
			return decoded
		}
	}
	return string(raw)
}

// conversationalCustomerInvokeArguments preserves malformed input_json as
// the exact raw value. A failed outer function-call decode must remain a
// visible invalid attempt in the report instead of disappearing from the
// reconstructed provider trace.
func conversationalCustomerInvokeArguments(arguments string) (webmcp.ToolRef, string) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(arguments), &fields); err != nil || fields == nil {
		return "", arguments
	}
	var toolRef string
	if raw, ok := fields["tool_ref"]; ok {
		if err := json.Unmarshal(raw, &toolRef); err != nil {
			// A non-string tool_ref is reported as an absent reference.
			toolRef = ""
		}
	}
	rawInput, ok := fields["input_json"]
	if !ok {
		return webmcp.ToolRef(toolRef), ""
	}
	if strings.TrimSpace(string(rawInput)) == "null" {
		return webmcp.ToolRef(toolRef), string(rawInput)
	}
	var input string
	if err := json.Unmarshal(rawInput, &input); err == nil {
		return webmcp.ToolRef(toolRef), input
	}
	return webmcp.ToolRef(toolRef), string(rawInput)
}

func expectedStepTextForStep(scenario browserconversation.BrowserConversationScenario, stepID string) string {
	for _, step := range scenario.Steps {
		if step.ID == stepID {
			return step.Utterance
		}
	}
	return ""
}

func conversationalCustomerLogStepIDs(scenario browserconversation.BrowserConversationScenario, logs []conversationalCustomerSessionLogEntry) []string {
	stepIDs := make([]string, len(logs))
	nextStep := 0
	lastStep := ""
	for index, entry := range logs {
		text := strings.TrimSpace(entry.Input.Text)
		if text == "" {
			stepIDs[index] = lastStep
			continue
		}
		matched := -1
		for candidate := nextStep; candidate < len(scenario.Steps); candidate++ {
			if strings.EqualFold(strings.TrimSpace(scenario.Steps[candidate].Utterance), text) {
				matched = candidate
				break
			}
		}
		if matched >= 0 {
			nextStep = matched + 1
			lastStep = scenario.Steps[matched].ID
			stepIDs[index] = lastStep
			continue
		}
		// The canonical run sends the interruption utterance once to start the
		// pending tool and may send the same audio again as overlap. Preserve
		// that duplicate under the declared interruption step rather than
		// inventing a seventh scenario step.
		if repeated := conversationalCustomerRepeatedInterrupt(scenario, nextStep, text); repeated >= 0 {
			lastStep = scenario.Steps[repeated].ID
			stepIDs[index] = lastStep
			continue
		}
		if nextStep < len(scenario.Steps) {
			lastStep = scenario.Steps[nextStep].ID
			nextStep++
			stepIDs[index] = lastStep
		}
	}
	return stepIDs
}

// conversationalCustomerRepeatedInterrupt finds an already-consumed
// interruption step whose utterance was sent again, or returns -1.
func conversationalCustomerRepeatedInterrupt(scenario browserconversation.BrowserConversationScenario, nextStep int, text string) int {
	for candidate := 0; candidate < nextStep && candidate < len(scenario.Steps); candidate++ {
		if scenario.Steps[candidate].Interrupt != nil && strings.EqualFold(strings.TrimSpace(scenario.Steps[candidate].Utterance), text) {
			return candidate
		}
	}
	return -1
}

func assignConversationalCustomerTurnSequences(turns []browserconversation.BrowserConversationTurn, calls []browserconversation.BrowserConversationBrokerCall) {
	type bounds struct{ first, last uint64 }
	byStep := make(map[string]bounds)
	for _, call := range calls {
		if call.StepID == "" || call.Sequence == 0 {
			continue
		}
		current := byStep[call.StepID]
		if current.first == 0 || call.Sequence < current.first {
			current.first = call.Sequence
		}
		if call.Sequence > current.last {
			current.last = call.Sequence
		}
		byStep[call.StepID] = current
	}
	next := uint64(len(calls) + 1)
	for index := range turns {
		current, ok := byStep[turns[index].StepID]
		if !ok {
			turns[index].Sequence = next
			next++
			continue
		}
		if turns[index].Direction == browserconversation.BrowserConversationCustomerTurn {
			turns[index].Sequence = current.first
		} else {
			turns[index].Sequence = current.last + 1
		}
	}
}

func conversationalCustomerInvocationState(event webmcp.BrowserEvent) webmcp.InvocationState {
	switch strings.ToLower(strings.TrimSpace(event.Status)) {
	case "completed":
		return webmcp.InvocationCompleted
	case "canceled", "cancelled":
		return webmcp.InvocationCanceled
	case "timed_out", "timeout", "timedout":
		return webmcp.InvocationTimedOut
	default:
		return webmcp.InvocationError
	}
}

func conversationalCustomerJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	if json.Valid(value) {
		return append(json.RawMessage(nil), value...)
	}
	encoded, err := json.Marshal(string(value))
	if err != nil {
		return nil
	}
	return encoded
}

func conversationalCustomerCurrentToolRefs(
	names map[webmcp.ToolRef]string,
	refsByGeneration map[uint64]map[webmcp.ToolRef]struct{},
	stepID string,
	navigationByStep map[string]webmcp.BrowserEvent,
	firstGeneration uint64,
) ([]webmcp.ToolRef, uint64) {
	generation := firstGeneration
	if navigation, ok := navigationByStep[stepID]; ok && navigation.Generation != 0 {
		generation = navigation.Generation
	}
	set := refsByGeneration[generation]
	refs := make([]webmcp.ToolRef, 0, len(set))
	for ref := range set {
		if names[ref] != "" {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		for ref, name := range names {
			if name == "" {
				continue
			}
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(left, right int) bool { return refs[left] < refs[right] })
	return refs, generation
}
