package transcript

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	manifestTestPassword = "media-password-7f5f"
	manifestTestAPIKey   = "api-key-2c9b"
)

var updateManifestGolden = flag.Bool("update-recording-manifest-golden", false, "print the deterministic recording manifest golden")

func TestWriteRecordingBundleLayoutManifestAndRedaction(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "recording")
	input := []byte{0x00, 0x01, 0x7f, 0xff}
	output := []byte{0x10, 0x20, 0x30, 0x40}
	clientTranscript := []byte(`{"peer":"client","message":"key=api-key-2c9b api-key-2c9b"}` + "\n")
	agentTranscript := []byte(`{"peer":"agent","source":"rtsp://operator:media-password-7f5f@camera.example/live","message":"media-password-7f5f"}` + "\n")
	config := RecordingConfig{
		Destination:      destination,
		ClientTranscript: clientTranscript,
		AgentTranscript:  agentTranscript,
		InputSegments:    [][]byte{input},
		OutputSegments:   [][]byte{output},
		Credentials:      []string{manifestTestPassword, manifestTestAPIKey},
		Metadata: RecordingMetadata{
			InputDevice: DeviceMetadata{
				ID: "mic-01", Name: "Desk Mic", Driver: "virtual", SampleRateHz: 16000, Channels: 1,
			},
			OutputDevice: DeviceMetadata{
				ID: "speaker-02", Name: "Desk Speaker", Driver: "virtual", SampleRateHz: 24000, Channels: 2,
			},
			Transport: "websocket",
			Model:     "speech-model-fixed",
			ClockBase: "2026-01-02T03:04:05.000000000Z",
			MediaSource: &MediaSourceMetadata{
				URL:  "rtsp://operator:" + manifestTestPassword + "@camera.example/live",
				Name: "front-door",
			},
			Configuration: map[string]string{
				"api_key": manifestTestAPIKey,
				"region":  "us-test-1",
			},
		},
		Corpus: []CorpusHash{
			{Path: "z-last.pcm", SHA256: strings.Repeat("B", 64)},
			{Path: "a-first.pcm", SHA256: strings.Repeat("A", 64)},
		},
	}

	if err := WriteRecordingBundle(config); err != nil {
		t.Fatalf("WriteRecordingBundle: %v", err)
	}

	entries := recordingEntries(t, destination)
	wantEntries := []string{
		"agent.transcript.jsonl",
		"audio",
		"audio/in-000.pcm",
		"audio/out-000.pcm",
		"client.transcript.jsonl",
		"manifest.json",
	}
	if !equalStrings(entries, wantEntries) {
		t.Fatalf("bundle entries = %v, want exactly %v", entries, wantEntries)
	}

	if got := readBundleFile(t, destination, "audio/in-000.pcm"); !bytes.Equal(got, input) {
		t.Fatalf("input PCM = %x, want %x", got, input)
	}
	if got := readBundleFile(t, destination, "audio/out-000.pcm"); !bytes.Equal(got, output) {
		t.Fatalf("output PCM = %x, want %x", got, output)
	}
	for _, path := range []string{"client.transcript.jsonl", "agent.transcript.jsonl", "audio/in-000.pcm", "audio/out-000.pcm"} {
		if data := readBundleFile(t, destination, path); len(data) == 0 {
			t.Fatalf("%s is empty", path)
		}
	}

	manifestBytes := readBundleFile(t, destination, "manifest.json")
	var manifest RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.FormatVersion != RecordingManifestVersion {
		t.Fatalf("manifest format_version = %d, want %d", manifest.FormatVersion, RecordingManifestVersion)
	}
	if manifest.Terminal != nil {
		t.Fatalf("legacy manifest unexpectedly contains terminal summary: %+v", manifest.Terminal)
	}
	if manifest.MediaSource == nil || !strings.Contains(manifest.MediaSource.URL, RecordingRedactionMarker) {
		t.Fatalf("manifest media source URL = %#v, want visible redaction", manifest.MediaSource)
	}
	if manifest.Configuration["api_key"] != RecordingRedactionMarker {
		t.Fatalf("manifest api_key = %q, want %q", manifest.Configuration["api_key"], RecordingRedactionMarker)
	}
	if len(manifest.Corpus) != 2 || manifest.Corpus[0].Path != "a-first.pcm" || manifest.Corpus[1].Path != "z-last.pcm" {
		t.Fatalf("manifest corpus ordering = %#v, want stable path order", manifest.Corpus)
	}
	if len(manifest.Artifacts) != 4 {
		t.Fatalf("manifest artifacts = %d, want four non-manifest artifacts", len(manifest.Artifacts))
	}
	for _, artifact := range manifest.Artifacts {
		data := readBundleFile(t, destination, artifact.Path)
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
			t.Errorf("hash for %s = %s, want %s", artifact.Path, artifact.SHA256, got)
		}
	}
	for _, path := range wantEntries {
		if path == "audio" {
			continue
		}
		data := readBundleFile(t, destination, path)
		if bytes.Contains(data, []byte(manifestTestPassword)) || bytes.Contains(data, []byte(manifestTestAPIKey)) {
			t.Errorf("%s contains a configured credential", path)
		}
	}
	if !bytes.Contains(manifestBytes, []byte(RecordingRedactionMarker)) {
		t.Fatal("manifest has no visible redaction marker")
	}

	const wantGolden = `{"format_version":1,"input_device":{"id":"mic-01","name":"Desk Mic","driver":"virtual","sample_rate_hz":16000,"channels":1},"output_device":{"id":"speaker-02","name":"Desk Speaker","driver":"virtual","sample_rate_hz":24000,"channels":2},"transport":"websocket","model":"speech-model-fixed","clock_base":"2026-01-02T03:04:05.000000000Z","media_source":{"url":"rtsp://operator:REDACTED@camera.example/live","name":"front-door"},"configuration":{"api_key":"REDACTED","region":"us-test-1"},"corpus":[{"path":"a-first.pcm","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"path":"z-last.pcm","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}],"artifacts":[{"path":"client.transcript.jsonl","sha256":"886c3af4c218f02b7b38349fadef6be01ba88df369e9c28c3f494ab8615b541f"},{"path":"agent.transcript.jsonl","sha256":"64363afc74e658d57bbc5be15417c9157d58b15f0b75ed537100030ecee75a69"},{"path":"audio/in-000.pcm","sha256":"9beb9b4fbb3161c1c60d01c253b504f0dd2ea909f764fd3d7c8213fa1580ae94"},{"path":"audio/out-000.pcm","sha256":"f4e3f0b04771c047e227c9ecaba65d3fe2fd0e1eee0a7552b956d1a7c535a7cf"}]}\n`
	if *updateManifestGolden {
		t.Fatalf("manifest golden update requested; replace wantGolden with:\n%s", manifestBytes)
	}
	golden := strings.TrimSuffix(wantGolden, `\n`) + "\n"
	if string(manifestBytes) != golden {
		t.Fatalf("manifest golden mismatch:\n%s", manifestBytes)
	}
}

