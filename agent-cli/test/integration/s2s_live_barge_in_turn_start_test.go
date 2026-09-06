package integration

import servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// newTurnStartPlainSpeechAudioReader makes the two sides of the turn-start
// race explicit. The second utterance is released after response 1 has been
// created but before its first output; the third utterance is released only
// after response 2 has reached MESSAGE.END.
func newTurnStartPlainSpeechAudioReader(trace *plainSpeechTrace) *plainSpeechAudioReader {
	return &plainSpeechAudioReader{
		segments: []plainSpeechAudioSegment{
			{frame: plainSpeechFrame(1), endOfTurn: true},
			{frame: plainSpeechFrame(2), gate: func(ctx context.Context) error {
				return trace.waitForCreated(ctx, 1)
			}, endOfTurn: true},
			{frame: plainSpeechFrame(3), gate: func(ctx context.Context) error {
				return trace.waitForDone(ctx, 2)
			}},
		},
	}
}

func runTurnStartPlainSpeechCLI(t *testing.T) plainSpeechRun {
	t.Helper()
	trace := newPlainSpeechTrace()
	server := newTurnStartPlainSpeechServer()
	t.Cleanup(server.shutdown)
	recorder := newPlainSpeechRecordingDialer(server)

	agentCLI, err := newPlainSpeechSessionCLI(recorder)
	if err != nil {
		t.Fatalf("initialize turn-start CLI: %v", err)
	}
	agentCLI.SetSessionStreamObserver(trace.observe)
	root := agentCLI.Generate()
	root.SetIn(newTurnStartPlainSpeechAudioReader(trace))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"--config-dir", filepath.Join(t.TempDir(), "config"),
		"session",
		"--record-dir", filepath.Join(t.TempDir(), "recording"),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--audio-in", "-",
		"--max-duration", plainSpeechRunTimeout.String(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), plainSpeechRunTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	var runErr error
	select {
	case runErr = <-done:
	case <-ctx.Done():
		timer := time.NewTimer(plainSpeechCommandJoinWait)
		select {
		case runErr = <-done:
			timer.Stop()
		case <-timer.C:
			runErr = fmt.Errorf("turn-start CLI command await timed out at %s: %w", plainSpeechRunTimeout, probe.ErrBargeInWait)
		}
	}
	return plainSpeechRun{capture: recorder.Capture(), trace: trace, server: server, err: runErr}
}

// These small composition helpers keep the turn-start scenario on the same
// shipped CLI path as the plain-speech proof without duplicating its provider
// setup. The concrete types remain in the integration package so the test
// cannot accidentally bypass command wiring.
func newPlainSpeechRecordingDialer(server *plainSpeechServer) *gwtesting.RecordingWebSocketDialer {
	return gwtesting.NewRecordingWebSocketDialer(server, "openai", "gpt-realtime")
}

func newPlainSpeechSessionCLI(recorder *gwtesting.RecordingWebSocketDialer) (*cli.AgentCLI, error) {
	sessionInferencer, err := servicetest.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(recorder),
		oaiprovider.WithClientOwnedAudioTurnBoundaries(),
	)
	if err != nil {
		return nil, err
	}
	return wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencer{response: "stateless inferencer should not be called"},
		sessionInferencer,
	)
}

func turnStartPlainSpeechContract() probe.BargeInContract {
	return probe.BargeInContract{
		Inputs: []probe.BargeInInputExpectation{
			{ID: "input-1", TurnID: "turn-1"},
			{ID: "input-2", TurnID: "turn-2"},
			{ID: "input-3", TurnID: "turn-3"},
		},
		Responses: []probe.BargeInResponseExpectation{
			{
				ID: "response-1", InputID: "input-1", TurnID: "turn-1",
				Disposition:   probe.BargeInDispositionCancelled,
				RequireCancel: true, ForbidOutput: true,
			},
			{
				ID: "response-2", InputID: "input-2", TurnID: "turn-2",
				Disposition:  probe.BargeInDispositionCompleted,
				ForbidCancel: true, RequireOutput: true, RequireContinuation: true,
			},
			{
				ID: "response-3", InputID: "input-3", TurnID: "turn-3",
				Disposition:  probe.BargeInDispositionCompleted,
				ForbidCancel: true, RequireOutput: true, RequireContinuation: true,
			},
		},
		RequireSessionTerminal: true,
	}
}

func validateTurnStartPlainSpeechCapture(capture gwtesting.SessionCapture) error {
	return normalizePlainSpeechCapture(capture, "").Validate(turnStartPlainSpeechContract())
}

