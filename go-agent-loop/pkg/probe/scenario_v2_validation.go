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
	decoders := []func(*ScenarioV2Expectation, scenarioV2Object, string) error{
		decodeScenarioV2ExpectationStrings,
		decodeScenarioV2ExpectationJSON,
		decodeScenarioV2ExpectationInputJSON,
		decodeScenarioV2ExpectationArrays,
		decodeScenarioV2ExpectationEquals,
	}
	for _, decode := range decoders {
		if err := decode(&expectation, value, location); err != nil {
			return ScenarioV2Expectation{}, err
		}
	}
	if err := validateScenarioV2ExpectationRequiredFields(expectation, location); err != nil {
		return ScenarioV2Expectation{}, err
	}
	return expectation, nil
}

func isScenarioV2JSONPathExpectation(expectationType ScenarioV2ExpectationType) bool {
	return expectationType == ScenarioV2ExpectationToolResultJSONPathEquals || expectationType == ScenarioV2ExpectationPageStateEquals
}

func decodeScenarioV2ExpectationStrings(expectation *ScenarioV2Expectation, value scenarioV2Object, location string) error {
	for fieldName, destination := range map[string]*string{
		"browser_id": &expectation.BrowserID, "target_id": &expectation.TargetID,
		"origin": &expectation.Origin, "name": &expectation.Name, "tool_ref": &expectation.ToolRef,
		"path": &expectation.Path, "status": &expectation.Status, "text": &expectation.Text,
	} {
		if value[fieldName] == nil {
			continue
		}
		parsed, err := scenarioV2String(value[fieldName], location+"."+fieldName)
		if err != nil {
			return err
		}
		*destination = parsed
	}
	expectation.JSONPath = expectation.Path
	if !isScenarioV2JSONPathExpectation(expectation.Type) {
		return nil
	}
	if strings.TrimSpace(expectation.Path) == "" {
		return newScenarioV2Error(location+".path", "must not be empty")
	}
	if !strings.HasPrefix(expectation.Path, "$") {
		return newScenarioV2Error(location+".path", "must be a JSONPath beginning with $")
	}
	return nil
}

func decodeScenarioV2ExpectationJSON(expectation *ScenarioV2Expectation, value scenarioV2Object, location string) error {
	rawValue, exists, err := optionalScenarioV2JSON(value, location, "value", false)
	if err != nil {
		return err
	}
	if exists {
		expectation.Value = rawValue
	}
	rawSchema, exists, err := optionalScenarioV2JSON(value, location, "schema", true)
	if err != nil {
		return err
	}
	if exists {
		expectation.Schema = rawSchema
	}
	if expectation.Type == ScenarioV2ExpectationToolSchemaEquals && len(expectation.Schema) == 0 {
		return newScenarioV2Error(location+".schema", "required field is missing")
	}
	if isScenarioV2JSONPathExpectation(expectation.Type) && len(expectation.Value) == 0 {
		return newScenarioV2Error(location+".value", "required field is missing")
	}
	return nil
}

func decodeScenarioV2ExpectationInputJSON(expectation *ScenarioV2Expectation, value scenarioV2Object, location string) error {
	rawInput, exists := value["input_json"]
	if !exists {
		return nil
	}
	var err error
	if expectation.InputJSON, err = scenarioV2String(rawInput, location+".input_json"); err != nil {
		return err
	}
	if _, err := decodeScenarioV2Object([]byte(expectation.InputJSON), location+".input_json"); err != nil {
		return newScenarioV2Error(location+".input_json", "must contain a JSON object")
	}
	return nil
}

func decodeScenarioV2ExpectationArrays(expectation *ScenarioV2Expectation, value scenarioV2Object, location string) error {
	operations, exists, err := optionalScenarioV2StringArray(value, location, "operations")
	if err != nil {
		return err
	}
	if exists {
		expectation.Operations = operations
	}
	methods, exists, err := optionalScenarioV2StringArray(value, location, "methods")
	if err != nil {
		return err
	}
	if exists {
		expectation.Methods = methods
	}
	return nil
}

func decodeScenarioV2ExpectationEquals(expectation *ScenarioV2Expectation, value scenarioV2Object, location string) error {
	rawEquals, exists := value["equals"]
	if !exists {
		return nil
	}
	var err error
	if expectation.Equals, err = scenarioV2Int(rawEquals, location+".equals"); err != nil {
		return err
	}
	if expectation.Equals < 0 {
		return newScenarioV2Error(location+".equals", "must not be negative")
	}
	expectation.HasEquals = true
	if expectation.Type == ScenarioV2ExpectationCatalogGenerationEquals {
		expectation.Generation = expectation.Equals
		expectation.HasGeneration = true
	}
	return nil
}