func TestWriteRecordingBundlePartialStatusDescribesOneSidedEvidence(t *testing.T) {
	const recordingFailure = "recording sink credential-123 became unavailable"
	tests := []struct {
		name        string
		client      []byte
		agent       []byte
		wantPath    string
		missingPath string
	}{
		{
			name:        "client only",
			client:      []byte("client evidence\n"),
			wantPath:    "client.transcript.jsonl",
			missingPath: "agent.transcript.jsonl",
		},
		{
			name:        "agent only",
			agent:       []byte("agent evidence\n"),
			wantPath:    "agent.transcript.jsonl",
			missingPath: "client.transcript.jsonl",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "recording")
			if err := WriteRecordingBundle(RecordingConfig{
				Destination:      destination,
				ClientTranscript: testCase.client,
				AgentTranscript:  testCase.agent,
				Credentials:      []string{"credential-123"},
				RecordingStatus:  &RecordingStatus{State: RecordingStatusPartial, Reason: recordingFailure},
			}); err != nil {
				t.Fatalf("WriteRecordingBundle: %v", err)
			}

			if got := readBundleFile(t, destination, testCase.wantPath); !bytes.Equal(got, append(testCase.client, testCase.agent...)) {
				t.Fatalf("%s = %q, want recorded evidence", testCase.wantPath, got)
			}
			if _, err := os.Stat(filepath.Join(destination, testCase.missingPath)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing transcript stat error = %v, want absent", err)
			}

			manifestBytes := readBundleFile(t, destination, "manifest.json")
			var manifest RecordingManifest
			if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
				t.Fatalf("decode partial manifest: %v", err)
			}
			if manifest.RecordingStatus == nil {
				t.Fatal("partial manifest omitted recording_status")
			}
			if manifest.RecordingStatus.State != RecordingStatusPartial {
				t.Fatalf("recording_status.state = %q, want %q", manifest.RecordingStatus.State, RecordingStatusPartial)
			}
			if manifest.RecordingStatus.Reason != "recording sink REDACTED became unavailable" {
				t.Fatalf("recording_status.reason = %q, want redacted reason", manifest.RecordingStatus.Reason)
			}
			if bytes.Contains(manifestBytes, []byte("credential-123")) {
				t.Fatal("partial manifest contains the configured credential")
			}
			if got := manifestArtifactPaths(manifest); !equalStrings(got, []string{testCase.wantPath}) {
				t.Fatalf("manifest artifacts = %v, want only %s", got, testCase.wantPath)
			}
		})
	}
}

