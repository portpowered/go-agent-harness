package agentprofile_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentprofile"
)

func TestCatalogGoldenAndOneToOne(t *testing.T) {
	var golden struct {
		Profiles []struct {
			Name, Instructions string
			Expected           agentprofile.ExpectedOutcome
		} `json:"profiles"`
	}
	data, err := os.ReadFile(filepath.Join("testdata", "catalog.golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	loader := agentprofile.NewLoader(realCatalogFS(t))
	names, err := loader.Names()
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	wantNames := make([]string, len(golden.Profiles))
	for i := range golden.Profiles {
		wantNames[i] = golden.Profiles[i].Name
	}
	if len(names) != 5 || !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("catalog names = %v, want exactly sorted names %v", names, wantNames)
	}
	profiles, err := loader.Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(profiles) != len(names) {
		t.Fatalf("loaded profile count = %d, want %d", len(profiles), len(names))
	}
	for i, want := range golden.Profiles {
		got := profiles[i]
		if got.Name != want.Name || got.Instructions != want.Instructions || !reflect.DeepEqual(got.ExpectedOutcome, want.Expected) {
			t.Fatalf("profile %q = %+v, want name=%q instructions=%q outcome=%+v", want.Name, got, want.Name, want.Instructions, want.Expected)
		}
	}
}

func TestLoadUnknownNamesAreTypedAndReturnZero(t *testing.T) {
	loader := agentprofile.NewLoader(profileFS("instructions", validOutcome))
	for _, name := range []string{"", "missing", "../shell-command", "shell/command", `C:\\shell-command`, "/shell-command"} {
		profile, err := loader.Load(name)
		var unknown *agentprofile.UnknownProfileError
		if err == nil || !errors.As(err, &unknown) || !errors.Is(err, agentprofile.ErrUnknownProfile) || unknown.Name != name || strings.TrimSpace(unknown.Reason) == "" {
			t.Fatalf("Load(%q) = profile=%+v error=%T %v, want typed unknown and zero profile", name, profile, err, err)
		}
		if !reflect.DeepEqual(profile, agentprofile.Profile{}) {
			t.Fatalf("Load(%q) returned usable profile %+v", name, profile)
		}
	}
}

func TestLoadMalformedCasesAreTypedAndReturnZero(t *testing.T) {
	const instructions = "use no tools"
	cases := map[string]map[string]string{
		"empty instructions":      {"profile/AGENTS.md": " \n", "profile/expected-outcome.json": validOutcome},
		"malformed declaration":   {"profile/AGENTS.md": instructions, "profile/expected-outcome.json": `{"kind":`},
		"missing declaration":     {"profile/AGENTS.md": instructions},
		"duplicate declarations":  {"profile/AGENTS.md": instructions, "profile/expected-outcome.json": validOutcome, "profile/expected.json": validOutcome},
		"invalid outcome kind":    {"profile/AGENTS.md": instructions, "profile/expected-outcome.json": `{"kind":"network"}`},
		"empty command":           {"profile/AGENTS.md": instructions, "profile/expected-outcome.json": `{"kind":"shell-command","command":""}`},
		"unsafe target":           {"profile/AGENTS.md": instructions, "profile/expected-outcome.json": `{"kind":"file-read","target_file":"../secret"}`},
		"wrong ordered count":     {"profile/AGENTS.md": instructions, "profile/expected-outcome.json": `{"kind":"ordered-multi-tool","ordered_calls":["file_read"],"first_result_informs_second":true}`},
		"non-zero no-tools count": {"profile/AGENTS.md": instructions, "profile/expected-outcome.json": `{"kind":"no-tools","call_count":1}`},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			profile, err := agentprofile.Load(mapFS(files), "profile")
			var malformed *agentprofile.MalformedProfileError
			if err == nil || !errors.As(err, &malformed) || !errors.Is(err, agentprofile.ErrMalformedProfile) || malformed.Profile != "profile" || strings.TrimSpace(malformed.Reason) == "" {
				t.Fatalf("error = %T %v, want typed profile diagnostic", err, err)
			}
			if !reflect.DeepEqual(profile, agentprofile.Profile{}) {
				t.Fatalf("malformed load returned usable profile %+v", profile)
			}
		})
	}
}

func TestCatalogRejectsOrphanDeclaration(t *testing.T) {
	_, err := agentprofile.NewLoader(mapFS(map[string]string{"expected-outcome.json": validOutcome})).Catalog()
	var malformed *agentprofile.MalformedProfileError
	if err == nil || !errors.As(err, &malformed) || !errors.Is(err, agentprofile.ErrMalformedProfile) {
		t.Fatalf("orphan declaration error = %T %v, want typed malformed error", err, err)
	}
}

const validOutcome = `{"kind":"no-tools","call_count":0}`

func profileFS(instructions, outcome string) fstest.MapFS {
	return mapFS(map[string]string{"profile/AGENTS.md": instructions, "profile/expected-outcome.json": outcome})
}

func mapFS(files map[string]string) fstest.MapFS {
	result := fstest.MapFS{}
	for name, data := range files {
		result[name] = &fstest.MapFile{Data: []byte(data)}
	}
	return result
}

func realCatalogFS(t *testing.T) fs.FS {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	return os.DirFS(filepath.Join(root, "agent-cli", "testdata", "agents-profiles"))
}
