package probe

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// LoadScenarioV2 decodes one strict probe.scenario.v2 document. scenarioPath
// supplies the canonical containing directory used for fixture references; it
// may be empty only when the document has no fixture references. An optional
// CorpusLookup makes send_audio corpus identity validation fail before any
// execution. Supplying no lookup preserves the legacy package's ability to
// parse authored scenarios before a runtime-specific corpus is selected.
func LoadScenarioV2(input any, scenarioPath string, lookups ...CorpusLookup) (ScenarioV2, error) {
	if len(lookups) > 1 {
		return ScenarioV2{}, newScenarioV2Error("corpus_lookup", "only one corpus lookup is permitted")
	}
	data, err := readInput(input)
	if err != nil {
		return ScenarioV2{}, newScenarioV2Error("document", "%v", err)
	}
	if !utf8.Valid(data) {
		return ScenarioV2{}, newScenarioV2Error("document", "input is not valid UTF-8")
	}
	root, err := decodeScenarioV2Object(data, "scenario")
	if err != nil {
		return ScenarioV2{}, err
	}
	if err := rejectScenarioV2Fields(root, scenarioV2RootFields, "scenario"); err != nil {
		return ScenarioV2{}, err
	}

	version, err := requiredScenarioV2String(root, "scenario", "schema_version")
	if err != nil {
		return ScenarioV2{}, err
	}
	if version != ScenarioV2Version {
		return ScenarioV2{}, newScenarioV2Error("scenario.schema_version", "unsupported version")
	}
	id, err := requiredScenarioV2String(root, "scenario", "id")
	if err != nil {
		return ScenarioV2{}, err
	}
	name, err := optionalScenarioV2String(root, "scenario", "name")
	if err != nil {
		return ScenarioV2{}, err
	}
	description, err := optionalScenarioV2String(root, "scenario", "description")
	if err != nil {
		return ScenarioV2{}, err
	}
	browserFixture, err := optionalScenarioV2String(root, "scenario", "browser_fixture")
	if err != nil {
		return ScenarioV2{}, err
	}
	providerFixture, err := optionalScenarioV2String(root, "scenario", "provider_fixture")
	if err != nil {
		return ScenarioV2{}, err
	}
	if _, exists := root["browser_fixture"]; exists && strings.TrimSpace(browserFixture) == "" {
		return ScenarioV2{}, &ScenarioV2Error{Path: "scenario.browser_fixture", Cause: ErrScenarioV2FixturePath}
	}
	if _, exists := root["provider_fixture"]; exists && strings.TrimSpace(providerFixture) == "" {
		return ScenarioV2{}, &ScenarioV2Error{Path: "scenario.provider_fixture", Cause: ErrScenarioV2FixturePath}
	}

	var fixtureRoot string
	if (browserFixture != "" || providerFixture != "") && strings.TrimSpace(scenarioPath) == "" {
		return ScenarioV2{}, newScenarioV2Error("scenario", "scenario path is required when fixture references are present")
	}
	if strings.TrimSpace(scenarioPath) != "" {
		fixtureRoot, err = canonicalScenarioV2Dir(scenarioPath)
		if err != nil && (browserFixture != "" || providerFixture != "") {
			return ScenarioV2{}, wrapScenarioV2Error("scenario", err)
		}
	}

	rawSteps, ok := root["steps"]
	if !ok {
		return ScenarioV2{}, newScenarioV2Error("scenario.steps", "required field is missing")
	}
	stepValues, err := scenarioV2Array(rawSteps, "scenario.steps")
	if err != nil {
		return ScenarioV2{}, err
	}
	if len(stepValues) == 0 {
		return ScenarioV2{}, newScenarioV2Error("scenario.steps", "must contain at least one step")
	}

	rawExpectations, ok := root["expectations"]
	if !ok {
		return ScenarioV2{}, newScenarioV2Error("scenario.expectations", "required field is missing")
	}
	expectationValues, err := scenarioV2Array(rawExpectations, "scenario.expectations")
	if err != nil {
		return ScenarioV2{}, err
	}
	if len(expectationValues) == 0 {
		return ScenarioV2{}, newScenarioV2Error("scenario.expectations", "must contain at least one expectation")
	}

	var lookup CorpusLookup
	if len(lookups) == 1 {
		lookup = lookups[0]
	}
	result := ScenarioV2{
		SchemaVersion:   version,
		ID:              id,
		Name:            name,
		Description:     description,
		BrowserFixture:  browserFixture,
		ProviderFixture: providerFixture,
		SourcePath:      scenarioPath,
		FixtureRoot:     fixtureRoot,
		Steps:           make([]ScenarioV2Step, len(stepValues)),
		Expectations:    make([]ScenarioV2Expectation, len(expectationValues)),
	}
	for index, raw := range stepValues {
		result.Steps[index], err = parseScenarioV2Step(raw, index, lookup, fixtureRoot)
		if err != nil {
			return ScenarioV2{}, err
		}
	}
	for index, raw := range expectationValues {
		result.Expectations[index], err = parseScenarioV2Expectation(raw, index)
		if err != nil {
			return ScenarioV2{}, err
		}
	}

	if browserFixture != "" || providerFixture != "" {
		if browserFixture != "" {
			result.BrowserFixturePath, err = resolveScenarioV2FixturePathFromRoot(fixtureRoot, browserFixture)
			if err != nil {
				return ScenarioV2{}, wrapScenarioV2Error("scenario.browser_fixture", err)
			}
		}
		if providerFixture != "" {
			result.ProviderFixturePath, err = resolveScenarioV2FixturePathFromRoot(fixtureRoot, providerFixture)
			if err != nil {
				return ScenarioV2{}, wrapScenarioV2Error("scenario.provider_fixture", err)
			}
		}
	}
	return result, nil
}

