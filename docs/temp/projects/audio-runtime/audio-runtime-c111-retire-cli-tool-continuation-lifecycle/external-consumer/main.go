package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioncontinuation"
	continuationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioncontinuation/wire"
)

func assertContract() (map[string]any, error) {
	first := continuationwire.NewService()
	second := continuationwire.NewService()
	if first == nil || second == nil {
		return nil, fmt.Errorf("Wire returned a nil service")
	}
	primary := errors.New("consumer provider close")
	err := first.Enrich(primary, sessioncontinuation.Snapshot{
		Unresolved: sessioncontinuation.UnresolvedToolResultsSnapshot{
			CallIDs: []string{" unresolved ", "unresolved"},
		},
		Tool: sessioncontinuation.ContinuationSnapshot{
			CallIDs:          []string{"image", "ordinary"},
			ProviderStatuses: map[string]string{"image": "ordinary-metadata", "ordinary": "failed"},
			ProviderCodes:    map[string]string{"ordinary": "E_TOOL"},
			ProviderDetails:  map[string]string{"ordinary": "provider detail"},
		},
		Image: sessioncontinuation.ContinuationSnapshot{
			CallIDs:          []string{"image"},
			ProviderStatuses: map[string]string{"image": "incomplete"},
			ProviderCodes:    map[string]string{"image": "E_IMAGE"},
			ProviderDetails:  map[string]string{"image": "image detail"},
		},
	})
	if !errors.Is(err, primary) || !errors.Is(err, sessioncontinuation.ErrUnresolvedToolResults) || !errors.Is(err, sessioncontinuation.ErrImageContinuationIncomplete) || !errors.Is(err, sessioncontinuation.ErrToolContinuationIncomplete) {
		return nil, fmt.Errorf("public error identity was not preserved: %v", err)
	}
	var unresolved *sessioncontinuation.UnresolvedToolResultsError
	if !errors.As(err, &unresolved) || !reflect.DeepEqual(unresolved.CallIDs, []string{"unresolved"}) {
		return nil, fmt.Errorf("unresolved result contract mismatch: %v", err)
	}
	var image *sessioncontinuation.ImageContinuationError
	if !errors.As(err, &image) || !reflect.DeepEqual(image.CallIDs, []string{"image"}) || image.ProviderStatuses["image"] != "incomplete" || image.ProviderCodes["image"] != "E_IMAGE" {
		return nil, fmt.Errorf("image continuation contract mismatch: %v", err)
	}
	var tool *sessioncontinuation.ToolContinuationError
	if !errors.As(err, &tool) || !reflect.DeepEqual(tool.CallIDs, []string{"ordinary"}) || tool.ProviderCodes["ordinary"] != "E_TOOL" {
		return nil, fmt.Errorf("ordinary continuation contract mismatch: %v", err)
	}
	if got := first.FormatMetadata(map[string]string{"b": "2", "a": "1"}); got != "a=1, b=2" {
		return nil, fmt.Errorf("metadata order = %q", got)
	}
	other := second.Enrich(nil, sessioncontinuation.Snapshot{Tool: sessioncontinuation.ContinuationSnapshot{CallIDs: []string{"second"}}})
	var otherTool *sessioncontinuation.ToolContinuationError
	if !errors.As(other, &otherTool) || !reflect.DeepEqual(otherTool.CallIDs, []string{"second"}) {
		return nil, fmt.Errorf("second service was not usable: %v", other)
	}
	tool.CallIDs[0] = "mutated-first"
	if otherTool.CallIDs[0] != "second" {
		return nil, fmt.Errorf("service instances shared output state")
	}
	return map[string]any{
		"status":             "accepted",
		"primary_cause":      true,
		"unresolved_ids":     unresolved.CallIDs,
		"image_ids":          image.CallIDs,
		"ordinary_tool_ids":  otherTool.CallIDs,
		"metadata":           first.FormatMetadata(map[string]string{"b": "2", "a": "1"}),
		"cli_imports":        false,
		"private_imports":    false,
		"credentials":        false,
		"hidden_global_io":   false,
	}, nil
}

func main() {
	report, err := assertContract()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