func turnStartResponseRecordIndex(capture gwtesting.SessionCapture, eventType, responseID string, occurrence int) int {
	return plainSpeechRecordIndex(capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Type == eventType && plainSpeechRecordResponseID(record) == responseID
	}, occurrence)
}

func turnStartClientRecordIndex(capture gwtesting.SessionCapture, eventType string, occurrence int) int {
	return plainSpeechRecordIndex(capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionClientToServer && record.Type == eventType
	}, occurrence)
}

func turnStartPayloadWithResponseID(payload []byte, id string) []byte {
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return payload
	}
	if response, ok := value["response"].(map[string]any); ok {
		response["id"] = id
	} else {
		value["response_id"] = id
	}
	mutated, err := json.Marshal(value)
	if err != nil {
		return payload
	}
	return mutated
}

func moveTurnStartRecordAfter(capture *gwtesting.SessionCapture, match, after func(gwtesting.CapturedSessionEvent) bool) bool {
	movedIndex := plainSpeechRecordIndex(*capture, match, 0)
	if movedIndex < 0 {
		return false
	}
	moved := capture.Records[movedIndex]
	capture.Records = append(capture.Records[:movedIndex], capture.Records[movedIndex+1:]...)
	renumberPlainSpeechCapture(capture)
	afterIndex := plainSpeechRecordIndex(*capture, after, 0)
	if afterIndex < 0 {
		return false
	}
	insertPlainSpeechRecordAfter(capture, afterIndex, moved)
	return true
}

func TestS2SLiveBargeInTurnStartCollisionMatrix(t *testing.T) {
	run := runTurnStartPlainSpeechCLI(t)
	if run.err != nil {
		dialCount, responses, protocolErrs := run.server.snapshot()
		t.Fatalf("turn-start CLI returned %v; dial_count=%d responses=%v protocol_errors=%v stream=%v", run.err, dialCount, responses, protocolErrs, run.trace.snapshot())
	}
	if err := validateTurnStartPlainSpeechCapture(run.capture); err != nil {
		t.Fatalf("turn-start identity-aware ledger failed: %v; stream=%v", err, run.trace.snapshot())
	}

	firstCreated := turnStartResponseRecordIndex(run.capture, "response.created", "response-plain-1", 0)
	firstOutput := turnStartResponseRecordIndex(run.capture, "response.output_audio.delta", "response-plain-1", 0)
	firstCancel := turnStartClientRecordIndex(run.capture, "response.cancel", 0)
	secondAppend := turnStartClientRecordIndex(run.capture, "input_audio_buffer.append", 1)
	firstTerminal := turnStartResponseRecordIndex(run.capture, "response.done", "response-plain-1", 0)
	secondTerminal := turnStartResponseRecordIndex(run.capture, "response.done", "response-plain-2", 0)
	thirdAppend := turnStartClientRecordIndex(run.capture, "input_audio_buffer.append", 2)
	thirdCreated := turnStartResponseRecordIndex(run.capture, "response.created", "response-plain-3", 0)
	secondCancel := turnStartClientRecordIndex(run.capture, "response.cancel", 1)
	if firstCreated < 0 || firstCancel < 0 || secondAppend < 0 || firstTerminal < 0 || secondTerminal < 0 || thirdAppend < 0 || thirdCreated < 0 {
		t.Fatalf("turn-start boundaries are incomplete: created=%d first_output=%d cancel=%d second_append=%d first_terminal=%d second_terminal=%d third_append=%d third_created=%d records=%v", firstCreated, firstOutput, firstCancel, secondAppend, firstTerminal, secondTerminal, thirdAppend, thirdCreated, run.capture.Records)
	}
	if firstOutput >= 0 {
		t.Fatalf("response 1 leaked first output before its turn-start cancellation: output=%d records=%v", firstOutput, run.capture.Records)
	}
	if secondCancel >= 0 {
		t.Fatalf("completion-winning response 2 was cancelled at sequence %d; records=%v", secondCancel, run.capture.Records)
	}
	if !(firstCreated < firstCancel && firstCancel < secondAppend && secondAppend < firstTerminal) {
		t.Fatalf("input-winning turn-start order = created:%d cancel:%d append:%d terminal:%d", firstCreated, firstCancel, secondAppend, firstTerminal)
	}
	if !(secondTerminal < thirdAppend && thirdAppend < thirdCreated) {
		t.Fatalf("completion-winning turn-start order = second_terminal:%d third_append:%d third_created:%d", secondTerminal, thirdAppend, thirdCreated)
	}

	dialCount, responses, protocolErrs := run.server.snapshot()
	if dialCount != 1 || len(responses) != 3 || len(protocolErrs) != 0 {
		t.Fatalf("turn-start provider observations = dials:%d responses:%d protocol_errors:%v; want one session, three responses, and no protocol errors", dialCount, len(responses), protocolErrs)
	}
	for index, response := range responses {
		wantCancel := 0
		if index == 0 {
			wantCancel = 1
		}
		if response.CancelCount != wantCancel || !response.TerminalSent {
			t.Fatalf("provider response %q = cancel:%d terminal:%t, want cancel:%d and terminal", response.ID, response.CancelCount, response.TerminalSent, wantCancel)
		}
	}
}

