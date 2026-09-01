package audio

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const DuplexCapsuleSchemaVersion = 2

var duplexCapsuleArtifacts = []string{
	"audio/provider-in.pcm",
	"audio/playback-rendered.pcm",
	"audio/capture-generated.pcm",
	"audio/source-near-end.pcm",
	"audio/source-background.pcm",
	"events.jsonl",
}

var duplexCapsuleV1Artifacts = []string{
	"audio/provider-in.pcm",
	"audio/playback-rendered.pcm",
	"audio/source-near-end.pcm",
	"audio/source-background.pcm",
	"events.jsonl",
}

type CapsuleArtifact struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type DuplexFailureCapsuleManifest struct {
	SchemaVersion int                        `json:"schema_version"`
	Finalized     bool                       `json:"finalized"`
	CreatedUTC    string                     `json:"created_utc"`
	Scenario      DuplexScenario             `json:"scenario"`
	CallbackCount int                        `json:"callback_count"`
	Artifacts     map[string]CapsuleArtifact `json:"artifacts"`
}
type DuplexFailureCapsule struct {
	Manifest                                               DuplexFailureCapsuleManifest
	ProviderInput, Rendered, Captured, NearEnd, Background []int16
	Events                                                 []DeviceTraceEvent
}

// WriteDuplexFailureCapsule atomically finalizes a deterministic multi-tap
// failure bundle. The manifest is written last in a sibling temporary
// directory, then renamed into place.
func WriteDuplexFailureCapsule(dir string, scenario DuplexScenario, providerInput []int16, registry *SimulatedDuplexRegistry) error {
	if registry == nil {
		return fmt.Errorf("nil simulated duplex registry")
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".audio-capsule-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	events := registry.Trace()
	rendered := registry.RenderedSamples()
	captured := registry.CapturedSamples()
	artifacts := map[string][]byte{
		"audio/provider-in.pcm":       encodeSamples(providerInput),
		"audio/playback-rendered.pcm": encodeSamples(rendered),
		"audio/capture-generated.pcm": encodeSamples(captured),
		"audio/source-near-end.pcm":   encodeSamples(scenario.Acoustic.NearEnd),
		"audio/source-background.pcm": encodeSamples(scenario.Acoustic.Background),
	}
	eventBytes, err := marshalJSONLines(events)
	if err != nil {
		return err
	}
	artifacts["events.jsonl"] = eventBytes
	metadata := make(map[string]CapsuleArtifact, len(artifacts))
	for name, data := range artifacts {
		path := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		metadata[name] = CapsuleArtifact{Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	}
	callbacks := 0
	for _, event := range events {
		if event.Tap == "render" && int(event.Sequence)+1 > callbacks {
			callbacks = int(event.Sequence) + 1
		}
	}
	manifest := DuplexFailureCapsuleManifest{SchemaVersion: DuplexCapsuleSchemaVersion, Finalized: true, CreatedUTC: time.Now().UTC().Format(time.RFC3339Nano), Scenario: scenario, CallbackCount: callbacks, Artifacts: metadata}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "run-manifest.json"), append(manifestBytes, '\n'), 0o644); err != nil {
		return err
	}
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("capsule destination %q already exists", dir)
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmp, dir)
}

func LoadDuplexFailureCapsule(dir string) (*DuplexFailureCapsule, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(dir, "run-manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest DuplexFailureCapsuleManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != 1 && manifest.SchemaVersion != DuplexCapsuleSchemaVersion {
		return nil, fmt.Errorf("unsupported audio capsule schema %d", manifest.SchemaVersion)
	}
	if !manifest.Finalized {
		return nil, fmt.Errorf("audio capsule is not finalized")
	}
	requiredArtifacts := duplexCapsuleArtifacts
	if manifest.SchemaVersion == 1 {
		requiredArtifacts = duplexCapsuleV1Artifacts
	}
	if len(manifest.Artifacts) != len(requiredArtifacts) {
		return nil, fmt.Errorf("audio capsule artifact inventory has %d entries; want %d", len(manifest.Artifacts), len(requiredArtifacts))
	}
	for _, name := range requiredArtifacts {
		if _, ok := manifest.Artifacts[name]; !ok {
			return nil, fmt.Errorf("audio capsule is missing required artifact %s", name)
		}
	}
	data := map[string][]byte{}
	for _, name := range requiredArtifacts {
		want := manifest.Artifacts[name]
		value, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return nil, fmt.Errorf("artifact %s: %w", name, err)
		}
		sum := sha256.Sum256(value)
		gotHash := hex.EncodeToString(sum[:])
		if int64(len(value)) != want.Size || gotHash != want.SHA256 {
			return nil, fmt.Errorf("artifact %s integrity mismatch: size=%d/%d sha256=%s/%s", name, len(value), want.Size, gotHash, want.SHA256)
		}
		data[name] = value
	}
	decode := func(name string) ([]int16, error) {
		value := data[name]
		if len(value)%2 != 0 {
			return nil, fmt.Errorf("artifact %s has odd PCM16 byte count", name)
		}
		out := make([]int16, len(value)/2)
		decodePCM16(out, value)
		return out, nil
	}
	provider, err := decode("audio/provider-in.pcm")
	if err != nil {
		return nil, err
	}
	rendered, err := decode("audio/playback-rendered.pcm")
	if err != nil {
		return nil, err
	}
	var captured []int16
	if manifest.SchemaVersion >= 2 {
		captured, err = decode("audio/capture-generated.pcm")
		if err != nil {
			return nil, err
		}
	}
	near, err := decode("audio/source-near-end.pcm")
	if err != nil {
		return nil, err
	}
	background, err := decode("audio/source-background.pcm")
	if err != nil {
		return nil, err
	}
	var events []DeviceTraceEvent
	scanner := bufio.NewScanner(bytes.NewReader(data["events.jsonl"]))
	for scanner.Scan() {
		var event DeviceTraceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	manifest.Scenario.Acoustic.NearEnd = near
	manifest.Scenario.Acoustic.Background = background
	return &DuplexFailureCapsule{Manifest: manifest, ProviderInput: provider, Rendered: rendered, Captured: captured, NearEnd: near, Background: background, Events: events}, nil
}

func ReplayDuplexFailureCapsule(dir string) (*SimulatedDuplexRegistry, error) {
	capsule, err := LoadDuplexFailureCapsule(dir)
	if err != nil {
		return nil, err
	}
	registry, err := NewSimulatedDuplexRegistry(capsule.Manifest.Scenario)
	if err != nil {
		return nil, err
	}
	opened, err := registry.Open(registry.output.ID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	if err := opened.(*SimulatedDuplexStream).WriteSamples(context.Background(), capsule.ProviderInput); err != nil {
		return nil, err
	}
	if err := registry.Advance(capsule.Manifest.CallbackCount); err != nil {
		return nil, err
	}
	if got := encodeSamples(registry.RenderedSamples()); !bytes.Equal(got, encodeSamples(capsule.Rendered)) {
		return nil, fmt.Errorf("replayed rendered PCM hash differs from capsule")
	}
	if capsule.Manifest.SchemaVersion >= 2 && !bytes.Equal(encodeSamples(registry.CapturedSamples()), encodeSamples(capsule.Captured)) {
		return nil, fmt.Errorf("replayed captured PCM hash differs from capsule")
	}
	return registry, nil
}

func encodeSamples(samples []int16) []byte {
	out := make([]byte, len(samples)*2)
	encodePCM16(out, samples)
	return out
}
func marshalJSONLines(events []DeviceTraceEvent) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}
