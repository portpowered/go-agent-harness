package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	roomcapabilities "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomcapabilities"
	roomcapabilitiesWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomcapabilities/wire"
)

type consumerExecutor struct {
	label string
	calls atomic.Int32
}

func (e *consumerExecutor) Execute(_ context.Context, call roomcapabilities.ToolCall) (roomcapabilities.ToolCallResponse, error) {
	e.calls.Add(1)
	return roomcapabilities.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: e.label + ":" + call.Name}, nil
}

type consumerReport struct {
	Schema                  string            `json:"schema"`
	ConstructedVia          string            `json:"constructed_via"`
	CandidateRevision       string            `json:"candidate_revision,omitempty"`
	SourceRevision          string            `json:"source_revision,omitempty"`
	FirstInitialDefinitions []string          `json:"first_initial_definitions"`
	FirstBaseDefinitions    []string          `json:"first_base_definitions"`
	FirstRefreshed          []string          `json:"first_refreshed"`
	SecondDefinitions       []string          `json:"second_definitions"`
	Dispatch                map[string]string `json:"dispatch"`
	MismatchErrorIdentity   bool              `json:"mismatch_error_identity"`
	StaleToolRejected       bool              `json:"stale_tool_rejected"`
	StaticPreserved         bool              `json:"static_preserved"`
	FirstInitializedOnce    bool              `json:"first_initialized_once"`
	FirstClosedOnce         bool              `json:"first_closed_once"`
	SecondInitializedOnce   bool              `json:"second_initialized_once"`
	SecondClosedOnce        bool              `json:"second_closed_once"`
}

func names(definitions []roomcapabilities.ToolDefinition) []string {
	result := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, definition.Name)
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func definitions(names ...string) []roomcapabilities.ToolDefinition {
	result := make([]roomcapabilities.ToolDefinition, 0, len(names))
	for _, name := range names {
		result = append(result, roomcapabilities.ToolDefinition{Name: name})
	}
	return result
}

