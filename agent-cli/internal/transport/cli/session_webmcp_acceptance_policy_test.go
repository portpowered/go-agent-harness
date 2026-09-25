package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// sessionWebMCPWorkspaceBaseline is intentionally page-agnostic. The live
// acceptance test writes this exact policy to a workspace AGENTS.md and then
// asks the production agent to operate previously unknown WebMCP sites.
const sessionWebMCPWorkspaceBaseline = `# Browser workspace

When the customer identifies an already-open website by its purpose, use the WebMCP tab-listing tool, match the request against the safe title and origin returned by that tool, and select the one matching tab with its exact browser and target IDs. If there is exactly one clear match, select it without asking the customer to repeat the URL. Ask only when multiple tabs genuinely match. Listing tabs is discovery, not completion: do not stop, narrate, or return to the customer after finding a clear match; select it immediately and continue the requested page work in the same turn.

After selecting a page, discover its currently advertised page tools. Prefer structured read tools over screenshots. Read the current page state before changing it, carry out the customer's requested edits with the page's advertised tools, preserve customer-supplied text exactly, and read the resulting state back before claiming success. When the customer selects a different already-open page, list and select tabs again; never reuse a page tool from the previously selected tab. When the customer asks to navigate the current tab to a new website, use the current-tab navigation tool so the target identity and any active cast are preserved.
`

func TestSessionWebMCPWorkspaceBaselineIsPageAgnosticAndVerifiable(t *testing.T) {
	for _, want := range []string{
		"already-open website by its purpose",
		"safe title and origin",
		"exact browser and target IDs",
		"exactly one clear match",
		"Listing tabs is discovery, not completion",
		"continue the requested page work in the same turn",
		"discover its currently advertised page tools",
		"Read the current page state before changing it",
		"preserve customer-supplied text exactly",
		"read the resulting state back",
		"never reuse a page tool from the previously selected tab",
		"current-tab navigation tool",
		"any active cast are preserved",
	} {
		if !strings.Contains(sessionWebMCPWorkspaceBaseline, want) {
			t.Errorf("baseline AGENTS.md omits %q", want)
		}
	}
	for _, forbidden := range []string{
		"paperie", "margin", "greeting card", "document editor",
		"get_card_state", "set_card_message", "create_document", "update_document",
		"openai.chatgpt.site",
	} {
		if strings.Contains(strings.ToLower(sessionWebMCPWorkspaceBaseline), forbidden) {
			t.Errorf("baseline AGENTS.md leaks scenario-specific hint %q", forbidden)
		}
	}
}

