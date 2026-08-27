package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const s2sLiveShapedBargeInV2ReplayFixture = "s2s_live_shaped_barge_in_v2_replay.session.json"

// s2sLiveShapedBargeInV2ReplayRuntimeObserver retains only the runtime facts
// needed to prove the replay completed from inside the shipped command. It
// deliberately drops payload contents, timestamps, and error text.
type s2sLiveShapedBargeInV2ReplayRuntimeObserver struct {
	mu    sync.Mutex
	facts []s2sLiveShapedBargeInV2ReplayRuntimeFact
}

type s2sLiveShapedBargeInV2ReplayRuntimeFact struct {
	Kind        services.SessionRuntimeObservationKind
	PayloadSize int
	Turns       int
	Commit      int
	Clean       bool
	HasError    bool
	Accounted   bool
}

func (o *s2sLiveShapedBargeInV2ReplayRuntimeObserver) ObserveSessionRuntime(observation services.SessionRuntimeObservation) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.facts = append(o.facts, s2sLiveShapedBargeInV2ReplayRuntimeFact{
		Kind:        observation.Kind,
		PayloadSize: len(observation.Payload),
		Turns:       observation.TurnsCompleted,
		Commit:      observation.InputCommit,
		Clean:       observation.Clean,
		HasError:    observation.Error != "",
		Accounted:   observation.FinalAccounting != nil,
	})
	o.mu.Unlock()
}

func (o *s2sLiveShapedBargeInV2ReplayRuntimeObserver) snapshot() []s2sLiveShapedBargeInV2ReplayRuntimeFact {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]s2sLiveShapedBargeInV2ReplayRuntimeFact(nil), o.facts...)
}

// loadS2SLiveShapedBargeInV2ReplayCapture loads the committed redacted
// provider observation. The committed capture contains placeholder objects
// instead of customer or assistant audio; only the materialized temp copy
// below contains generated sentinel PCM accepted by the replay transport.
func loadS2SLiveShapedBargeInV2ReplayCapture(t *testing.T) (gwtesting.SessionCapture, string) {
	t.Helper()
	fixturePath := locateCLIFixture(t, s2sLiveShapedBargeInV2ReplayFixture)
	if violations := gwtesting.ValidateSessionCaptureFile(fixturePath); len(violations) != 0 {
		t.Fatalf("sanitized live-derived replay fixture failed hygiene validation: %v", violations)
	}
	capture, err := gwtesting.LoadSessionCapture(fixturePath)
	if err != nil {
		t.Fatalf("load sanitized live-derived replay fixture: %v", err)
	}
	materializeS2SLiveShapedBargeInV2Audio(t, &capture)
	path := filepath.Join(t.TempDir(), "s2s-live-shaped-barge-in-v2-replay.session.json")
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal materialized replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write materialized replay capture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(path); err != nil {
		t.Fatalf("replay dialer rejected materialized live-derived capture: %v", err)
	}
	return capture, path
}

func materializeS2SLiveShapedBargeInV2Audio(t *testing.T, capture *gwtesting.SessionCapture) {
	t.Helper()
	inputTurn := 0
	outputDelta := 0
	for index := range capture.Records {
		record := &capture.Records[index]
		if record.PayloadType != gwtesting.SessionPayloadTypeWebSocketMessage {
			t.Fatalf("record %d payload type = %q, want websocket_message", record.Sequence, record.PayloadType)
		}
		var payload map[string]any
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Fatalf("decode replay placeholder at sequence %d: %v", record.Sequence, err)
		}
		switch record.Type {
		case "input_audio_buffer.append":
			inputTurn++
			payload["audio"] = base64.StdEncoding.EncodeToString(deterministicBargeInFrame(byte(inputTurn)))
		case "response.output_audio.delta":
			outputDelta++
			payload["delta"] = base64.StdEncoding.EncodeToString([]byte{byte(outputDelta), 0, byte(outputDelta + 20), 0})
		default:
			continue
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode replay placeholder at sequence %d: %v", record.Sequence, err)
		}
		record.Payload = encoded
	}
	if inputTurn != deterministicBargeInTurns {
		t.Fatalf("sanitized replay fixture has %d input placeholders, want %d", inputTurn, deterministicBargeInTurns)
	}
	if outputDelta != 3 {
		t.Fatalf("sanitized replay fixture has %d output placeholders, want 3", outputDelta)
	}
}