// DecodeScenarioV2 is an alias for LoadScenarioV2.
func DecodeScenarioV2(input any, scenarioPath string, lookups ...CorpusLookup) (ScenarioV2, error) {
	return LoadScenarioV2(input, scenarioPath, lookups...)
}

// LoadProbeScenarioV2 is an alias for LoadScenarioV2.
func LoadProbeScenarioV2(input any, scenarioPath string, lookups ...CorpusLookup) (ScenarioV2, error) {
	return LoadScenarioV2(input, scenarioPath, lookups...)
}

// LoadScenarioV2File reads and validates a v2 scenario from disk. Referenced
// fixtures are resolved under the scenario's canonical containing directory;
// they are not opened until a caller explicitly asks for one.
func LoadScenarioV2File(path string, lookups ...CorpusLookup) (ScenarioV2, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ScenarioV2{}, fmt.Errorf("read probe.scenario.v2 %q: %w", path, err)
	}
	return LoadScenarioV2(data, path, lookups...)
}

// LoadProbeScenarioV2File is an alias for LoadScenarioV2File.
func LoadProbeScenarioV2File(path string, lookups ...CorpusLookup) (ScenarioV2, error) {
	return LoadScenarioV2File(path, lookups...)
}

// Validate checks a typed ScenarioV2 constructed by a caller. Documents
// decoded by LoadScenarioV2 have already passed the stricter unknown-field
// checks; this method protects the public typed seam from invalid values.
func (s ScenarioV2) Validate(lookups ...CorpusLookup) error {
	if len(lookups) > 1 {
		return newScenarioV2Error("corpus_lookup", "only one corpus lookup is permitted")
	}
	if s.SchemaVersion != ScenarioV2Version {
		return newScenarioV2Error("schema_version", "unsupported version")
	}
	if strings.TrimSpace(s.ID) == "" {
		return newScenarioV2Error("id", "must not be empty")
	}
	if len(s.Steps) == 0 {
		return newScenarioV2Error("steps", "must contain at least one step")
	}
	if len(s.Expectations) == 0 {
		return newScenarioV2Error("expectations", "must contain at least one expectation")
	}
	if s.BrowserFixture != "" || s.ProviderFixture != "" {
		if s.FixtureRoot == "" {
			return newScenarioV2Error("fixture", "scenario has no canonical fixture root")
		}
		for fieldName, reference := range map[string]string{
			"browser_fixture":  s.BrowserFixture,
			"provider_fixture": s.ProviderFixture,
		} {
			if reference == "" {
				continue
			}
			if _, err := resolveScenarioV2FixturePathFromRoot(s.FixtureRoot, reference); err != nil {
				return wrapScenarioV2Error(fieldName, err)
			}
		}
	}
	var lookup CorpusLookup
	if len(lookups) == 1 {
		lookup = lookups[0]
	}
	for index, step := range s.Steps {
		if err := validateTypedScenarioV2Step(step, index, lookup); err != nil {
			return err
		}
	}
	for index, expectation := range s.Expectations {
		if err := validateTypedScenarioV2Expectation(expectation, index); err != nil {
			return err
		}
	}
	return nil
}