// TestValidateSessionPageToolsSwitchVoiceObservationRejectsMisroutedRuns
// keeps the live voice trace oracle honest: a synthetic Cubecade->Margin->
// Cubecade trace passes, and each misrouted or incomplete variant is rejected.
func TestValidateSessionPageToolsSwitchVoiceObservationRejectsMisroutedRuns(t *testing.T) {
	cube := sessionPageToolsLiveTarget{BrowserID: "browser", TargetID: "cube"}
	margin := sessionPageToolsLiveTarget{BrowserID: "browser", TargetID: "margin"}
	surface := func(page []string) sessionPageToolsSwitchVoiceSurface {
		var tools []sessionPageToolsSwitchVoiceTool
		for _, name := range append(append([]string{"exec"}, webmcp.StableToolNames()...), page...) {
			tools = append(tools, sessionPageToolsSwitchVoiceTool{Name: name, Raw: json.RawMessage(`{"name":"` + name + `"}`)})
		}
		return sessionPageToolsSwitchVoiceSurface{Tools: tools}
	}
	valid := func() sessionPageToolsSwitchVoiceObservation {
		calls := []sessionPageToolsSwitchVoiceCall{
			{Name: webmcp.SelectTabToolName, Arguments: `{"browser_id":"browser","target_id":"cube"}`},
			{Name: ambiguousCubeStateTool, Arguments: `{}`},
			{Name: webmcp.SelectTabToolName, Arguments: `{"browser_id":"browser","target_id":"margin"}`},
			{Name: liveCreateDocumentToolName, Arguments: `{"title":"voice title","content":"voice content"}`},
			{Name: liveGetDocumentToolName, Arguments: `{"document_id":"doc-1"}`},
			{Name: webmcp.SelectTabToolName, Arguments: `{"browser_id":"browser","target_id":"cube"}`},
			{Name: ambiguousCubeStateTool, Arguments: `{}`},
		}
		outputs := make([]sessionPageToolsSwitchVoiceOutput, len(calls))
		for index := range calls {
			calls[index].Index, calls[index].ArgumentsAt = index*3, index*3+1
			calls[index].CallID = fmt.Sprintf("call-%d", index)
			data := json.RawMessage(`{}`)
			if calls[index].Name == liveCreateDocumentToolName {
				data = json.RawMessage(`{"document_id":"doc-1"}`)
			}
			outputs[index] = sessionPageToolsSwitchVoiceOutput{Index: index*3 + 2, CallID: calls[index].CallID, Envelope: webmcp.ToolResultEnvelope{OK: true, Data: data}}
		}
		return sessionPageToolsSwitchVoiceObservation{
			Provider: config.ProviderOpenAI, Model: sessionPageToolsSwitchVoiceModel, SessionCreated: 1,
			Surfaces:             []sessionPageToolsSwitchVoiceSurface{surface(liveCubecadePageTools()), surface(liveMarginPageTools()), surface(liveCubecadePageTools())},
			Calls:                calls,
			Outputs:              outputs,
			UserTranscripts:      []string{"read the cube", "switch to the document editor", "use the exact title", "switch back", "goodbye"},
			AssistantTranscripts: []string{"done"},
		}
	}
	documentID, err := validateSessionPageToolsSwitchVoiceObservation(valid(), cube, margin, "voice title", "voice content")
	if err != nil || documentID != "doc-1" {
		t.Fatalf("valid synthetic trace = (%q, %v), want doc-1", documentID, err)
	}
	tests := map[string]func(*sessionPageToolsSwitchVoiceObservation){
		"cube tool while Margin selected": func(o *sessionPageToolsSwitchVoiceObservation) { o.Calls[3].Name = ambiguousCubeStateTool },
		"cube moved":                      func(o *sessionPageToolsSwitchVoiceObservation) { o.Calls[1].Name = sessionAudioInterruptQueueTool },
		"inexact document": func(o *sessionPageToolsSwitchVoiceObservation) {
			o.Calls[3].Arguments = `{"title":"other","content":"voice content"}`
		},
		"readback of another document": func(o *sessionPageToolsSwitchVoiceObservation) { o.Calls[4].Arguments = `{"document_id":"doc-2"}` },
		"no return to Cubecade":        func(o *sessionPageToolsSwitchVoiceObservation) { o.Surfaces = o.Surfaces[:2] },
		"static definition changed": func(o *sessionPageToolsSwitchVoiceObservation) {
			o.Surfaces[2].Tools[0].Raw = json.RawMessage(`{"name":"exec","description":"changed"}`)
		},
		"failed tool output": func(o *sessionPageToolsSwitchVoiceObservation) { o.Outputs[2].Envelope.OK = false },
		"wrong model":        func(o *sessionPageToolsSwitchVoiceObservation) { o.Model = "other" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			observation := valid()
			mutate(&observation)
			if _, err := validateSessionPageToolsSwitchVoiceObservation(observation, cube, margin, "voice title", "voice content"); err == nil {
				t.Fatal("misrouted trace was accepted")
			}
		})
	}
}

// sessionPageToolsSwitchVoiceBaseNames returns the sorted static and stable
// (non-page) tool names advertised across every surface.
func sessionPageToolsSwitchVoiceBaseNames(surfaces []sessionPageToolsSwitchVoiceSurface, pageNames map[string]struct{}) ([]string, error) {
	baseNames := make([]string, 0)
	for _, surface := range surfaces {
		seen := map[string]bool{}
		for _, tool := range surface.Tools {
			if seen[tool.Name] {
				return nil, fmt.Errorf("session.update at record %d repeats tool %q", surface.Index, tool.Name)
			}
			seen[tool.Name] = true
			if _, isPage := pageNames[tool.Name]; !isPage {
				baseNames = appendUniqueSessionPageToolsSwitchVoice(baseNames, tool.Name)
			}
		}
	}
	sort.Strings(baseNames)
	if len(baseNames) == 0 {
		return nil, errors.New("provider definitions contain no static or stable base tools")
	}
	for _, stable := range webmcp.StableToolNames() {
		if !containsSessionPageToolsSwitchVoice(baseNames, stable) {
			return nil, fmt.Errorf("provider definition base omitted stable broker tool %q", stable)
		}
	}
	return baseNames, nil
}