func TestWriteRecordingBundleExplicitCompleteStatusKeepsPairedContract(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "recording")
	if err := WriteRecordingBundle(RecordingConfig{
		Destination:      destination,
		ClientTranscript: []byte("client\n"),
		AgentTranscript:  []byte("agent\n"),
		RecordingStatus:  &RecordingStatus{State: RecordingStatusComplete},
	}); err != nil {
		t.Fatalf("WriteRecordingBundle: %v", err)
	}
	var manifest RecordingManifest
	if err := json.Unmarshal(readBundleFile(t, destination, "manifest.json"), &manifest); err != nil {
		t.Fatalf("decode complete manifest: %v", err)
	}
	if manifest.RecordingStatus == nil || manifest.RecordingStatus.State != RecordingStatusComplete {
		t.Fatalf("recording_status = %+v, want explicit complete status", manifest.RecordingStatus)
	}
	if got := manifestArtifactPaths(manifest); !equalStrings(got, []string{"client.transcript.jsonl", "agent.transcript.jsonl"}) {
		t.Fatalf("manifest artifacts = %v, want paired transcripts", got)
	}
}

func TestWriteRecordingBundleRejectsInvalidPartialStatusBeforePublication(t *testing.T) {
	tests := []struct {
		name   string
		config RecordingConfig
	}{
		{
			name: "partial without evidence",
			config: RecordingConfig{
				RecordingStatus: &RecordingStatus{State: RecordingStatusPartial, Reason: "sink unavailable"},
			},
		},
		{
			name: "partial without reason",
			config: RecordingConfig{
				ClientTranscript: []byte("client\n"),
				RecordingStatus:  &RecordingStatus{State: RecordingStatusPartial},
			},
		},
		{
			name: "unsupported state",
			config: RecordingConfig{
				ClientTranscript: []byte("client\n"),
				AgentTranscript:  []byte("agent\n"),
				RecordingStatus:  &RecordingStatus{State: "degraded", Reason: "sink unavailable"},
			},
		},
		{
			name: "complete with one side",
			config: RecordingConfig{
				ClientTranscript: []byte("client\n"),
				RecordingStatus:  &RecordingStatus{State: RecordingStatusComplete},
			},
		},
		{
			name: "one side without explicit partial state",
			config: RecordingConfig{
				ClientTranscript: []byte("client\n"),
			},
		},
		{
			name: "complete with reason",
			config: RecordingConfig{
				ClientTranscript: []byte("client\n"),
				AgentTranscript:  []byte("agent\n"),
				RecordingStatus:  &RecordingStatus{State: RecordingStatusComplete, Reason: "not complete"},
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "recording")
			testCase.config.Destination = destination
			err := WriteRecordingBundle(testCase.config)
			if !errors.Is(err, ErrInvalidRecording) {
				t.Fatalf("error = %v, want ErrInvalidRecording", err)
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed destination stat error = %v, want absent", statErr)
			}
		})
	}
}

