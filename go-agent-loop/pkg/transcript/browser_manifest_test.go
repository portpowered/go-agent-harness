package transcript

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRecordingBundlePairsBrowserEvidenceInV2Manifest(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "recording")
	data := []byte(`{"version":"webmcp.browser-events.v1","sequence":1,"monotonic_ms":0,"type":"browser.discovery.started","payload":{},"redaction":{"mode":"none"}}
`)
	artifact := BrowserArtifact{
		Format: BrowserEventsVersion,
		Data:   data,
		Redaction: BrowserRedactionPolicy{
			URLQuery:           true,
			URLFragment:        true,
			ToolArguments:      []string{"write_secret", "read_state"},
			ResultJSONPointers: []string{"/z", "/a~1b"},
			DigestTools:        []string{"digest_tool"},
		},
	}

	if err := WriteRecordingBundle(RecordingConfig{
		Destination:      destination,
		ClientTranscript: []byte("client transcript\n"),
		AgentTranscript:  []byte("agent transcript\n"),
		BrowserArtifact:  &artifact,
	}); err != nil {
		t.Fatalf("WriteRecordingBundle: %v", err)
	}

	entries := recordingEntries(t, destination)
	if !equalStrings(entries, []string{
		"agent.transcript.jsonl",
		"audio",
		"browser.events.jsonl",
		"client.transcript.jsonl",
		"manifest.json",
	}) {
		t.Fatalf("bundle entries = %v, want one browser artifact and one manifest", entries)
	}
	if _, err := os.Stat(filepath.Join(destination, "browser", "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("browser child manifest stat error = %v, want absent", err)
	}

	dataOnDisk := readBundleFile(t, destination, BrowserArtifactDefaultPath)
	if !bytes.Equal(dataOnDisk, data) {
		t.Fatalf("browser artifact bytes changed: got %q want %q", dataOnDisk, data)
	}
	digest := sha256.Sum256(data)
	wantDigest := hex.EncodeToString(digest[:])

	manifestBytes := readBundleFile(t, destination, "manifest.json")
	var manifest RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.FormatVersion != RecordingManifestV2Version {
		t.Fatalf("format_version = %d, want %d", manifest.FormatVersion, RecordingManifestV2Version)
	}
	if manifest.Browser == nil {
		t.Fatal("manifest omitted browser evidence")
	}
	if manifest.Browser.Format != BrowserEventsVersion || manifest.Browser.Artifact.Path != BrowserArtifactDefaultPath || manifest.Browser.Artifact.SHA256 != wantDigest {
		t.Fatalf("browser manifest entry = %+v, want format/path/hash", manifest.Browser)
	}
	if got := manifest.Browser.Redaction.ToolArguments; !equalStrings(got, []string{"read_state", "write_secret"}) {
		t.Fatalf("tool argument policy = %v, want normalized ordering", got)
	}
	if got := manifest.Browser.Redaction.ResultJSONPointers; !equalStrings(got, []string{"/a~1b", "/z"}) {
		t.Fatalf("result pointer policy = %v, want normalized ordering", got)
	}
	matching := 0
	for _, entry := range manifest.Artifacts {
		if entry.Path == BrowserArtifactDefaultPath {
			matching++
			if entry.SHA256 != wantDigest {
				t.Fatalf("top-level browser hash = %s, want %s", entry.SHA256, wantDigest)
			}
		}
	}
	if matching != 1 {
		t.Fatalf("top-level browser artifact occurrences = %d, want exactly one", matching)
	}

	const wantBrowser = `{"format":"webmcp.browser-events.v1","artifact":{"path":"browser.events.jsonl","sha256":"` + "PLACEHOLDER" + `"},"redaction":{"url_query":true,"url_fragment":true,"tool_arguments":["read_state","write_secret"],"result_json_pointers":["/a~1b","/z"],"digest_tools":["digest_tool"],"raw_cdp":false}}`
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(manifestBytes, &fields); err != nil {
		t.Fatalf("decode manifest fields: %v", err)
	}
	gotBrowser := string(fields["browser"])
	if gotBrowser != strings.Replace(wantBrowser, "PLACEHOLDER", wantDigest, 1) {
		t.Fatalf("browser manifest JSON = %s, want deterministic shape", gotBrowser)
	}
}

