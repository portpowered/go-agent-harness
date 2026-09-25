package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	crossProcessInvocationValue = "cross-process"
	crossProcessWatchTimeout    = 8 * time.Second
	crossProcessActionGap       = 350 * time.Millisecond
)

type crossProcessWatchEvent struct {
	Version      string `json:"version"`
	Type         string `json:"type"`
	Sequence     uint64 `json:"sequence"`
	BrowserID    string `json:"browser_id,omitempty"`
	TargetID     string `json:"target_id,omitempty"`
	Generation   uint64 `json:"generation,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
	ToolRef      string `json:"tool_ref,omitempty"`
	State        string `json:"state,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type crossProcessWatchData struct {
	Status string                   `json:"status"`
	Events []crossProcessWatchEvent `json:"events"`
}

type crossProcessInvocationData struct {
	InvocationID string          `json:"invocation_id"`
	ToolRef      string          `json:"tool_ref"`
	Status       string          `json:"status"`
	Output       json.RawMessage `json:"output"`
}

type crossProcessTabData struct {
	BrowserID string `json:"browser_id"`
	TargetID  string `json:"target_id"`
	Type      string `json:"type"`
	Origin    string `json:"origin"`
	Eligible  bool   `json:"eligible"`
}

type crossProcessTabsData struct {
	Tabs []crossProcessTabData `json:"tabs"`
}

type crossProcessEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error json.RawMessage `json:"error"`
}

type crossProcessProcessReport struct {
	Started  bool   `json:"started"`
	Finished bool   `json:"finished"`
	Exit     string `json:"exit,omitempty"`
}

type crossProcessTargetCheck struct {
	Phase      string                `json:"phase"`
	Present    bool                  `json:"present"`
	Attached   bool                  `json:"attached"`
	Responsive bool                  `json:"responsive"`
	TargetID   string                `json:"targetID"`
	Type       string                `json:"type,omitempty"`
	URL        string                `json:"url,omitempty"`
	State      crossProcessPageState `json:"state,omitempty"`
}

type crossProcessProbeReport struct {
	ObservedAt            string                     `json:"observedAt"`
	Endpoint              string                     `json:"endpoint"`
	FixtureURL            string                     `json:"fixtureURL"`
	TargetID              string                     `json:"targetID"`
	RawTargetID           string                     `json:"rawTargetID"`
	BrowserID             string                     `json:"browserID"`
	InvocationID          string                     `json:"invocationID"`
	Watcher               crossProcessProcessReport  `json:"watcher"`
	Invoker               crossProcessProcessReport  `json:"invoker"`
	WatchStatus           string                     `json:"watchStatus"`
	WatchEvents           []crossProcessWatchEvent   `json:"watchEvents"`
	InvokerResult         crossProcessInvocationData `json:"invokerResult"`
	CDPEvents             []crossProcessCDPEvent     `json:"cdpEvents"`
	InitialOracle         crossProcessPageState      `json:"initialOracle"`
	AfterInvocationOracle crossProcessPageState      `json:"afterInvocationOracle"`
	AfterInvocationCDP    crossProcessPageState      `json:"afterInvocationCDP"`
	FinalOracle           crossProcessPageState      `json:"finalOracle"`
	TargetChecks          []crossProcessTargetCheck  `json:"targetChecks"`
	Cleanup               string                     `json:"cleanup"`
	Verdict               string                     `json:"verdict"`
}

type crossProcessCommand struct {
	command []string
	cmd     *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	done    chan struct{}

	mu      sync.Mutex
	waitErr error
}

func startCrossProcessCommand(ctx context.Context, command []string) (*crossProcessCommand, error) {
	if len(command) == 0 || command[0] == "" {
		return nil, errors.New("cross-process probe command is empty")
	}
	process := &crossProcessCommand{
		command: append([]string(nil), command...),
		done:    make(chan struct{}),
	}
	process.cmd = exec.CommandContext(ctx, command[0], command[1:]...)
	process.cmd.Stdout = &process.stdout
	process.cmd.Stderr = &process.stderr
	if err := process.cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", command[0], err)
	}
	go func() {
		err := process.cmd.Wait()
		process.mu.Lock()
		process.waitErr = err
		process.mu.Unlock()
		close(process.done)
	}()
	return process, nil
}