func TestRecordingManifestRejectsInvalidStatusShapes(t *testing.T) {
	validDigest := strings.Repeat("a", 64)
	base := `{"format_version":1,"input_device":{},"output_device":{},"transport":"","model":"","clock_base":"","recording_status":{"state":"partial","reason":"sink unavailable"},"artifacts":[{"path":"client.transcript.jsonl","sha256":"` + validDigest + `"}]}`
	tests := []struct {
		name string
		json string
	}{
		{name: "valid client-only partial", json: base},
		{name: "partial without reason", json: strings.Replace(base, `,"reason":"sink unavailable"`, "", 1)},
		{name: "partial without transcript", json: strings.Replace(base, `{"path":"client.transcript.jsonl","sha256":"`+validDigest+`"}`, "", 1)},
		{name: "null status", json: strings.Replace(base, `{"state":"partial","reason":"sink unavailable"}`, "null", 1)},
		{name: "unknown status field", json: strings.Replace(base, `"reason":"sink unavailable"`, `"reason":"sink unavailable","extra":true`, 1)},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			var manifest RecordingManifest
			err := json.Unmarshal([]byte(testCase.json), &manifest)
			if testCase.name == "valid client-only partial" {
				if err != nil {
					t.Fatalf("decode valid partial manifest: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidRecordingManifest) {
				t.Fatalf("error = %v, want ErrInvalidRecordingManifest", err)
			}
		})
	}
}

func TestRecordingBundleEmitsDeterministicOptionalTerminalSummary(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "recording")
	want := &RecordingTerminalSummary{
		Reason:             "max_duration",
		Classification:     "max_duration",
		TerminalReason:     messages.TerminalReason("max_duration"),
		TerminalProvenance: messages.TerminalProvenanceLoop,
		OutputState:        messages.TerminalOutputPartial,
	}
	if err := WriteRecordingBundle(RecordingConfig{
		Destination:      destination,
		ClientTranscript: []byte("client\n"),
		AgentTranscript:  []byte("agent\n"),
		Terminal:         want,
	}); err != nil {
		t.Fatalf("WriteRecordingBundle: %v", err)
	}

	manifestBytes := readBundleFile(t, destination, "manifest.json")
	var manifest RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Terminal == nil {
		t.Fatal("manifest omitted terminal summary")
	}
	if *manifest.Terminal != *want {
		t.Fatalf("manifest terminal summary = %+v, want %+v", *manifest.Terminal, *want)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(manifestBytes, &fields); err != nil {
		t.Fatalf("decode manifest fields: %v", err)
	}
	const wantTerminal = `{"reason":"max_duration","classification":"max_duration","terminal_reason":"max_duration","terminal_provenance":"loop","output_state":"partial"}`
	if got := string(fields["terminal"]); got != wantTerminal {
		t.Fatalf("encoded terminal summary = %s, want %s", got, wantTerminal)
	}
	terminalFields := make(map[string]json.RawMessage)
	if err := json.Unmarshal(fields["terminal"], &terminalFields); err != nil {
		t.Fatalf("decode terminal summary: %v", err)
	}
	if len(terminalFields) != 5 {
		t.Fatalf("terminal summary field count = %d, want 5", len(terminalFields))
	}

	for field, value := range map[string]string{
		"reason":              "max_duration",
		"classification":      "max_duration",
		"terminal_reason":     "max_duration",
		"terminal_provenance": "loop",
		"output_state":        "partial",
	} {
		var got string
		if err := json.Unmarshal(terminalFields[field], &got); err != nil {
			t.Fatalf("decode terminal field %s value: %v", field, err)
		}
		if got != value {
			t.Fatalf("terminal %s = %q, want %q", field, got, value)
		}
	}
}

func TestRecordingBundleRejectsIncompleteTerminalSummary(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "recording")
	err := WriteRecordingBundle(RecordingConfig{
		Destination:      destination,
		ClientTranscript: []byte("client\n"),
		AgentTranscript:  []byte("agent\n"),
		Terminal: &RecordingTerminalSummary{
			Reason:             "max_duration",
			Classification:     "max_duration",
			TerminalReason:     messages.TerminalReason("max_duration"),
			TerminalProvenance: messages.TerminalProvenanceLoop,
		},
	})
	if !errors.Is(err, ErrInvalidRecording) {
		t.Fatalf("error = %v, want ErrInvalidRecording", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed destination stat error = %v, want absent", statErr)
	}
}

