package main

import (
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestHistoryFingerprintAndResponseAreRequestDerived(t *testing.T) {
	items := []messageRecord{{Role: string(messages.RoleUser), Text: "one"}, {Role: string(messages.RoleAssistant), Text: "two"}}
	provider := newDeterministicProvider("test", providerOptions{})
	response := provider.observe(messages.InferenceRequest{Messages: []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "one"),
		messages.NewTextMessage(messages.RoleAssistant, "two"),
	}})
	if !strings.Contains(response, historyFingerprint(items)) || !strings.Contains(response, `input="one"`) {
		t.Fatalf("response %q does not encode observed history", response)
	}
}

func TestConfigRejectsRelativePathsAndTrailingJSON(t *testing.T) {
	if err := validateConfig(config{Scenario: "boundary", StoreDirectory: "relative", WorkspaceDirectory: "/tmp/work", Input: "x"}); err == nil {
		t.Fatal("relative store path was accepted")
	}
	input := strings.NewReader(`{"scenario":"boundary","store_directory":"/tmp/store","workspace_directory":"/tmp/work","input":"x"} {"scenario":"boundary"}`)
	if _, err := readConfigFrom(input); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("trailing JSON was accepted: %v", err)
	}
}