// runS2SLiveShapedBargeInV2Replay drives the public session command over the
// materialized replay capture. The reader retains the same event gates as the
// deterministic matrix, so the fixture cannot pass by merely matching counts.
func runS2SLiveShapedBargeInV2Replay(t *testing.T, replayPath string) (*deterministicBargeInTrace, *s2sLiveShapedBargeInV2ReplayRuntimeObserver, string, error) {
	t.Helper()
	trace := newDeterministicBargeInTrace()
	runtimeObserver := &s2sLiveShapedBargeInV2ReplayRuntimeObserver{}
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortSessionRuntimeObserver, runtimeObserver),
	)
	if err != nil {
		return trace, runtimeObserver, "", fmt.Errorf("initialize shipped CLI: %w", err)
	}
	agentCLI.SetSessionStreamObserver(trace.observe)
	workDir := t.TempDir()
	audioOutPath := filepath.Join(workDir, "assistant.wav")
	root := agentCLI.Generate()
	root.SetIn(newDeterministicBargeInAudioReader(trace))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"--config-dir", filepath.Join(workDir, "config"),
		"session",
		"--replay", replayPath,
		"--record-dir", filepath.Join(workDir, "recording"),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "replay-test-key",
		"--system-prompt", "none",
		"--audio-in", "-",
		"--audio-out", audioOutPath,
		"--max-duration", deterministicBargeInRunTimeout.String(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), deterministicBargeInRunTimeout)
	defer cancel()
	return trace, runtimeObserver, audioOutPath, root.ExecuteContext(ctx)
}

func assertS2SLiveShapedBargeInV2ReplayRuntime(t *testing.T, trace *deterministicBargeInTrace, observer *s2sLiveShapedBargeInV2ReplayRuntimeObserver, audioOutPath string) {
	t.Helper()
	events := trace.streamSnapshot()
	starts, ends, audio := 0, 0, 0
	for _, event := range events {
		switch event.Type {
		case "MESSAGE.START":
			starts++
		case "MESSAGE.END":
			ends++
		case "AUDIO.DELTA":
			if event.Bytes > 0 {
				audio++
			}
		}
	}
	if starts != deterministicBargeInTurns || ends != deterministicBargeInTurns || audio != 3 {
		t.Fatalf("replayed stream boundary counts = starts:%d ends:%d audio:%d, want %d/%d/3; stream=%v", starts, ends, audio, deterministicBargeInTurns, deterministicBargeInTurns, events)
	}
	info, err := os.Stat(audioOutPath)
	if err != nil || info.Size() <= 44 {
		t.Fatalf("replayed assistant audio artifact is missing or empty: %v", err)
	}

	facts := observer.snapshot()
	inputCommits, turns, terminals := 0, 0, 0
	for _, fact := range facts {
		switch fact.Kind {
		case services.SessionRuntimeObservationInputCommit:
			inputCommits++
			if fact.PayloadSize == 0 || fact.Commit != inputCommits {
				t.Fatalf("replayed input commit fact %d was not non-empty or ordered: %#v", inputCommits, fact)
			}
		case services.SessionRuntimeObservationTurnCompleted:
			turns++
		case services.SessionRuntimeObservationTerminal:
			terminals++
			if !fact.Clean || fact.HasError || !fact.Accounted || fact.Turns != deterministicBargeInTurns {
				t.Fatalf("replayed terminal fact was not clean and accounted: %#v", fact)
			}
		}
	}
	if inputCommits != deterministicBargeInTurns || turns != deterministicBargeInTurns || terminals != 1 {
		t.Fatalf("replayed runtime counts = input:%d turns:%d terminals:%d, want %d/%d/1; facts=%#v", inputCommits, turns, terminals, deterministicBargeInTurns, deterministicBargeInTurns, facts)
	}
}