func TestRecordingBundleNumbersAudioDirectionsIndependently(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "recording")
	input := [][]byte{{1}, {2, 3}}
	output := [][]byte{{4}, {5, 6, 7}, {8}}
	if err := WriteRecordingBundle(RecordingConfig{
		Destination:      destination,
		ClientTranscript: []byte("client\n"),
		AgentTranscript:  []byte("agent\n"),
		InputSegments:    input,
		OutputSegments:   output,
	}); err != nil {
		t.Fatalf("WriteRecordingBundle: %v", err)
	}
	want := []string{
		"agent.transcript.jsonl", "audio", "audio/in-000.pcm", "audio/in-001.pcm",
		"audio/out-000.pcm", "audio/out-001.pcm", "audio/out-002.pcm", "client.transcript.jsonl", "manifest.json",
	}
	if got := recordingEntries(t, destination); !equalStrings(got, want) {
		t.Fatalf("bundle entries = %v, want %v", got, want)
	}
	for index, wantBytes := range input {
		path := filepath.ToSlash(filepath.Join("audio", "in-"+threeDigits(index)+".pcm"))
		if got := readBundleFile(t, destination, path); !bytes.Equal(got, wantBytes) {
			t.Errorf("%s = %x, want %x", path, got, wantBytes)
		}
	}
	for index, wantBytes := range output {
		path := filepath.ToSlash(filepath.Join("audio", "out-"+threeDigits(index)+".pcm"))
		if got := readBundleFile(t, destination, path); !bytes.Equal(got, wantBytes) {
			t.Errorf("%s = %x, want %x", path, got, wantBytes)
		}
	}
}

func TestRecordingBundleAllowsZeroInputAudioSegments(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "prompt-only")
	sessionLog := []byte(`{"turn_index":1,"input":{"text":"hello","audio_bytes":0,"committed":true},"response":{"text":"hi","complete":true,"audio_bytes":2}}` + "\n")
	if err := WriteRecordingBundle(RecordingConfig{
		Destination:      destination,
		ClientTranscript: []byte("client transcript\n"),
		AgentTranscript:  []byte("agent transcript\n"),
		OutputSegments:   [][]byte{{0x10, 0x00}},
		SessionLog:       sessionLog,
	}); err != nil {
		t.Fatalf("WriteRecordingBundle: %v", err)
	}

	wantEntries := []string{
		"agent.transcript.jsonl", "audio", "audio/out-000.pcm", "client.transcript.jsonl", "manifest.json", "session-log.jsonl",
	}
	if got := recordingEntries(t, destination); !equalStrings(got, wantEntries) {
		t.Fatalf("bundle entries = %v, want %v", got, wantEntries)
	}
	if _, err := os.Stat(filepath.Join(destination, "audio", "in-000.pcm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prompt-only input audio stat error = %v, want absent", err)
	}

	var manifest RecordingManifest
	if err := json.Unmarshal(readBundleFile(t, destination, "manifest.json"), &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	wantArtifacts := []string{
		"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/out-000.pcm",
	}
	gotArtifacts := make([]string, 0, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		gotArtifacts = append(gotArtifacts, artifact.Path)
		data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("read manifest artifact %q: %v", artifact.Path, err)
		}
		if len(data) == 0 {
			t.Fatalf("manifest artifact %q is empty", artifact.Path)
		}
	}
	if !equalStrings(gotArtifacts, wantArtifacts) {
		t.Fatalf("manifest artifacts = %v, want %v", gotArtifacts, wantArtifacts)
	}
}

func TestRecordingBundleRejectsPresentEmptyAudioSegments(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		input  [][]byte
		output [][]byte
	}{
		{name: "input", input: [][]byte{nil}, output: [][]byte{{1}}},
		{name: "output", input: nil, output: [][]byte{nil}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "recording")
			err := WriteRecordingBundle(RecordingConfig{
				Destination:      destination,
				ClientTranscript: []byte("client\n"),
				AgentTranscript:  []byte("agent\n"),
				InputSegments:    testCase.input,
				OutputSegments:   testCase.output,
			})
			if !errors.Is(err, ErrInvalidRecording) {
				t.Fatalf("error = %v, want ErrInvalidRecording", err)
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed destination stat error = %v, want absent", statErr)
			}
		})
	}
}