func (p *crossProcessCommand) wait(ctx context.Context) error {
	if p == nil {
		return errors.New("cross-process probe command is nil")
	}
	select {
	case <-p.done:
		p.mu.Lock()
		err := p.waitErr
		p.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *crossProcessCommand) stop() {
	if p == nil || p.cmd == nil {
		return
	}
	select {
	case <-p.done:
		return
	default:
	}
	if p.cmd.Process != nil {
		killProbeProcess(p.cmd.Process)
	}
	<-p.done
}

func (p *crossProcessCommand) report() crossProcessProcessReport {
	if p == nil {
		return crossProcessProcessReport{}
	}
	result := crossProcessProcessReport{Started: p.cmd != nil}
	select {
	case <-p.done:
		result.Finished = true
	default:
	}
	p.mu.Lock()
	if p.waitErr != nil {
		result.Exit = p.waitErr.Error()
	}
	p.mu.Unlock()
	return result
}

func (p *crossProcessCommand) stdoutText() string {
	if p == nil {
		return ""
	}
	return p.stdout.String()
}

func (p *crossProcessCommand) stderrText() string {
	if p == nil {
		return ""
	}
	return p.stderr.String()
}

func directAgentCommand(agentBinary, configDir string, args ...string) []string {
	command := []string{agentBinary, "--config-dir", configDir}
	return append(command, args...)
}

func writeCrossProcessConfig(configDir, httpEndpoint string) error {
	contents := "browser:\n  connection:\n    cdp_url: " + httpEndpoint + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(contents), 0o600); err != nil {
		return fmt.Errorf("write temporary agent config: %w", err)
	}
	return nil
}

func findCrossProcessTab(tabs crossProcessTabsData, fixtureURL string) (crossProcessTabData, error) {
	parsed, err := url.Parse(fixtureURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return crossProcessTabData{}, fmt.Errorf("fixture URL is invalid: %q", fixtureURL)
	}
	wantOrigin := parsed.Scheme + "://" + parsed.Host
	var matches []crossProcessTabData
	for _, tab := range tabs.Tabs {
		if tab.Type == "page" && tab.Origin == wantOrigin && tab.Eligible {
			matches = append(matches, tab)
		}
	}
	if len(matches) != 1 {
		return crossProcessTabData{}, fmt.Errorf("eligible fixture tabs = %d, want exactly one: %+v", len(matches), tabs.Tabs)
	}
	if matches[0].BrowserID == "" || matches[0].TargetID == "" {
		return crossProcessTabData{}, fmt.Errorf("eligible fixture tab has incomplete identity: %+v", matches[0])
	}
	return matches[0], nil
}

func parseCrossProcessEnvelope(output string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(output))
	var envelope crossProcessEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("decode CLI result: %w; output=%q", err, trimWatchProcessOutput(output))
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("CLI result contains more than one JSON value")
	}
	if !envelope.OK {
		return fmt.Errorf("CLI result was not successful: %s", trimWatchProcessOutput(string(envelope.Error)))
	}
	if len(envelope.Data) == 0 {
		return errors.New("CLI result omitted data")
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("decode CLI result data: %w", err)
	}
	return nil
}

