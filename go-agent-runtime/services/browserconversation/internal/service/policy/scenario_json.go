package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type browserConversationScenarioJSON struct {
	Version     string                              `json:"version"`
	ID          string                              `json:"id"`
	Name        string                              `json:"name"`
	Fixture     BrowserConversationFixture          `json:"fixture"`
	Steps       []browserConversationStepJSON       `json:"steps"`
	RunTimeout  string                              `json:"run_timeout"`
	PostSession BrowserConversationTabStateRequired `json:"post_session"`
}

type browserConversationStepJSON struct {
	ID            string                            `json:"id"`
	Utterance     string                            `json:"utterance"`
	PageID        string                            `json:"page_id"`
	ExpectedState *BrowserStateTransition           `json:"expected_state,omitempty"`
	Navigation    *BrowserCustomerNavigation        `json:"navigation,omitempty"`
	Correction    *BrowserConversationCorrection    `json:"correction,omitempty"`
	Interrupt     *BrowserConversationInterrupt     `json:"interrupt,omitempty"`
	Cancel        *BrowserConversationCancelRequest `json:"cancel,omitempty"`
	Deadline      string                            `json:"deadline"`
}

func parseScenarioJSON(data []byte) (BrowserConversationScenario, error) {
	var wire browserConversationScenarioJSON
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return BrowserConversationScenario{}, browserScenarioError("scenario", "invalid JSON: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return BrowserConversationScenario{}, browserScenarioError("scenario", "must contain exactly one JSON object")
	}
	runTimeout, err := parseScenarioDuration("run_timeout", wire.RunTimeout)
	if err != nil {
		return BrowserConversationScenario{}, err
	}
	steps := make([]BrowserConversationStep, len(wire.Steps))
	for index, step := range wire.Steps {
		deadline, parseErr := parseScenarioDuration(fmt.Sprintf("steps[%d].deadline", index), step.Deadline)
		if parseErr != nil {
			return BrowserConversationScenario{}, parseErr
		}
		steps[index] = BrowserConversationStep{
			ID: step.ID, Utterance: step.Utterance, PageID: step.PageID,
			ExpectedState: step.ExpectedState,
			Navigation:    step.Navigation,
			Correction:    step.Correction,
			Interrupt:     step.Interrupt,
			Cancel:        step.Cancel,
			Deadline:      deadline,
		}
	}
	parsed := BrowserConversationScenario{
		Version: wire.Version, ID: wire.ID, Name: wire.Name, Fixture: wire.Fixture,
		Steps: steps, RunTimeout: runTimeout, PostSession: wire.PostSession,
	}
	return parsed.Clone(), nil
}

func parseScenarioDuration(path, raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, browserScenarioError(path, "must be a duration string")
	}
	return duration, nil
}
