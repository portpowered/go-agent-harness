package probe

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func validateScenarioV2StepRequiredFields(step ScenarioV2Step, location string, lookup CorpusLookup) error {
	nonEmpty := func(value, fieldName string) error {
		if strings.TrimSpace(value) == "" {
			return newScenarioV2Error(location+"."+fieldName, "required field is missing")
		}
		return nil
	}
	if step.ToolRef != "" {
		if err := validateScenarioV2ToolRef(step.ToolRef, location+".tool_ref"); err != nil {
			return err
		}
	}
	if step.HasDurationMS && step.DurationMS < 0 {
		return newScenarioV2Error(location+".duration_ms", "must not be negative")
	}
	switch step.Type {
	case ScenarioV2StepBrowserSelect:
		return nonEmpty(step.TargetID, "target_id")
	case ScenarioV2StepBrowserNavigateFixture:
		if step.URL == "" && step.Fixture == "" {
			return newScenarioV2Error(location, "url or fixture is required")
		}
		if step.URL != "" && step.Fixture != "" {
			return newScenarioV2Error(location, "url and fixture are mutually exclusive")
		}
	case ScenarioV2StepWebMCPInvoke:
		if err := nonEmpty(step.ToolRef, "tool_ref"); err != nil {
			return err
		}
		if err := nonEmpty(step.InputJSON, "input_json"); err != nil {
			return err
		}
		if err := nonEmpty(step.Reason, "reason"); err != nil {
			return err
		}
		if _, err := decodeScenarioV2Object([]byte(step.InputJSON), location+".input_json"); err != nil {
			return newScenarioV2Error(location+".input_json", "must contain a JSON object")
		}
	case ScenarioV2StepWebMCPCancel:
		return nonEmpty(step.InvocationID, "invocation_id")
	case ScenarioV2StepSendText:
		return nonEmpty(step.Text, "text")
	case ScenarioV2StepSendAudio:
		if err := nonEmpty(step.CorpusID, "corpus_id"); err != nil {
			return err
		}
		if lookup != nil && !lookup.Has(step.CorpusID) {
			return &ScenarioV2Error{Path: location + ".corpus_id", Cause: errors.Join(ErrScenarioV2UnknownCorpus, fmt.Errorf("corpus is not registered"))}
		}
	case ScenarioV2StepInterrupt:
		return nonEmpty(step.AfterEvent, "after_event")
	case ScenarioV2StepOpenTab:
		return nonEmpty(step.URL, "url")
	case ScenarioV2StepSwitchBrowser:
		return nonEmpty(step.BrowserID, "browser_id")
	case ScenarioV2StepSleepFake:
		if !step.HasDurationMS {
			return newScenarioV2Error(location+".duration_ms", "required field is missing")
		}
	}
	return nil
}