func TestRecordingBundleEmitsOptionalSessionLog(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "recording")
		sessionLog := []byte("{\"turn_index\":1}\n{\"turn_index\":2}\n")
		if err := WriteRecordingBundle(RecordingConfig{
			Destination:      destination,
			ClientTranscript: []byte("client\n"),
			AgentTranscript:  []byte("agent\n"),
			InputSegments:    [][]byte{{1, 2}},
			OutputSegments:   [][]byte{{3, 4}},
			SessionLog:       sessionLog,
		}); err != nil {
			t.Fatalf("WriteRecordingBundle: %v", err)
		}
		want := []string{
			"agent.transcript.jsonl", "audio", "audio/in-000.pcm", "audio/out-000.pcm",
			"client.transcript.jsonl", "manifest.json", "session-log.jsonl",
		}
		if got := recordingEntries(t, destination); !equalStrings(got, want) {
			t.Fatalf("bundle entries = %v, want %v", got, want)
		}
		if got := readBundleFile(t, destination, "session-log.jsonl"); !bytes.Equal(got, sessionLog) {
			t.Fatalf("session-log.jsonl = %q, want %q", got, sessionLog)
		}
		var manifest RecordingManifest
		manifestBytes := readBundleFile(t, destination, "manifest.json")
		if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		found := false
		for _, artifact := range manifest.Artifacts {
			if artifact.Path == "session-log.jsonl" {
				found = true
				sum := sha256.Sum256(sessionLog)
				if artifact.SHA256 != hex.EncodeToString(sum[:]) {
					t.Fatalf("session-log hash = %s, want %s", artifact.SHA256, hex.EncodeToString(sum[:]))
				}
			}
		}
		if !found {
			t.Fatal("manifest artifacts missing session-log.jsonl")
		}
	})
	t.Run("absent when empty", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "recording")
		if err := WriteRecordingBundle(testRecordingConfig(destination)); err != nil {
			t.Fatalf("WriteRecordingBundle: %v", err)
		}
		for _, entry := range recordingEntries(t, destination) {
			if entry == "session-log.jsonl" {
				t.Fatal("empty SessionLog must not emit session-log.jsonl")
			}
		}
	})
}

func TestRecordingBundleFailureIdentitiesAndRetry(t *testing.T) {
	t.Run("existing non-empty destination", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "recording")
		if err := os.Mkdir(destination, 0o755); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(destination, "customer.txt")
		original := []byte("do not overwrite")
		if err := os.WriteFile(sentinel, original, 0o644); err != nil {
			t.Fatal(err)
		}
		err := WriteRecordingBundle(testRecordingConfig(destination))
		if !errors.Is(err, ErrRecordingDestinationNotEmpty) || !errors.Is(err, ErrRecordingDestination) {
			t.Fatalf("error = %v, want destination identities", err)
		}
		if got, readErr := os.ReadFile(sentinel); readErr != nil || !bytes.Equal(got, original) {
			t.Fatalf("existing content changed: bytes=%q err=%v", got, readErr)
		}
	})

	t.Run("unwritable destination", func(t *testing.T) {
		parentFile := filepath.Join(t.TempDir(), "parent-file")
		if err := os.WriteFile(parentFile, []byte("not a directory"), 0o644); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(parentFile, "recording")
		err := WriteRecordingBundle(testRecordingConfig(destination))
		if !errors.Is(err, ErrRecordingDestination) {
			t.Fatalf("error = %v, want ErrRecordingDestination", err)
		}
		if !strings.Contains(err.Error(), destination) {
			t.Fatalf("error = %v, want destination context", err)
		}
	})

	t.Run("injected disk full and retry", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "recording")
		config := testRecordingConfig(destination)
		config.WriteFile = func(path string, data []byte, mode os.FileMode) (int, error) {
			return 0, ErrRecordingDiskFull
		}
		err := WriteRecordingBundle(config)
		if !errors.Is(err, ErrRecordingWrite) || !errors.Is(err, ErrRecordingDiskFull) {
			t.Fatalf("error = %v, want write and disk-full identities", err)
		}
		if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed recording destination stat error = %v, want absent", statErr)
		}
		config.WriteFile = nil
		if err := WriteRecordingBundle(config); err != nil {
			t.Fatalf("retry WriteRecordingBundle: %v", err)
		}
		if _, err := os.Stat(filepath.Join(destination, "manifest.json")); err != nil {
			t.Fatalf("retry manifest: %v", err)
		}
	})

	t.Run("injected short write", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "recording")
		config := testRecordingConfig(destination)
		config.WriteFile = func(path string, data []byte, mode os.FileMode) (int, error) {
			if err := os.WriteFile(path, data, mode); err != nil {
				return 0, err
			}
			return len(data) - 1, nil
		}
		err := WriteRecordingBundle(config)
		if !errors.Is(err, ErrRecordingWrite) || !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("error = %v, want write and short-write identities", err)
		}
		if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed recording destination stat error = %v, want absent", statErr)
		}
	})
}

