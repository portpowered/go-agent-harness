package service

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
)

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
			browserConversationOpaqueString(call.State) == browserConversationInvocationCompleted &&
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