func parseScenarioV2Expectation(raw json.RawMessage, index int) (ScenarioV2Expectation, error) {
	location := fmt.Sprintf("scenario.expectations[%d]", index)
	value, err := decodeScenarioV2Object(raw, location)
	if err != nil {
		return ScenarioV2Expectation{}, err
	}
	typeName, err := requiredScenarioV2String(value, location, "type")
	if err != nil {
		return ScenarioV2Expectation{}, err
	}
	expectationType := ScenarioV2ExpectationType(typeName)
	allowed, ok := scenarioV2ExpectationFields[expectationType]
	if !ok {
		return ScenarioV2Expectation{}, newScenarioV2Error(location+".type", "unknown expectation variant")
	}
	if err := rejectScenarioV2Fields(value, scenarioV2VariantFields(allowed), location); err != nil {
		return ScenarioV2Expectation{}, err
	}
	expectation := ScenarioV2Expectation{Type: expectationType}
	for fieldName, destination := range map[string]*string{
		"browser_id": &expectation.BrowserID, "target_id": &expectation.TargetID,
		"origin": &expectation.Origin, "name": &expectation.Name, "tool_ref": &expectation.ToolRef,
		"path": &expectation.Path, "status": &expectation.Status, "text": &expectation.Text,
	} {
		if value[fieldName] == nil {
			continue
		}
		parsed, parseErr := scenarioV2String(value[fieldName], location+"."+fieldName)
		if parseErr != nil {
			return ScenarioV2Expectation{}, parseErr
		}
		*destination = parsed
	}
	expectation.JSONPath = expectation.Path
	if expectation.Type == ScenarioV2ExpectationToolResultJSONPathEquals || expectation.Type == ScenarioV2ExpectationPageStateEquals {
		if strings.TrimSpace(expectation.Path) == "" {
			return ScenarioV2Expectation{}, newScenarioV2Error(location+".path", "must not be empty")
		}
		if !strings.HasPrefix(expectation.Path, "$") {
			return ScenarioV2Expectation{}, newScenarioV2Error(location+".path", "must be a JSONPath beginning with $")
		}
	}
	if rawValue, exists, valueErr := optionalScenarioV2JSON(value, location, "value", false); valueErr != nil {
		return ScenarioV2Expectation{}, valueErr
	} else if exists {
		expectation.Value = rawValue
	}
	if rawSchema, exists, schemaErr := optionalScenarioV2JSON(value, location, "schema", true); schemaErr != nil {
		return ScenarioV2Expectation{}, schemaErr
	} else if exists {
		expectation.Schema = rawSchema
	}
	if expectation.Type == ScenarioV2ExpectationToolSchemaEquals && len(expectation.Schema) == 0 {
		return ScenarioV2Expectation{}, newScenarioV2Error(location+".schema", "required field is missing")
	}
	if expectation.Type == ScenarioV2ExpectationToolResultJSONPathEquals || expectation.Type == ScenarioV2ExpectationPageStateEquals {
		if len(expectation.Value) == 0 {
			return ScenarioV2Expectation{}, newScenarioV2Error(location+".value", "required field is missing")
		}
	}
	if rawInput, exists := value["input_json"]; exists {
		expectation.InputJSON, err = scenarioV2String(rawInput, location+".input_json")
		if err != nil {
			return ScenarioV2Expectation{}, err
		}
		if _, err := decodeScenarioV2Object([]byte(expectation.InputJSON), location+".input_json"); err != nil {
			return ScenarioV2Expectation{}, newScenarioV2Error(location+".input_json", "must contain a JSON object")
		}
	}
	if rawOperations, exists, arrayErr := optionalScenarioV2StringArray(value, location, "operations"); arrayErr != nil {
		return ScenarioV2Expectation{}, arrayErr
	} else if exists {
		expectation.Operations = rawOperations
	}
	if rawMethods, exists, arrayErr := optionalScenarioV2StringArray(value, location, "methods"); arrayErr != nil {
		return ScenarioV2Expectation{}, arrayErr
	} else if exists {
		expectation.Methods = rawMethods
	}
	if rawEquals, exists := value["equals"]; exists {
		expectation.Equals, err = scenarioV2Int(rawEquals, location+".equals")
		if err != nil {
			return ScenarioV2Expectation{}, err
		}
		if expectation.Equals < 0 {
			return ScenarioV2Expectation{}, newScenarioV2Error(location+".equals", "must not be negative")
		}
		expectation.HasEquals = true
		if expectation.Type == ScenarioV2ExpectationCatalogGenerationEquals {
			expectation.Generation = expectation.Equals
			expectation.HasGeneration = true
		}
	}
	if err := validateScenarioV2ExpectationRequiredFields(expectation, location); err != nil {
		return ScenarioV2Expectation{}, err
	}
	return expectation, nil
}

