// Command room-replay-consumer exercises the public roomreplaybundle contract
// from a separate module with workspace mode disabled. It imports no CLI,
// internal, credential, or scheduler package.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplaybundle"
	roomreplaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplaybundle/wire"
)

const marker = "C75_ROOMREPLAYBUNDLE_CONSUMER"

func main() {
	if err := run(); err != nil {
		panic(err)
	}
	fmt.Println(marker + " PASS")
}

func run() error {
	root, err := os.MkdirTemp("", "c75-room-replay-consumer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err := writeBundle(root); err != nil {
		return err
	}

	service := roomreplaywire.NewService()
	first, err := service.Load(root)
	if err != nil {
		return fmt.Errorf("public service Load: %w", err)
	}
	second, err := service.Load(root)
	if err != nil {
		return fmt.Errorf("second public service Load: %w", err)
	}
	if len(first.Participants) != 2 || first.Participants[0].ID != "alpha" || first.Participants[1].ID != "beta" {
		return fmt.Errorf("participants = %+v, want deterministic alpha/beta projection", first.Participants)
	}
	if len(first.Timeline) != 1 || first.Timeline[0].ParticipantID != "alpha" {
		return fmt.Errorf("timeline = %+v, want one validated event", first.Timeline)
	}
	if !reflect.DeepEqual(first.Participants, second.Participants) || !reflect.DeepEqual(first.Artifacts, second.Artifacts) {
		return fmt.Errorf("repeated Load calls produced different deterministic projections")
	}
	outside := filepath.Join(filepath.Dir(root), "c75-room-replay-output")
	if err := service.ValidateOutput(first, outside); err != nil {
		return fmt.Errorf("public ValidateOutput rejected sibling destination: %w", err)
	}

	corrupt := filepath.Join(root, "participants", "alpha", "sent.pcm")
	if err := os.WriteFile(corrupt, []byte{9, 8, 7, 6}, 0o600); err != nil {
		return err
	}
	_, err = service.Load(root)
	if err == nil || !errors.Is(err, roomreplaybundle.ErrInvalidRoomReplayBundle) {
		return fmt.Errorf("same-length corruption error = %v, want ErrInvalidRoomReplayBundle", err)
	}
	return nil
}

func writeBundle(root string) error {
	clock := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	common := []byte{1, 2, 3, 4}
	paths := map[string][]byte{
		"participants/alpha/agent.wav":         common,
		"participants/alpha/diagnostics.jsonl": common,
		"participants/alpha/deltas.jsonl":      common,
		"participants/alpha/sent.pcm":          common,
		"participants/alpha/received.pcm":      common,
		"participants/alpha/events.jsonl":      common,
		"participants/beta/agent.wav":          common,
		"participants/beta/diagnostics.jsonl":  common,
		"participants/beta/deltas.jsonl":       common,
		"participants/beta/sent.pcm":           common,
		"participants/beta/received.pcm":       common,
		"participants/beta/events.jsonl":       common,
		"room-mix.wav":                         common,
	}
	timelinePath := "room-timeline.jsonl"
	timeline := []byte(fmt.Sprintf(`{"sequence":0,"monotonic_offset_ms":0,"unix_ms":%d,"type":"speech_start","participant_id":"alpha"}`+"\n", clock.UnixMilli()))
	paths[timelinePath] = timeline
	for name, data := range paths {
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filename, data, 0o600); err != nil {
			return err
		}
	}

	participants := make(map[string]any)
	for _, id := range []string{"alpha", "beta"} {
		artifacts := make(map[string]any)
		for _, role := range []string{"wav", "diagnostics", "deltas", "sent_pcm", "received_pcm", "events"} {
			name := "participants/" + id + "/" + map[string]string{
				"wav": "agent.wav", "diagnostics": "diagnostics.jsonl", "deltas": "deltas.jsonl", "sent_pcm": "sent.pcm", "received_pcm": "received.pcm", "events": "events.jsonl",
			}[role]
			artifacts[role] = artifact(name, paths[name])
		}
		participants[id] = map[string]any{"id": id, "kind": "human", "artifacts": artifacts}
	}
	manifest := map[string]any{
		"schema_version": roomreplaybundle.RoomReplayBundleSchemaVersion,
		"finalized":      true,
		"clock_base":     clock.Format(time.RFC3339Nano),
		"timing":         map[string]any{"started_at": clock.Format(time.RFC3339Nano), "ended_at": clock.Add(time.Second).Format(time.RFC3339Nano)},
		"pcm_format":     map[string]any{"sample_rate_hz": 24000, "channels": 1, "sample_width_bits": 16, "byte_order": "little", "encoding": "signed_pcm16"},
		"participants":   participants,
		"artifacts": map[string]any{
			"room_timeline": artifact(timelinePath, timeline),
			"room_mix":      artifact("room-mix.wav", common),
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, roomreplaybundle.RoomReplayBundleManifestPath), append(data, '\n'), 0o600)
}

func artifact(path string, data []byte) map[string]any {
	digest := sha256.Sum256(data)
	return map[string]any{"path": path, "size": len(data), "sha256": hex.EncodeToString(digest[:])}
}