func nonEmptyScenarioV2Field(location, fieldName, value string) error {
	if strings.TrimSpace(value) == "" {
		return newScenarioV2Error(location+"."+fieldName, "required field is missing")
	}
	return nil
}

// validateScenarioV2ExpectationCommonFields checks fields shared by several
// expectation variants before the variant-specific requirements.
func validateScenarioV2ExpectationCommonFields(expectation ScenarioV2Expectation, location string) error {
	if expectation.ToolRef != "" {
		if err := validateScenarioV2ToolRef(expectation.ToolRef, location+".tool_ref"); err != nil {
			return err
		}
	}
	if expectation.HasEquals && expectation.Equals < 0 {
		return newScenarioV2Error(location+".equals", "must not be negative")
	}
	requiresEquals := expectation.Type == ScenarioV2ExpectationBrowserCountEquals ||
		expectation.Type == ScenarioV2ExpectationEligibleTabCountEquals ||
		expectation.Type == ScenarioV2ExpectationCatalogGenerationEquals ||
		expectation.Type == ScenarioV2ExpectationToolInvocationCount
	if requiresEquals && !expectation.HasEquals {
		return newScenarioV2Error(location+".equals", "required field is missing")
	}
	return nil
}

func validateScenarioV2ExpectationRequiredFields(expectation ScenarioV2Expectation, location string) error {
	nonEmpty := func(value, fieldName string) error {
		return nonEmptyScenarioV2Field(location, fieldName, value)
	}
	if err := validateScenarioV2ExpectationCommonFields(expectation, location); err != nil {
		return err
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
		return validateScenarioV2ToolInputJSONExpectation(expectation, location)
	case ScenarioV2ExpectationToolStatusEquals:
		if err := nonEmpty(expectation.Name, "name"); err != nil {
			return err
		}
		return nonEmpty(expectation.Status, "status")
	case ScenarioV2ExpectationToolSchemaEquals:
		return validateScenarioV2ToolSchemaExpectation(expectation, location)
	case ScenarioV2ExpectationToolResultJSONPathEquals, ScenarioV2ExpectationPageStateEquals:
		return validateScenarioV2JSONPathExpectation(expectation, location)
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

func validateScenarioV2ToolInputJSONExpectation(expectation ScenarioV2Expectation, location string) error {
	if err := nonEmptyScenarioV2Field(location, "name", expectation.Name); err != nil {
		return err
	}
	if err := nonEmptyScenarioV2Field(location, "input_json", expectation.InputJSON); err != nil {
		return err
	}
	if _, err := decodeScenarioV2Object([]byte(expectation.InputJSON), location+".input_json"); err != nil {
		return newScenarioV2Error(location+".input_json", "must contain a JSON object")
	}
	return nil
}

func validateScenarioV2ToolSchemaExpectation(expectation ScenarioV2Expectation, location string) error {
	if err := nonEmptyScenarioV2Field(location, "name", expectation.Name); err != nil {
		return err
	}
	if len(expectation.Schema) == 0 {
		return newScenarioV2Error(location+".schema", "required field is missing")
	}
	_, err := scenarioV2JSON(expectation.Schema, location+".schema", true)
	return err
}

func validateScenarioV2JSONPathExpectation(expectation ScenarioV2Expectation, location string) error {
	if strings.TrimSpace(expectation.Path) == "" || !strings.HasPrefix(expectation.Path, "$") {
		return newScenarioV2Error(location+".path", "must be a JSONPath beginning with $")
	}
	if len(expectation.Value) == 0 {
		return newScenarioV2Error(location+".value", "required field is missing")
	}
	_, err := scenarioV2JSON(expectation.Value, location+".value", false)
	return err
}

func validateScenarioV2ToolRef(value, location string) error {
	const prefix = "webmcp.tool-ref.v1:"
	const tokenLength = 22
	if !strings.HasPrefix(value, prefix) || len(value)-len(prefix) != tokenLength {
		return newScenarioV2Error(location, "must use the webmcp.tool-ref.v1 grammar")
	}
	for _, character := range value[len(prefix):] {
		if !isScenarioV2ToolRefTokenCharacter(character) {
			return newScenarioV2Error(location, "must use the webmcp.tool-ref.v1 grammar")
		}
	}
	return nil
}

// isScenarioV2ToolRefTokenCharacter reports whether character belongs to the
// URL-safe base64 alphabet used by tool-ref tokens.
func isScenarioV2ToolRefTokenCharacter(character rune) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9') || character == '_' || character == '-'
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