func TestWriteRecordingBundleAllowsProviderOnlyV2Manifest(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "recording")
	if err := WriteRecordingBundle(RecordingConfig{
		Destination:      destination,
		ClientTranscript: []byte("client\n"),
		AgentTranscript:  []byte("agent\n"),
		ManifestVersion:  RecordingManifestV2Version,
	}); err != nil {
		t.Fatalf("WriteRecordingBundle: %v", err)
	}
	manifestBytes := readBundleFile(t, destination, "manifest.json")
	if bytes.Contains(manifestBytes, []byte(`"browser"`)) {
		t.Fatalf("provider-only v2 manifest unexpectedly contains browser: %s", manifestBytes)
	}
	var manifest RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode provider-only v2 manifest: %v", err)
	}
	if manifest.FormatVersion != RecordingManifestV2Version || manifest.Browser != nil {
		t.Fatalf("provider-only manifest = %+v, want v2 without browser", manifest)
	}
}

func TestRecordingManifestRejectsUnsupportedOrInconsistentBrowserMetadata(t *testing.T) {
	valid := validBrowserManifest()
	tests := []struct {
		name string
		edit func(*RecordingManifest)
		want error
	}{
		{
			name: "unknown version",
			edit: func(manifest *RecordingManifest) { manifest.FormatVersion = 9 },
			want: ErrUnknownRecordingManifestVersion,
		},
		{
			name: "browser under v1",
			edit: func(manifest *RecordingManifest) { manifest.FormatVersion = RecordingManifestV1Version },
			want: ErrInvalidRecordingManifest,
		},
		{
			name: "browser artifact omitted",
			edit: func(manifest *RecordingManifest) { manifest.Artifacts = nil },
			want: ErrInvalidRecordingManifest,
		},
		{
			name: "browser hash mismatch",
			edit: func(manifest *RecordingManifest) { manifest.Browser.Artifact.SHA256 = strings.Repeat("b", 64) },
			want: ErrInvalidRecordingManifest,
		},
		{
			name: "duplicate top-level artifact",
			edit: func(manifest *RecordingManifest) {
				manifest.Artifacts = append(manifest.Artifacts, manifest.Artifacts[0])
			},
			want: ErrInvalidRecordingManifest,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			manifest := cloneBrowserManifest(valid)
			testCase.edit(&manifest)
			_, err := json.Marshal(manifest)
			if err == nil {
				t.Fatal("json.Marshal unexpectedly accepted invalid manifest")
			}
			if !errors.Is(err, testCase.want) {
				t.Fatalf("marshal error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestRecordingManifestJSONRejectsStrictBrowserShapes(t *testing.T) {
	base := `{"format_version":2,"input_device":{},"output_device":{},"transport":"replay","model":"fixture","clock_base":"fake:0","artifacts":[{"path":"browser.events.jsonl","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"browser":{"format":"webmcp.browser-events.v1","artifact":{"path":"browser.events.jsonl","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"redaction":{"url_query":false,"url_fragment":false,"tool_arguments":[],"result_json_pointers":[],"digest_tools":[],"raw_cdp":false}}}`
	tests := []struct {
		name string
		data string
	}{
		{name: "browser unknown field", data: strings.Replace(base, `"redaction":{`, `"extra":true,"redaction":{`, 1)},
		{name: "policy unknown field", data: strings.Replace(base, `"raw_cdp":false}}}`, `"raw_cdp":false,"extra":true}}}`, 1)},
		{name: "policy missing field", data: strings.Replace(base, `,"raw_cdp":false}}}`, `}}}`, 1)},
		{name: "browser null under v1", data: strings.Replace(strings.Replace(base, `"format_version":2`, `"format_version":1`, 1), `"browser":{`, `"browser":null`, 1)},
		{name: "unsafe browser path", data: strings.Replace(base, `browser.events.jsonl`, `../browser.events.jsonl`, 2)},
		{name: "uppercase browser hash", data: strings.Replace(base, strings.Repeat("a", 64), strings.Repeat("A", 64), 2)},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			var manifest RecordingManifest
			if err := json.Unmarshal([]byte(testCase.data), &manifest); err == nil {
				t.Fatal("json.Unmarshal unexpectedly accepted invalid browser manifest")
			}
		})
	}
}

func TestWriteRecordingBundleRejectsUnsafeBrowserArtifactBeforeCommit(t *testing.T) {
	validData := []byte(`{"version":"webmcp.browser-events.v1","sequence":1}
`)
	tests := []struct {
		name     string
		artifact BrowserArtifact
		config   func(RecordingConfig) RecordingConfig
		want     error
	}{
		{
			name: "hash mismatch",
			artifact: BrowserArtifact{
				Format: BrowserEventsVersion,
				Data:   validData,
				SHA256: strings.Repeat("0", 64),
			},
			want: ErrInvalidRecording,
		},
		{
			name: "unsafe path",
			artifact: BrowserArtifact{
				Format: BrowserEventsVersion,
				Path:   "../browser.events.jsonl",
				Data:   validData,
			},
			want: ErrInvalidRecording,
		},
		{
			name: "raw cdp policy",
			artifact: BrowserArtifact{
				Format: BrowserEventsVersion,
				Data:   validData,
				Redaction: BrowserRedactionPolicy{
					RawCDP: true,
				},
			},
			want: ErrInvalidRecording,
		},
		{
			name: "credential survives",
			artifact: BrowserArtifact{
				Format: BrowserEventsVersion,
				Data: []byte(`{"message":"browser-secret"}
`),
			},
			config: func(config RecordingConfig) RecordingConfig {
				config.Credentials = []string{"browser-secret"}
				return config
			},
			want: ErrRecordingUnsafeArtifact,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "recording")
			config := RecordingConfig{
				Destination:      destination,
				ClientTranscript: []byte("client\n"),
				AgentTranscript:  []byte("agent\n"),
				BrowserArtifact:  &testCase.artifact,
			}
			if testCase.config != nil {
				config = testCase.config(config)
			}
			err := WriteRecordingBundle(config)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed destination stat error = %v, want absent", statErr)
			}
		})
	}
}

