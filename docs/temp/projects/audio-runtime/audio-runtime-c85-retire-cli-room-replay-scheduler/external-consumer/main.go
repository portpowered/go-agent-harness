// Command external-consumer proves that the replay scheduler is usable from a
// separate credential-free module without importing agent-cli.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplayschedule"
	roomreplaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplayschedule/wire"
)

const captureCoverage = "session_capture.v2:json(version,provider,session,records,ends_with_disconnect)"

type provider struct {
	Name  string `json:"name,omitempty"`
	Model string `json:"model,omitempty"`
}

type session struct {
	StartedAtUTC string `json:"started_at_utc,omitempty"`
}

type record struct {
	Sequence    int             `json:"sequence"`
	Direction   string          `json:"direction"`
	TimestampMs int64           `json:"timestamp_ms"`
	Type        string          `json:"type"`
	PayloadType string          `json:"payload_type"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

type envelope struct {
	Version   int       `json:"version"`
	Provider  provider  `json:"provider"`
	Session   session   `json:"session"`
	Records   []record  `json:"records"`
	Integrity integrity `json:"integrity"`
}

type coverageEnvelope struct {
	Version    int      `json:"version"`
	Provider   provider `json:"provider"`
	Session    session  `json:"session"`
	Records    []record `json:"records"`
	Disconnect bool     `json:"ends_with_disconnect,omitempty"`
}

type integrity struct {
	Algorithm string `json:"algorithm"`
	Coverage  string `json:"coverage"`
	Digest    string `json:"digest"`
}

func main() {
	root, err := os.MkdirTemp("", "room-replay-consumer-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)

	capturePath := filepath.Join(root, "target.session.json")
	if err := writeCapture(capturePath); err != nil {
		panic(err)
	}
	sourceAPath := filepath.Join(root, "source-a.pcm")
	sourceBPath := filepath.Join(root, "source-b.pcm")
	targetPath := filepath.Join(root, "target.pcm")
	if err := os.WriteFile(sourceAPath, []byte{1, 0}, 0o600); err != nil {
		panic(err)
	}
	if err := os.WriteFile(sourceBPath, []byte{2, 0}, 0o600); err != nil {
		panic(err)
	}
	if err := os.WriteFile(targetPath, []byte{0, 0, 0, 0}, 0o600); err != nil {
		panic(err)
	}

	service := roomreplaywire.NewService()
	schedule, err := service.Build(context.Background(), roomreplayschedule.BuildRequest{
		SourceFormat: roomreplayschedule.SourcePCM16Format{SampleRate: 100, Channels: 1, SampleWidthBits: 16},
		TargetFormat: roomreplayschedule.PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond},
		Participants: []roomreplayschedule.Participant{
			{ID: "source-a", SentPCMPath: sourceAPath},
			{ID: "source-b", SentPCMPath: sourceBPath},
			{ID: "target", CapturePath: capturePath, SentPCMPath: targetPath},
		},
		Timeline: []roomreplayschedule.TimelineEvent{
			{Sequence: 20, Type: "speech_start", ParticipantID: "source-a"},
			{Sequence: 10, Type: "speech_start", ParticipantID: "source-b"},
		},
		TargetIDs: []string{"target"},
	})
	if err != nil {
		panic(err)
	}
	var mu sync.Mutex
	released := 0
	acks := 0
	releasedSources := make([]string, 0, 2)
	releasedPCM := make([][]byte, 0, 2)
	target := roomreplayschedule.Target{
		ID:      "target",
		Active:  func() bool { return true },
		Release: func(context.Context, string, []byte) error { return nil },
		Advance: func(context.Context) error { return nil },
		AwaitAcknowledgement: func(context.Context) error {
			mu.Lock()
			acks++
			mu.Unlock()
			return nil
		},
	}
	target.Release = func(_ context.Context, sourceID string, pcm []byte) error {
		mu.Lock()
		released++
		releasedSources = append(releasedSources, sourceID)
		releasedPCM = append(releasedPCM, append([]byte(nil), pcm...))
		mu.Unlock()
		return nil
	}
	if err := schedule.Run(context.Background(), roomreplayschedule.RunRequest{Targets: []roomreplayschedule.Target{target}}); err != nil {
		panic(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if released != 2 || acks != 1 || len(releasedSources) != 2 || releasedSources[0] != "source-b" || releasedSources[1] != "source-a" {
		panic(fmt.Sprintf("release oracle = count %d acks %d sources %v", released, acks, releasedSources))
	}
	if !bytes.Equal(releasedPCM[0], []byte{2, 0, 0, 0}) || !bytes.Equal(releasedPCM[1], []byte{1, 0, 0, 0}) {
		panic(fmt.Sprintf("release bytes = %x, want 02000000 then 01000000", releasedPCM))
	}
	_, err = service.Build(context.Background(), roomreplayschedule.BuildRequest{
		SourceFormat: roomreplayschedule.SourcePCM16Format{SampleRate: 100, Channels: 1, SampleWidthBits: 8},
		TargetFormat: roomreplayschedule.PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond},
		Participants: []roomreplayschedule.Participant{{ID: "target", CapturePath: capturePath, SentPCMPath: targetPath}},
		TargetIDs:    []string{"target"},
	})
	if !errors.Is(err, roomreplayschedule.ErrInvalidPCM) {
		panic(fmt.Sprintf("malformed source error = %v", err))
	}
	fmt.Println(`{"external_consumer":true,"credential_free":true,"released":2,"acknowledgements":1,"malformed_rejected":true}`)
}

func writeCapture(path string) error {
	records := []record{{
		Sequence: 1, Direction: "client_to_server", Type: "input_audio_buffer.append",
		PayloadType: "websocket_message", Payload: json.RawMessage(`{"type":"input_audio_buffer.append"}`),
	}}
	base := coverageEnvelope{
		Version: 2, Session: session{StartedAtUTC: "2026-01-01T00:00:00Z"}, Records: records,
	}
	data, err := json.Marshal(base)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	value := envelope{
		Version: 2, Session: base.Session, Records: records,
		Integrity: integrity{Algorithm: "sha256", Coverage: captureCoverage, Digest: hex.EncodeToString(digest[:])},
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}