func validateScenarioV2ExpectationRequiredFields(expectation ScenarioV2Expectation, location string) error {
	nonEmpty := func(value, fieldName string) error {
		if strings.TrimSpace(value) == "" {
			return newScenarioV2Error(location+"."+fieldName, "required field is missing")
		}
		return nil
	}
	if expectation.ToolRef != "" {
		if err := validateScenarioV2ToolRef(expectation.ToolRef, location+".tool_ref"); err != nil {
			return err
		}
	}
	if expectation.HasEquals && expectation.Equals < 0 {
		return newScenarioV2Error(location+".equals", "must not be negative")
	}
	if expectation.Type == ScenarioV2ExpectationBrowserCountEquals ||
		expectation.Type == ScenarioV2ExpectationEligibleTabCountEquals ||
		expectation.Type == ScenarioV2ExpectationCatalogGenerationEquals ||
		expectation.Type == ScenarioV2ExpectationToolInvocationCount {
		if !expectation.HasEquals {
			return newScenarioV2Error(location+".equals", "required field is missing")
		}
	}
	switch expectation.Type {
	case ScenarioV2ExpectationSelectedTabEquals:
		return nonEmpty(expectation.TargetID, "target_id")
	case ScenarioV2ExpectationSelectedOriginEquals:
		return nonEmpty(expectation.Origin, "origin")
	case ScenarioV2ExpectationToolCatalogContains, ScenarioV2ExpectationToolCatalogNotContains,
		ScenarioV2ExpectationToolInvocationCount:
		return nonEmpty(expectation.Name, "name")
	case ScenarioV2ExpectationToolInputJSONEquals:
		if err := nonEmpty(expectation.Name, "name"); err != nil {
			return err
		}
		if err := nonEmpty(expectation.InputJSON, "input_json"); err != nil {
			return err
		}
		if _, err := decodeScenarioV2Object([]byte(expectation.InputJSON), location+".input_json"); err != nil {
			return newScenarioV2Error(location+".input_json", "must contain a JSON object")
		}
	case ScenarioV2ExpectationToolStatusEquals:
		if err := nonEmpty(expectation.Name, "name"); err != nil {
			return err
		}
		return nonEmpty(expectation.Status, "status")
	case ScenarioV2ExpectationToolSchemaEquals:
		if err := nonEmpty(expectation.Name, "name"); err != nil {
			return err
		}
		if len(expectation.Schema) == 0 {
			return newScenarioV2Error(location+".schema", "required field is missing")
		}
		if _, err := scenarioV2JSON(expectation.Schema, location+".schema", true); err != nil {
			return err
		}
	case ScenarioV2ExpectationToolResultJSONPathEquals, ScenarioV2ExpectationPageStateEquals:
		if strings.TrimSpace(expectation.Path) == "" || !strings.HasPrefix(expectation.Path, "$") {
			return newScenarioV2Error(location+".path", "must be a JSONPath beginning with $")
		}
		if len(expectation.Value) == 0 {
			return newScenarioV2Error(location+".value", "required field is missing")
		}
		if _, err := scenarioV2JSON(expectation.Value, location+".value", false); err != nil {
			return err
		}
	case ScenarioV2ExpectationChromeOperationOrder, ScenarioV2ExpectationNoUnexpectedChromeOperations:
		if expectation.Operations == nil {
			return newScenarioV2Error(location+".operations", "required field is missing")
		}
	case ScenarioV2ExpectationGeneratedCDPMethodOrder, ScenarioV2ExpectationNoUnexpectedGeneratedCDPMethods:
		if expectation.Methods == nil {
			return newScenarioV2Error(location+".methods", "required field is missing")
		}
	case ScenarioV2ExpectationTranscriptContains:
		return nonEmpty(expectation.Text, "text")
	case ScenarioV2ExpectationStaleToolRejected:
		if expectation.ToolRef != "" {
			return nil
		}
	}
	return nil
}

func validateScenarioV2ToolRef(value, location string) error {
	const prefix = "webmcp.tool-ref.v1:"
	const tokenLength = 22
	if !strings.HasPrefix(value, prefix) || len(value)-len(prefix) != tokenLength {
		return newScenarioV2Error(location, "must use the webmcp.tool-ref.v1 grammar")
	}
	for _, character := range value[len(prefix):] {
		if !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-') {
			return newScenarioV2Error(location, "must use the webmcp.tool-ref.v1 grammar")
		}
	}
	return nil
}

func validateTypedScenarioV2Step(step ScenarioV2Step, index int, lookup CorpusLookup) error {
	if _, ok := scenarioV2StepFields[step.Type]; !ok {
		return newScenarioV2Error(fmt.Sprintf("steps[%d].type", index), "unknown step variant")
	}
	return validateScenarioV2StepRequiredFields(step, fmt.Sprintf("steps[%d]", index), lookup)
}

func validateTypedScenarioV2Expectation(expectation ScenarioV2Expectation, index int) error {
	if _, ok := scenarioV2ExpectationFields[expectation.Type]; !ok {
		return newScenarioV2Error(fmt.Sprintf("expectations[%d].type", index), "unknown expectation variant")
	}
	return validateScenarioV2ExpectationRequiredFields(expectation, fmt.Sprintf("expectations[%d]", index))
}

func cloneScenarioV2Raw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