func run() (consumerReport, error) {
	service := roomcapabilitiesWire.NewService()
	firstStatic := &consumerExecutor{label: "first-static"}
	firstBrowser := &consumerExecutor{label: "first-browser"}
	secondStatic := &consumerExecutor{label: "second-static"}
	secondBrowser := &consumerExecutor{label: "second-browser"}
	var firstRefreshes atomic.Int32
	var firstInitializes atomic.Int32
	var firstCloses atomic.Int32
	var secondInitializes atomic.Int32
	var secondCloses atomic.Int32

	first, err := service.Compose(context.Background(), roomcapabilities.Participant{ID: "first", Tools: []string{"alpha"}}, roomcapabilities.ToolCapabilities{
		Executor: firstStatic, Definitions: definitions("alpha"),
	}, &roomcapabilities.BrowserCapabilities{
		Executor: firstBrowser, Definitions: definitions("page-first"), ToolDefinitionBase: definitions("page-stable"),
		RefreshToolDefinitions: func(context.Context) ([]roomcapabilities.ToolDefinition, error) {
			firstRefreshes.Add(1)
			return definitions("page-first-refreshed"), nil
		},
		Initialize: func(context.Context) error { firstInitializes.Add(1); return nil },
		Close:      func() error { firstCloses.Add(1); return nil },
	})
	if err != nil {
		return consumerReport{}, fmt.Errorf("compose first participant: %w", err)
	}
	second, err := service.Compose(context.Background(), roomcapabilities.Participant{ID: "second", Tools: []string{"beta"}}, roomcapabilities.ToolCapabilities{
		Executor: secondStatic, Definitions: definitions("beta"),
	}, &roomcapabilities.BrowserCapabilities{
		Executor: secondBrowser, Definitions: definitions("page-second"), ToolDefinitionBase: definitions("page-stable-second"),
		Initialize: func(context.Context) error { secondInitializes.Add(1); return nil },
		Close:      func() error { secondCloses.Add(1); return nil },
	})
	if err != nil {
		return consumerReport{}, fmt.Errorf("compose second participant: %w", err)
	}

	dispatch := make(map[string]string)
	for _, item := range []struct {
		label      string
		capability roomcapabilities.Capability
		name       string
	}{
		{"first static", first, "alpha"},
		{"first browser", first, "page-first"},
		{"second static", second, "beta"},
		{"second browser", second, "page-second"},
	} {
		response, executeErr := item.capability.Executor.Execute(context.Background(), roomcapabilities.ToolCall{ID: item.label, Name: item.name})
		if executeErr != nil {
			return consumerReport{}, fmt.Errorf("%s dispatch: %w", item.label, executeErr)
		}
		dispatch[item.label] = response.Content
	}

	firstRefreshed, err := first.RefreshToolDefinitions(context.Background())
	if err != nil {
		return consumerReport{}, fmt.Errorf("refresh first participant: %w", err)
	}
	_, staleErr := first.Executor.Execute(context.Background(), roomcapabilities.ToolCall{Name: "page-first"})
	refreshedResponse, err := first.Executor.Execute(context.Background(), roomcapabilities.ToolCall{Name: "page-first-refreshed"})
	if err != nil {
		return consumerReport{}, fmt.Errorf("refreshed browser dispatch: %w", err)
	}
	dispatch["first refreshed browser"] = refreshedResponse.Content
	secondResponse, err := second.Executor.Execute(context.Background(), roomcapabilities.ToolCall{Name: "page-second"})
	if err != nil {
		return consumerReport{}, fmt.Errorf("second participant after first refresh: %w", err)
	}
	dispatch["second browser after first refresh"] = secondResponse.Content

	_, mismatchErr := service.Compose(context.Background(), roomcapabilities.Participant{ID: "mismatch", Tools: []string{"alpha"}}, roomcapabilities.ToolCapabilities{
		Executor: firstStatic, Definitions: definitions("not-requested"),
	}, nil)

	if err := first.Initialize(context.Background()); err != nil {
		return consumerReport{}, fmt.Errorf("initialize first participant: %w", err)
	}
	if err := first.Initialize(context.Background()); err != nil {
		return consumerReport{}, fmt.Errorf("initialize first participant twice: %w", err)
	}
	if err := second.Initialize(context.Background()); err != nil {
		return consumerReport{}, fmt.Errorf("initialize second participant: %w", err)
	}
	if err := second.Initialize(context.Background()); err != nil {
		return consumerReport{}, fmt.Errorf("initialize second participant twice: %w", err)
	}
	if err := first.Close(); err != nil {
		return consumerReport{}, fmt.Errorf("close first participant: %w", err)
	}
	if err := first.Close(); err != nil {
		return consumerReport{}, fmt.Errorf("close first participant twice: %w", err)
	}
	if err := second.Close(); err != nil {
		return consumerReport{}, fmt.Errorf("close second participant: %w", err)
	}
	if err := second.Close(); err != nil {
		return consumerReport{}, fmt.Errorf("close second participant twice: %w", err)
	}

	firstInitial := names(first.Definitions)
	firstBase := names(first.ToolDefinitionBase)
	return consumerReport{
		Schema:                  "audio-runtime.c88.roomcapabilities-consumer/v1",
		ConstructedVia:          "roomcapabilities/wire.NewService",
		CandidateRevision:       os.Getenv("C88_CANDIDATE_REVISION"),
		SourceRevision:          os.Getenv("C88_SOURCE_REVISION"),
		FirstInitialDefinitions: firstInitial,
		FirstBaseDefinitions:    firstBase,
		FirstRefreshed:          names(firstRefreshed),
		SecondDefinitions:       names(second.Definitions),
		Dispatch:                dispatch,
		MismatchErrorIdentity:   errors.Is(mismatchErr, roomcapabilities.ErrToolMismatch) && errors.Is(mismatchErr, roomcapabilities.ErrUnrequestedTool),
		StaleToolRejected:       staleErr != nil,
		StaticPreserved:         contains(names(firstRefreshed), "alpha") && !contains(names(firstRefreshed), "page-first"),
		FirstInitializedOnce:    firstInitializes.Load() == 1,
		FirstClosedOnce:         firstCloses.Load() == 1,
		SecondInitializedOnce:   secondInitializes.Load() == 1,
		SecondClosedOnce:        secondCloses.Load() == 1,
	}, nil
}

func main() {
	report, err := run()
	if err != nil {
		panic(err)
	}
	if err := printJSON(report); err != nil {
		panic(err)
	}
}

func printJSON(value consumerReport) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}