// Valid reports whether a typed v2 scenario passes Validate.
func (s ScenarioV2) Valid(lookups ...CorpusLookup) bool { return s.Validate(lookups...) == nil }

// ResolveFixture resolves one authored reference using the scenario's
// canonical root. It is useful for callers that have additional fixture-like
// files but must retain the same containment policy.
func (s ScenarioV2) ResolveFixture(reference string) (string, error) {
	if s.FixtureRoot == "" {
		return "", newScenarioV2Error("fixture", "scenario has no canonical fixture root")
	}
	return resolveScenarioV2FixturePathFromRoot(s.FixtureRoot, reference)
}

// OpenBrowserFixture performs the containment check again immediately before
// opening the browser fixture. This prevents a syntactically safe reference
// from becoming an unsafe open after a symlink is introduced or changed.
func (s ScenarioV2) OpenBrowserFixture() (io.ReadCloser, error) {
	return s.openFixture(s.BrowserFixture, "browser_fixture")
}

// OpenProviderFixture performs the containment check again immediately before
// opening the provider fixture.
func (s ScenarioV2) OpenProviderFixture() (io.ReadCloser, error) {
	return s.openFixture(s.ProviderFixture, "provider_fixture")
}

func (s ScenarioV2) openFixture(reference, fieldName string) (io.ReadCloser, error) {
	if reference == "" {
		return nil, newScenarioV2Error(fieldName, "is not configured")
	}
	path, err := s.ResolveFixture(reference)
	if err != nil {
		return nil, wrapScenarioV2Error(fieldName, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s %q: %w", fieldName, path, err)
	}
	// Re-evaluate the path after opening. A changed symlink must never make a
	// caller believe an out-of-root target was opened as a contained fixture.
	resolved, resolveErr := resolveScenarioV2FixturePathFromRoot(s.FixtureRoot, reference)
	if resolveErr != nil || resolved != path {
		_ = file.Close()
		if resolveErr != nil {
			return nil, wrapScenarioV2Error(fieldName, resolveErr)
		}
		return nil, newScenarioV2Error(fieldName, "fixture target changed during open")
	}
	return file, nil
}

// ResolveScenarioV2FixturePath resolves reference relative to the canonical
// containing directory of scenarioPath. It validates and resolves the path,
// but does not open or parse the target.
func ResolveScenarioV2FixturePath(scenarioPath, reference string) (string, error) {
	root, err := canonicalScenarioV2Dir(scenarioPath)
	if err != nil {
		return "", wrapScenarioV2Error("scenario", err)
	}
	return resolveScenarioV2FixturePathFromRoot(root, reference)
}

// ResolveScenarioFixturePath is a descriptive alias for
// ResolveScenarioV2FixturePath.
func ResolveScenarioFixturePath(scenarioPath, reference string) (string, error) {
	return ResolveScenarioV2FixturePath(scenarioPath, reference)
}

// OpenScenarioV2Fixture resolves and opens one contained fixture reference.
func OpenScenarioV2Fixture(scenarioPath, reference string) (io.ReadCloser, error) {
	path, err := ResolveScenarioV2FixturePath(scenarioPath, reference)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open fixture %q: %w", path, err)
	}
	resolved, resolveErr := ResolveScenarioV2FixturePath(scenarioPath, reference)
	if resolveErr != nil || resolved != path {
		_ = file.Close()
		if resolveErr != nil {
			return nil, resolveErr
		}
		return nil, newScenarioV2Error("fixture", "fixture target changed during open")
	}
	return file, nil
}