func TestS2SLiveBargeInTurnStartOracleRejectsNamedMutations(t *testing.T) {
	run := runTurnStartPlainSpeechCLI(t)
	if run.err != nil {
		t.Fatalf("build positive turn-start capture for negative controls: %v", run.err)
	}
	cases := []struct {
		name   string
		mutate func(*gwtesting.SessionCapture) bool
		want   string
	}{
		{
			name: "missing cancel",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				index := turnStartClientRecordIndex(*capture, "response.cancel", 0)
				if index < 0 {
					return false
				}
				capture.Records = append(capture.Records[:index], capture.Records[index+1:]...)
				renumberPlainSpeechCapture(capture)
				return true
			},
			want: `response "response-1" was marked cancelled without a cancellation event`,
		},
		{
			name: "late cancel",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				return moveTurnStartRecordAfter(capture,
					func(record gwtesting.CapturedSessionEvent) bool {
						return record.Direction == gwtesting.DirectionClientToServer && record.Type == "response.cancel"
					},
					func(record gwtesting.CapturedSessionEvent) bool {
						return record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.done" && plainSpeechRecordResponseID(record) == "response-plain-1"
					})
			},
			want: "cancellation references unknown response",
		},
		{
			name: "duplicate cancel",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				index := turnStartClientRecordIndex(*capture, "response.cancel", 0)
				if index < 0 {
					return false
				}
				insertPlainSpeechRecordAfter(capture, index, capture.Records[index])
				return true
			},
			want: `response "response-1" received duplicate cancellation`,
		},
		{
			name: "first output leakage",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				outputIndex := turnStartResponseRecordIndex(*capture, "response.output_audio.delta", "response-plain-2", 0)
				createdIndex := turnStartResponseRecordIndex(*capture, "response.created", "response-plain-1", 0)
				if outputIndex < 0 || createdIndex < 0 {
					return false
				}
				leaked := capture.Records[outputIndex]
				leaked.Payload = turnStartPayloadWithResponseID(plainSpeechRecordPayload(leaked), "response-plain-1")
				leaked.Data = nil
				insertPlainSpeechRecordAfter(capture, createdIndex, leaked)
				return true
			},
			want: `response "response-1" emitted 1 non-empty output events although output is forbidden`,
		},
		{
			name: "completion assigned to wrong turn",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				index := turnStartResponseRecordIndex(*capture, "response.done", "response-plain-2", 0)
				if index < 0 {
					return false
				}
				capture.Records[index].Payload = turnStartPayloadWithResponseID(plainSpeechRecordPayload(capture.Records[index]), "response-plain-1")
				capture.Records[index].Data = nil
				return true
			},
			want: `response "response-1" received duplicate terminal disposition`,
		},
		{
			name: "dropped replacement",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				before := len(capture.Records)
				removePlainSpeechRecords(capture, func(record gwtesting.CapturedSessionEvent) bool {
					return plainSpeechRecordResponseID(record) == "response-plain-2"
				})
				return len(capture.Records) != before
			},
			want: `missing response "response-3"`,
		},
		{
			name: "clean unresolved close",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				index := turnStartResponseRecordIndex(*capture, "response.done", "response-plain-3", 0)
				if index < 0 {
					return false
				}
				capture.Records = append(capture.Records[:index], capture.Records[index+1:]...)
				renumberPlainSpeechCapture(capture)
				return true
			},
			want: `response "response-3" has unresolved terminal disposition`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			capture := clonePlainSpeechCapture(run.capture)
			if !testCase.mutate(&capture) {
				t.Fatal("negative control did not find its target event")
			}
			err := validateTurnStartPlainSpeechCapture(capture)
			if err == nil {
				t.Fatal("negative control unexpectedly passed the turn-start ledger")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("negative-control error = %v, want detail %q", err, testCase.want)
			}
		})
	}
}