func TestRecordingBundleRejectsUnsafeCredentialInputs(t *testing.T) {
	t.Run("empty credential", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "recording")
		config := testRecordingConfig(destination)
		config.Credentials = []string{""}
		err := WriteRecordingBundle(config)
		if !errors.Is(err, ErrEmptyRecordingCredential) {
			t.Fatalf("error = %v, want ErrEmptyRecordingCredential", err)
		}
		if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("destination stat error = %v, want absent", statErr)
		}
	})

	t.Run("credential in PCM", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "recording")
		config := testRecordingConfig(destination)
		config.Credentials = []string{"pcm-secret"}
		config.InputSegments = [][]byte{[]byte("pcm-secret")}
		err := WriteRecordingBundle(config)
		if !errors.Is(err, ErrRecordingUnsafeArtifact) {
			t.Fatalf("error = %v, want ErrRecordingUnsafeArtifact", err)
		}
		if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("destination stat error = %v, want absent", statErr)
		}
	})

	t.Run("credential never reaches injected writer", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "recording")
		firstSecret := "input-pcm-secret"
		secondSecret := "output-pcm-secret"
		config := testRecordingConfig(destination)
		config.Credentials = []string{firstSecret, secondSecret}
		config.InputSegments = [][]byte{[]byte(firstSecret)}
		config.OutputSegments = [][]byte{[]byte(secondSecret)}
		var callbackSawCredential bool
		config.WriteFile = func(path string, data []byte, mode os.FileMode) (int, error) {
			if bytes.Contains(data, []byte(firstSecret)) || bytes.Contains(data, []byte(secondSecret)) {
				callbackSawCredential = true
			}
			if err := os.WriteFile(path, data, mode); err != nil {
				return 0, err
			}
			return len(data), nil
		}

		err := WriteRecordingBundle(config)
		if !errors.Is(err, ErrRecordingUnsafeArtifact) {
			t.Fatalf("error = %v, want ErrRecordingUnsafeArtifact", err)
		}
		if callbackSawCredential {
			t.Fatal("injected writer observed a configured credential")
		}
		if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("destination stat error = %v, want absent", statErr)
		}
	})
}

func TestRecordingWriter(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "recording")
	writer, err := NewRecordingWriter(testRecordingConfig(destination))
	if err != nil {
		t.Fatalf("NewRecordingWriter: %v", err)
	}
	if err := writer.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

func testRecordingConfig(destination string) RecordingConfig {
	return RecordingConfig{
		Destination:      destination,
		ClientTranscript: []byte("client transcript\n"),
		AgentTranscript:  []byte("agent transcript\n"),
		InputSegments:    [][]byte{{1, 2}},
		OutputSegments:   [][]byte{{3, 4}},
	}
}

func recordingEntries(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(entries)
	return entries
}

func readBundleFile(t *testing.T, root, relative string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return data
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func manifestArtifactPaths(manifest RecordingManifest) []string {
	paths := make([]string, 0, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		paths = append(paths, artifact.Path)
	}
	return paths
}

func threeDigits(value int) string {
	return fmt.Sprintf("%03d", value)
}
