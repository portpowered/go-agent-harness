package fleet

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeScenario(t *testing.T, dir, id string) string {
	t.Helper()
	path := filepath.Join(dir, id+".scenario.json")
	body := `{"id":"` + id + `","name":"` + id + `","steps":[],"expectations":[]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	return path
}

func TestComposeExpandsEveryCoordinateInDeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	second := writeScenario(t, dir, "second")
	first := writeScenario(t, dir, "first")

	manifest, err := Compose(ComposeInput{
		ScenarioFiles: []string{second, first},
		Transports:    []Transport{TransportLive, TransportReplay},
		RepeatCount:   3,
		Concurrency:   2,
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if manifest.SchemaVersion != SchemaVersion || manifest.EntryCount() != 12 {
		t.Fatalf("manifest shape = version %d, entries %d; want version %d, entries 12", manifest.SchemaVersion, manifest.EntryCount(), SchemaVersion)
	}
	wantScenarios := []string{"first", "second"}
	wantTransports := []Transport{TransportLive, TransportReplay}
	for index, scenario := range wantScenarios {
		for transportIndex, transport := range wantTransports {
			for repeat := 0; repeat < 3; repeat++ {
				entry := manifest.Entries[index*len(wantTransports)*3+transportIndex*3+repeat]
				wantID := EntryID(scenario, transport, repeat)
				if entry.ID != wantID || entry.ScenarioID != scenario || entry.Transport != transport || entry.RepeatIndex != repeat {
					t.Fatalf("entry = %+v, want ID %q and coordinates %s/%s/%d", entry, wantID, scenario, transport, repeat)
				}
			}
		}
	}

	reordered, err := Compose(ComposeInput{
		ScenarioFiles: []string{first, second},
		Transports:    []Transport{TransportReplay, TransportLive},
		RepeatCount:   3,
		Concurrency:   2,
	})
	if err != nil {
		t.Fatalf("Compose reordered: %v", err)
	}
	if !reflect.DeepEqual(manifest, reordered) {
		t.Fatalf("equivalent input order changed manifest:\nfirst=%+v\nsecond=%+v", manifest, reordered)
	}
}

func TestComposeRejectsUnknownTransportWithTypedFieldError(t *testing.T) {
	path := writeScenario(t, t.TempDir(), "scenario")
	_, err := Compose(ComposeInput{
		ScenarioFiles: []string{path},
		Transports:    []Transport{"serial"},
		RepeatCount:   1,
		Concurrency:   1,
	})
	if err == nil {
		t.Fatal("unknown transport unexpectedly composed")
	}
	if !errors.Is(err, ErrUnknownTransport) {
		t.Fatalf("error = %v, want ErrUnknownTransport", err)
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "transports[0]" || !strings.Contains(err.Error(), "serial") {
		t.Fatalf("error = %v, typed field error = %+v", err, validationErr)
	}
}

func TestComposeRejectsNonPositiveRepeatAndConcurrencyFields(t *testing.T) {
	path := writeScenario(t, t.TempDir(), "scenario")
	tests := []struct {
		name  string
		in    ComposeInput
		want  error
		field string
	}{
		{name: "zero repeats", in: ComposeInput{ScenarioFiles: []string{path}, Transports: []Transport{TransportReplay}, RepeatCount: 0, Concurrency: 1}, want: ErrInvalidRepeatCount, field: "repeat_count"},
		{name: "negative repeats", in: ComposeInput{ScenarioFiles: []string{path}, Transports: []Transport{TransportReplay}, RepeatCount: -1, Concurrency: 1}, want: ErrInvalidRepeatCount, field: "repeat_count"},
		{name: "zero concurrency", in: ComposeInput{ScenarioFiles: []string{path}, Transports: []Transport{TransportReplay}, RepeatCount: 1, Concurrency: 0}, want: ErrInvalidConcurrency, field: "concurrency"},
		{name: "negative concurrency", in: ComposeInput{ScenarioFiles: []string{path}, Transports: []Transport{TransportReplay}, RepeatCount: 1, Concurrency: -1}, want: ErrInvalidConcurrency, field: "concurrency"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compose(test.in)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) || validationErr.Field != test.field {
				t.Fatalf("error = %v, validation = %+v, want field %q", err, validationErr, test.field)
			}
		})
	}
}

func TestManifestRoundTripsAndRejectsMissingEntries(t *testing.T) {
	path := writeScenario(t, t.TempDir(), "scenario")
	manifest, err := ComposeFiles([]string{path}, []string{"replay"}, 2, 1)
	if err != nil {
		t.Fatalf("ComposeFiles: %v", err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	decoded, err := ParseManifest(data)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if !reflect.DeepEqual(manifest, decoded) {
		t.Fatalf("round trip changed manifest: got %+v, want %+v", decoded, manifest)
	}
	decoded.Entries = decoded.Entries[:1]
	if err := decoded.Validate(); !errors.Is(err, ErrInvalidManifest) || !strings.Contains(err.Error(), "exactly 2 entries") {
		t.Fatalf("truncated manifest error = %v", err)
	}
}

func TestComposeRejectsMalformedScenarioIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.scenario.json")
	if err := os.WriteFile(path, []byte(`{"steps":[]}`), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	_, err := Compose(ComposeInput{ScenarioFiles: []string{path}, Transports: []Transport{TransportReplay}, RepeatCount: 1, Concurrency: 1})
	if err == nil || !strings.Contains(err.Error(), "scenario.id") {
		t.Fatalf("error = %v, want scenario identity error", err)
	}
}