func TestSessionCommandS2SLiveShapedBargeInV2ReplaysSanitizedLiveCapture(t *testing.T) {
	capture, replayPath := loadS2SLiveShapedBargeInV2ReplayCapture(t)
	ledger, err := validateDeterministicBargeInCapture(capture, true)
	if err != nil {
		t.Fatalf("sanitized live-derived replay fixture failed the shared ledger: %v; evidence=%s", err, ledger.evidence())
	}
	trace, runtimeObserver, audioOutPath, runErr := runS2SLiveShapedBargeInV2Replay(t, replayPath)
	if runErr != nil {
		t.Fatalf("shipped session CLI failed to replay sanitized live capture: %v; evidence=%s; stream=%v", runErr, ledger.evidence(), trace.streamSnapshot())
	}
	assertS2SLiveShapedBargeInV2ReplayRuntime(t, trace, runtimeObserver, audioOutPath)
	t.Logf("replay of prior live observation: responses=%d input_commits=%d cancellation_boundaries=%d terminal=clean accounted", len(ledger.Responses), ledger.InputCommits, deterministicBargeInCancels)
}

func TestSessionCommandS2SLiveShapedBargeInV2ReplayNegativeControls(t *testing.T) {
	base, _ := loadS2SLiveShapedBargeInV2ReplayCapture(t)
	cases := []struct {
		name   string
		mutate func(*gwtesting.SessionCapture) bool
		want   string
	}{
		{
			name: "dropped replacement",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				filtered := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records))
				removed := false
				for _, record := range capture.Records {
					if deterministicJSONField(record.Payload, "response.id") == "R4" {
						removed = true
						continue
					}
					filtered = append(filtered, record)
				}
				capture.Records = filtered
				renumberDeterministicCapture(capture)
				return removed
			},
			want: "responses: expected 4, actual 3",
		},
		{
			name: "duplicate cancellation",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				for index, record := range capture.Records {
					if record.Type != "response.cancel" {
						continue
					}
					duplicate := record
					capture.Records = append(capture.Records[:index+1], append([]gwtesting.CapturedSessionEvent{duplicate}, capture.Records[index+1:]...)...)
					renumberDeterministicCapture(capture)
					return true
				}
				return false
			},
			want: "duplicate response.cancel",
		},
		{
			name: "clean unresolved outcome",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				for index := len(capture.Records) - 1; index >= 0; index-- {
					if capture.Records[index].Type != "response.done" {
						continue
					}
					capture.Records = append(capture.Records[:index], capture.Records[index+1:]...)
					renumberDeterministicCapture(capture)
					return true
				}
				return false
			},
			want: "unresolved terminal disposition",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			capture := cloneDeterministicCapture(base)
			if !testCase.mutate(&capture) {
				t.Fatal("negative control did not find its target event")
			}
			ledger, validationErr := validateDeterministicBargeInCapture(capture, testCase.name == "clean unresolved outcome")
			if validationErr == nil || !strings.Contains(validationErr.Error(), testCase.want) {
				t.Fatalf("negative control validation = %v, want detail %q; evidence=%s", validationErr, testCase.want, ledger.evidence())
			}

			_, replayPath := writeS2SLiveShapedBargeInV2ReplayCapture(t, capture)
			trace, _, _, runErr := runS2SLiveShapedBargeInV2Replay(t, replayPath)
			if runErr == nil {
				t.Fatalf("negative control replay unexpectedly completed cleanly; evidence=%s; stream=%v", ledger.evidence(), trace.streamSnapshot())
			}
		})
	}
}

func writeS2SLiveShapedBargeInV2ReplayCapture(t *testing.T, capture gwtesting.SessionCapture) (gwtesting.SessionCapture, string) {
	t.Helper()
	materializeS2SLiveShapedBargeInV2Audio(t, &capture)
	path := filepath.Join(t.TempDir(), "s2s-live-shaped-barge-in-v2-negative.session.json")
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal negative replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write negative replay capture: %v", err)
	}
	return capture, path
}