// validateSessionPageToolsSwitchVoiceSurfaces proves every surface keeps the
// base definitions stable and the page surfaces go Cubecade->Margin->Cubecade.
func validateSessionPageToolsSwitchVoiceSurfaces(surfaces []sessionPageToolsSwitchVoiceSurface, baseNames []string, pageNames map[string]struct{}) error {
	wantCube, wantMargin := liveCubecadePageTools(), liveMarginPageTools()
	sort.Strings(wantCube)
	sort.Strings(wantMargin)
	baseDefinitions, stableDefinitions := map[string]string{}, map[string]string{}
	transitions := sessionPageToolsSwitchVoiceTransitions{cubeAt: -1, marginAt: -1, returnedCubeAt: -1}
	for index, surface := range surfaces {
		currentByName, page, err := sessionPageToolsSwitchVoiceSurfaceNames(surface, baseNames, pageNames)
		if err != nil {
			return err
		}
		if err := recordSessionPageToolsSwitchVoiceDefinitions(baseDefinitions, baseNames, currentByName, "static"); err != nil {
			return err
		}
		if err := recordSessionPageToolsSwitchVoiceDefinitions(stableDefinitions, webmcp.StableToolNames(), currentByName, "stable"); err != nil {
			return err
		}
		if err := transitions.observe(index, surface.Index, page, wantCube, wantMargin); err != nil {
			return err
		}
	}
	if transitions.cubeAt < 0 || transitions.marginAt < 0 || transitions.returnedCubeAt < 0 || transitions.cubeAt >= transitions.marginAt || transitions.marginAt >= transitions.returnedCubeAt {
		return fmt.Errorf("definition transitions did not prove Cubecade->Margin->Cubecade: cube=%d margin=%d returned_cube=%d surfaces=%d", transitions.cubeAt, transitions.marginAt, transitions.returnedCubeAt, len(surfaces))
	}
	return nil
}

// sessionPageToolsSwitchVoiceSurfaceNames canonicalizes one surface and
// requires it to be exactly the base tools plus its sorted page tools.
func sessionPageToolsSwitchVoiceSurfaceNames(surface sessionPageToolsSwitchVoiceSurface, baseNames []string, pageNames map[string]struct{}) (map[string]string, []string, error) {
	currentNames := make([]string, 0, len(surface.Tools))
	currentByName := map[string]string{}
	page := make([]string, 0)
	for _, tool := range surface.Tools {
		currentNames = append(currentNames, tool.Name)
		canonical, err := sessionPageToolsSwitchVoiceCanonicalJSON(tool.Raw)
		if err != nil {
			return nil, nil, fmt.Errorf("canonicalize provider definition %q: %w", tool.Name, err)
		}
		currentByName[tool.Name] = canonical
		if _, isPage := pageNames[tool.Name]; isPage {
			page = append(page, tool.Name)
		}
	}
	sort.Strings(currentNames)
	sort.Strings(page)
	for _, name := range baseNames {
		if !containsSessionPageToolsSwitchVoice(currentNames, name) {
			return nil, nil, fmt.Errorf("session.update at record %d omitted base tool %q", surface.Index, name)
		}
	}
	wantNames := append(append([]string(nil), baseNames...), page...)
	sort.Strings(wantNames)
	if !sameSessionPageToolsSwitchVoiceStrings(currentNames, wantNames) {
		return nil, nil, fmt.Errorf("session.update at record %d has unexpected ordered surface: got=%v want=%v", surface.Index, currentNames, wantNames)
	}
	return currentByName, page, nil
}

func recordSessionPageToolsSwitchVoiceDefinitions(seen map[string]string, names []string, current map[string]string, kind string) error {
	for _, name := range names {
		if previous, ok := seen[name]; ok && previous != current[name] {
			return fmt.Errorf("%s definition %q changed across session.update replacements", kind, name)
		}
		seen[name] = current[name]
	}
	return nil
}

// sessionPageToolsSwitchVoiceTransitions records the surface indexes of the
// first Cubecade, first Margin, and returned Cubecade page surfaces.
type sessionPageToolsSwitchVoiceTransitions struct {
	cubeAt, marginAt, returnedCubeAt int
}

func (transitions *sessionPageToolsSwitchVoiceTransitions) observe(index, recordIndex int, page, wantCube, wantMargin []string) error {
	switch {
	case sameSessionPageToolsSwitchVoiceStrings(page, wantCube):
		if transitions.cubeAt < 0 {
			transitions.cubeAt = index
		} else if transitions.marginAt >= 0 && transitions.returnedCubeAt < 0 {
			transitions.returnedCubeAt = index
		}
	case sameSessionPageToolsSwitchVoiceStrings(page, wantMargin):
		if transitions.cubeAt < 0 {
			return fmt.Errorf("Margin surface at record %d preceded Cubecade surface", recordIndex)
		}
		if transitions.marginAt < 0 {
			transitions.marginAt = index
		}
	default:
		if len(page) != 0 {
			return fmt.Errorf("session.update at record %d advertised unexpected page tools %v", recordIndex, page)
		}
	}
	return nil
}

func sessionPageToolsSwitchVoiceCanonicalJSON(raw json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func appendUniqueSessionPageToolsSwitchVoice(values []string, value string) []string {
	if containsSessionPageToolsSwitchVoice(values, value) {
		return values
	}
	return append(values, value)
}

func sameSessionPageToolsSwitchVoiceStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
