package probe

import (
	"encoding/json"
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
	root, err := decodeScenarioV2Root(input)
	if err != nil {
		return ScenarioV2{}, err
	}
	result, err := decodeScenarioV2Header(root)
	if err != nil {
		return ScenarioV2{}, err
	}
	result.SourcePath = scenarioPath
	hasFixtures := result.BrowserFixture != "" || result.ProviderFixture != ""
	if result.FixtureRoot, err = scenarioV2FixtureRoot(scenarioPath, hasFixtures); err != nil {
		return ScenarioV2{}, err
	}
	stepValues, err := requiredScenarioV2Array(root, "steps", "step")
	if err != nil {
		return ScenarioV2{}, err
	}
	expectationValues, err := requiredScenarioV2Array(root, "expectations", "expectation")
	if err != nil {
		return ScenarioV2{}, err
	}
	var lookup CorpusLookup
	if len(lookups) == 1 {
		lookup = lookups[0]
	}
	result.Steps = make([]ScenarioV2Step, len(stepValues))
	for index, raw := range stepValues {
		if result.Steps[index], err = parseScenarioV2Step(raw, index, lookup, result.FixtureRoot); err != nil {
			return ScenarioV2{}, err
		}
	}
	result.Expectations = make([]ScenarioV2Expectation, len(expectationValues))
	for index, raw := range expectationValues {
		if result.Expectations[index], err = parseScenarioV2Expectation(raw, index); err != nil {
			return ScenarioV2{}, err
		}
	}
	if err := resolveScenarioV2Fixtures(&result); err != nil {
		return ScenarioV2{}, err
	}
	return result, nil
}

// decodeScenarioV2Root reads one UTF-8 JSON object and rejects unknown root
// fields.
func decodeScenarioV2Root(input any) (scenarioV2Object, error) {
	data, err := readInput(input)
	if err != nil {
		return nil, newScenarioV2Error("document", "%v", err)
	}
	if !utf8.Valid(data) {
		return nil, newScenarioV2Error("document", "input is not valid UTF-8")
	}
	root, err := decodeScenarioV2Object(data, "scenario")
	if err != nil {
		return nil, err
	}
	if err := rejectScenarioV2Fields(root, scenarioV2RootFields, "scenario"); err != nil {
		return nil, err
	}
	return root, nil
}

// decodeScenarioV2Header decodes the scalar root fields in document order.
func decodeScenarioV2Header(root scenarioV2Object) (ScenarioV2, error) {
	version, err := requiredScenarioV2String(root, "scenario", "schema_version")
	if err != nil {
		return ScenarioV2{}, err
	}
	if version != ScenarioV2Version {
		return ScenarioV2{}, newScenarioV2Error("scenario.schema_version", "unsupported version")
	}
	result := ScenarioV2{SchemaVersion: version}
	if result.ID, err = requiredScenarioV2String(root, "scenario", "id"); err != nil {
		return ScenarioV2{}, err
	}
	if result.Name, err = optionalScenarioV2String(root, "scenario", "name"); err != nil {
		return ScenarioV2{}, err
	}
	if result.Description, err = optionalScenarioV2String(root, "scenario", "description"); err != nil {
		return ScenarioV2{}, err
	}
	if result.BrowserFixture, err = optionalScenarioV2String(root, "scenario", "browser_fixture"); err != nil {
		return ScenarioV2{}, err
	}
	if result.ProviderFixture, err = optionalScenarioV2String(root, "scenario", "provider_fixture"); err != nil {
		return ScenarioV2{}, err
	}
	if err := rejectBlankScenarioV2Fixture(root, "browser_fixture", result.BrowserFixture); err != nil {
		return ScenarioV2{}, err
	}
	if err := rejectBlankScenarioV2Fixture(root, "provider_fixture", result.ProviderFixture); err != nil {
		return ScenarioV2{}, err
	}
	return result, nil
}

func rejectBlankScenarioV2Fixture(root scenarioV2Object, fieldName, reference string) error {
	if _, exists := root[fieldName]; exists && strings.TrimSpace(reference) == "" {
		return &ScenarioV2Error{Path: "scenario." + fieldName, Cause: ErrScenarioV2FixturePath}
	}
	return nil
}

// scenarioV2FixtureRoot canonicalizes the scenario directory. The directory
// is mandatory only when the document references fixtures.
func scenarioV2FixtureRoot(scenarioPath string, hasFixtures bool) (string, error) {
	if strings.TrimSpace(scenarioPath) == "" {
		if hasFixtures {
			return "", newScenarioV2Error("scenario", "scenario path is required when fixture references are present")
		}
		return "", nil
	}
	fixtureRoot, err := canonicalScenarioV2Dir(scenarioPath)
	if err != nil && hasFixtures {
		return "", wrapScenarioV2Error("scenario", err)
	}
	return fixtureRoot, nil
}

func requiredScenarioV2Array(root scenarioV2Object, fieldName, noun string) ([]json.RawMessage, error) {
	location := "scenario." + fieldName
	raw, ok := root[fieldName]
	if !ok {
		return nil, newScenarioV2Error(location, "required field is missing")
	}
	values, err := scenarioV2Array(raw, location)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, newScenarioV2Error(location, "must contain at least one %s", noun)
	}
	return values, nil
}

func resolveScenarioV2Fixtures(result *ScenarioV2) error {
	var err error
	if result.BrowserFixture != "" {
		if result.BrowserFixturePath, err = resolveScenarioV2FixturePathFromRoot(result.FixtureRoot, result.BrowserFixture); err != nil {
			return wrapScenarioV2Error("scenario.browser_fixture", err)
		}
	}
	if result.ProviderFixture != "" {
		if result.ProviderFixturePath, err = resolveScenarioV2FixturePathFromRoot(result.FixtureRoot, result.ProviderFixture); err != nil {
			return wrapScenarioV2Error("scenario.provider_fixture", err)
		}
	}
	return nil
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
		discardRejectedFixture(file)
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
		discardRejectedFixture(file)
		if resolveErr != nil {
			return nil, resolveErr
		}
		return nil, newScenarioV2Error("fixture", "fixture target changed during open")
	}
	return file, nil
}

// discardRejectedFixture closes a fixture file the loader is rejecting. The
// rejection is the reported outcome; a close failure on the abandoned
// read-only handle cannot change it.
func discardRejectedFixture(file *os.File) {
	if err := file.Close(); err != nil {
		return
	}
}