func TestWriteRecordingBundleBrowserWriteFailureLeavesNoPartialDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "recording")
	data := []byte(`{"version":"webmcp.browser-events.v1","sequence":1}
`)
	config := RecordingConfig{
		Destination:      destination,
		ClientTranscript: []byte("client\n"),
		AgentTranscript:  []byte("agent\n"),
		BrowserArtifact: &BrowserArtifact{
			Format: BrowserEventsVersion,
			Data:   data,
		},
		WriteFile: func(path string, data []byte, mode os.FileMode) (int, error) {
			if strings.HasSuffix(path, BrowserArtifactDefaultPath) {
				return 0, ErrRecordingDiskFull
			}
			if err := os.WriteFile(path, data, mode); err != nil {
				return 0, err
			}
			return len(data), nil
		},
	}
	err := WriteRecordingBundle(config)
	if !errors.Is(err, ErrRecordingWrite) || !errors.Is(err, ErrRecordingDiskFull) {
		t.Fatalf("error = %v, want recording write and disk-full identities", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed destination stat error = %v, want absent", statErr)
	}
}

func validBrowserManifest() RecordingManifest {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return RecordingManifest{
		FormatVersion: RecordingManifestV2Version,
		Artifacts:     []ArtifactHash{{Path: BrowserArtifactDefaultPath, SHA256: digest}},
		Browser: &BrowserManifest{
			Format:   BrowserEventsVersion,
			Artifact: ArtifactHash{Path: BrowserArtifactDefaultPath, SHA256: digest},
			Redaction: BrowserRedactionPolicy{
				ToolArguments:      []string{},
				ResultJSONPointers: []string{},
				DigestTools:        []string{},
			},
		},
	}
}

func cloneBrowserManifest(manifest RecordingManifest) RecordingManifest {
	clone := manifest
	clone.Artifacts = append([]ArtifactHash(nil), manifest.Artifacts...)
	if manifest.Browser != nil {
		browser := *manifest.Browser
		browser.Redaction.ToolArguments = append([]string(nil), manifest.Browser.Redaction.ToolArguments...)
		browser.Redaction.ResultJSONPointers = append([]string(nil), manifest.Browser.Redaction.ResultJSONPointers...)
		browser.Redaction.DigestTools = append([]string(nil), manifest.Browser.Redaction.DigestTools...)
		clone.Browser = &browser
	}
	return clone
}

func ExampleRecordingManifest_v2() {
	manifest := validBrowserManifest()
	data, err := json.Marshal(manifest)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(data))
	// Output:
	// {"format_version":2,"input_device":{},"output_device":{},"transport":"","model":"","clock_base":"","artifacts":[{"path":"browser.events.jsonl","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"browser":{"format":"webmcp.browser-events.v1","artifact":{"path":"browser.events.jsonl","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"redaction":{"url_query":false,"url_fragment":false,"tool_arguments":[],"result_json_pointers":[],"digest_tools":[],"raw_cdp":false}}}
}