func checkCrossProcessTarget(ctx context.Context, httpEndpoint, targetID, phase string, pageContext context.Context, fixtureURL string) (crossProcessTargetCheck, error) {
	info, err := readCrossProcessTarget(ctx, httpEndpoint, targetID)
	if err != nil {
		return crossProcessTargetCheck{}, err
	}
	check := crossProcessTargetCheck{
		Phase:    phase,
		Present:  true,
		Attached: info.Attached,
		TargetID: info.ID,
		Type:     info.Type,
		URL:      info.URL,
	}
	if info.Type != "page" || info.URL != fixtureURL {
		return crossProcessTargetCheck{}, fmt.Errorf("%s target = %+v, want page %s", phase, info, fixtureURL)
	}
	if pageContext != nil {
		state, stateErr := readCrossProcessPageState(pageContext)
		if stateErr != nil {
			return crossProcessTargetCheck{}, fmt.Errorf("%s target is not responsive: %w", phase, stateErr)
		}
		check.State = state
		check.Responsive = state.Ready && state.VisibleText != ""
		if !check.Responsive {
			return crossProcessTargetCheck{}, fmt.Errorf("%s target state is not ready: %+v", phase, state)
		}
	}
	return check, nil
}

func parseAndValidateCrossProcessWatch(output, targetID string) (crossProcessWatchData, string, error) {
	var watch crossProcessWatchData
	if err := parseCrossProcessEnvelope(output, &watch); err != nil {
		return crossProcessWatchData{}, "", err
	}
	browserID, err := validateCrossProcessWatch(watch, targetID)
	if err != nil {
		return crossProcessWatchData{}, "", err
	}
	return watch, browserID, nil
}

func validateCrossProcessWatch(watch crossProcessWatchData, targetID string) (string, error) {
	if watch.Status != "canceled" {
		return "", fmt.Errorf("watch status = %q, want canceled after bounded live run", watch.Status)
	}
	wantTypes := []string{
		"selected",
		"catalog_changed",
		"catalog_changed",
		"invocation_created",
		"invocation_terminal",
		"catalog_changed",
	}
	if len(watch.Events) != len(wantTypes) {
		return "", fmt.Errorf("watch event count = %d, want %d: %+v", len(watch.Events), len(wantTypes), watch.Events)
	}
	browserID := ""
	generation := uint64(0)
	for index, event := range watch.Events {
		if event.Type != wantTypes[index] {
			return "", fmt.Errorf("watch event %d type = %q, want %q: %+v", index, event.Type, wantTypes[index], watch.Events)
		}
		if event.Sequence == 0 || (index > 0 && event.Sequence <= watch.Events[index-1].Sequence) {
			return "", fmt.Errorf("watch sequence at %d is not strictly increasing: %+v", index, watch.Events)
		}
		if event.BrowserID == "" || event.TargetID != targetID || event.Generation == 0 {
			return "", fmt.Errorf("watch event %d identity = %+v, want browser, target %q, generation", index, event, targetID)
		}
		if browserID == "" {
			browserID = event.BrowserID
		} else if event.BrowserID != browserID {
			return "", fmt.Errorf("watch event %d browser ID = %q, want %q", index, event.BrowserID, browserID)
		}
		if generation == 0 {
			generation = event.Generation
		} else if event.Generation != generation {
			return "", fmt.Errorf("watch event %d generation = %d, want %d", index, event.Generation, generation)
		}
	}
	if watch.Events[1].Reason != "tools_added" || watch.Events[2].Reason != "tools_added" || watch.Events[5].Reason != "tools_removed" {
		return "", fmt.Errorf("catalog change reasons = %q/%q/%q, want tools_added/tools_added/tools_removed", watch.Events[1].Reason, watch.Events[2].Reason, watch.Events[5].Reason)
	}
	created, terminal := watch.Events[3], watch.Events[4]
	if created.InvocationID == "" || created.InvocationID != terminal.InvocationID {
		return "", fmt.Errorf("invocation IDs = %q/%q, want one non-empty ID", created.InvocationID, terminal.InvocationID)
	}
	if created.ToolRef == "" || created.ToolRef != terminal.ToolRef {
		return "", fmt.Errorf("invocation refs = %q/%q, want one resolved ref", created.ToolRef, terminal.ToolRef)
	}
	if created.State != "dispatched" || terminal.State != "completed" {
		return "", fmt.Errorf("invocation states = %q/%q, want dispatched/completed", created.State, terminal.State)
	}
	return browserID, nil
}

func containsCrossProcessTool(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
