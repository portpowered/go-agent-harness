package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
)

func TestPrepareRequiresClockAndNoOpsWhenDisabled(t *testing.T) {
	service := New()
	if prepared, err := service.Prepare(sessiontrace.Request{}); err != nil || prepared != nil {
		t.Fatalf("disabled prepare = %#v, %v", prepared, err)
	}
	if _, err := service.Prepare(sessiontrace.Request{TraceAudio: true}); !errors.Is(err, sessiontrace.ErrClockRequired) {
		t.Fatalf("missing clock error = %v", err)
	}
}

func TestPrepareReportsStagingDirectoryFailure(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New().Prepare(sessiontrace.Request{TraceAudio: true, RecordDirectory: filepath.Join(parent, "requested"), Clock: clock.Real{}})
	if err == nil {
		t.Fatal("staging failure returned nil error")
	}
}

func TestPreparedTracePoliciesAndNilDeviceCallbacks(t *testing.T) {
	prepared, err := New().Prepare(sessiontrace.Request{TraceAudio: true, RecordDirectory: filepath.Join(t.TempDir(), "requested"), Clock: clock.Real{}, CloseTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	provider, providerOK := prepared.RuntimeObserver().(sessiontrace.ProviderBoundaryObserver)
	commit, commitOK := prepared.RuntimeObserver().(sessiontrace.CommitPayloadObserver)
	if !providerOK || !provider.ObserveProviderBoundaries() || !commitOK || commit.RetainCommitPayload() {
		t.Fatal("trace policy preferences were not published")
	}
	binding := prepared.DeviceBinding()
	samples := []int16{1, 2}
	binding.PreGateSamplesObserver(16000, samples)
	binding.UploadedSamplesObserver(24000, samples)
	if err := binding.PlaybackSamplesObserver(context.Background(), 16000, samples); err != nil {
		t.Fatal(err)
	}
	binding.RenderedSamplesObserver(16000, samples)
	binding.RenderedSamplesUnavailable()
	prepared.RuntimeObserver().ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{Kind: sessiontrace.SessionRuntimeObservationAudioOutput, Payload: []byte("audio")})
	if err := prepared.Finish(context.Background(), "", true); err != nil {
		t.Fatal(err)
	}
}

func TestFinishCloseTimeoutRetainsStagedPath(t *testing.T) {
	release := make(chan struct{})
	prepared := &prepared{path: t.TempDir(), timeout: time.Millisecond, closed: make(chan struct{}), closeTrace: func() error {
		<-release
		return nil
	}}
	bundle := t.TempDir()
	if err := prepared.Finish(context.Background(), bundle, true); !errors.Is(err, sessiontrace.ErrCloseTimeout) {
		t.Fatalf("timeout error = %v", err)
	}
	close(release)
	if err := prepared.Finish(context.Background(), "", true); err != nil {
		t.Fatalf("finish after timeout = %v", err)
	}
}

func TestPreparedCapturesEdgesRedactsAndPublishes(t *testing.T) {
	root := t.TempDir()
	prepared, callbackOrder, observed := newCausalTrace(t, root)
	exerciseCausalTrace(t, prepared)
	if len(*observed) != 4 || (*observed)[1].Tick != 7 || (*observed)[2].Kind != sessiontrace.SessionRuntimeObservationTerminal || (*observed)[3].Kind != "unknown" {
		t.Fatalf("prior observer events = %#v", *observed)
	}
	if !reflect.DeepEqual(*callbackOrder, []string{"pre-gate", "uploaded", "enqueued", "rendered", "unavailable"}) {
		t.Fatalf("callback order = %#v", *callbackOrder)
	}
	events := publishCausalTrace(t, prepared, root)
	assertCausalEvents(t, events)
}

func newCausalTrace(t *testing.T, root string) (sessiontrace.Prepared, *[]string, *[]sessiontrace.SessionRuntimeObservation) {
	t.Helper()
	source := clock.NewDeterministic(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), time.Millisecond)
	callbackOrder := []string{}
	observed := []sessiontrace.SessionRuntimeObservation{}
	prior := sessiontrace.RuntimeObserverFunc(func(observation sessiontrace.SessionRuntimeObservation) {
		observed = append(observed, observation)
		if len(observation.Payload) > 0 {
			observation.Payload[0] = 'X'
		}
	})
	prepared, err := New().Prepare(sessiontrace.Request{TraceAudio: true, RecordDirectory: filepath.Join(root, "requested"), Clock: source, Credentials: []string{"", "trace-secret"}, RuntimeObserver: prior, Device: sessiontrace.DeviceBinding{
		PreGateSamplesObserver: func(int, []int16) { callbackOrder = append(callbackOrder, "pre-gate") }, UploadedSamplesObserver: func(int, []int16) { callbackOrder = append(callbackOrder, "uploaded") },
		PlaybackSamplesObserver: func(context.Context, int, []int16) error {
			callbackOrder = append(callbackOrder, "enqueued")
			return nil
		}, RenderedSamplesObserver: func(int, []int16) { callbackOrder = append(callbackOrder, "rendered") }, RenderedSamplesUnavailable: func() { callbackOrder = append(callbackOrder, "unavailable") },
	}})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.StagedPath() == filepath.Join(root, "requested") {
		t.Fatalf("trace was not staged separately: %q", prepared.StagedPath())
	}
	return prepared, &callbackOrder, &observed
}

func exerciseCausalTrace(t *testing.T, prepared sessiontrace.Prepared) {
	t.Helper()
	binding := prepared.DeviceBinding()
	samples := []int16{1, 2, 3, 4}
	binding.PreGateSamplesObserver(16000, samples)
	binding.UploadedSamplesObserver(24000, samples)
	if err := binding.PlaybackSamplesObserver(context.Background(), 16000, samples); err != nil {
		t.Fatal(err)
	}
	binding.RenderedSamplesObserver(16000, samples)
	binding.RenderedSamplesUnavailable()
	payload := []byte("Authorization: trace-secret")
	prepared.RuntimeObserver().ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{Kind: "provider_wire_send", Tick: 7, ResponseID: "response-7", ResponsePurpose: "response.create", Payload: payload, Error: "trace-secret was sent"})
	prepared.RuntimeObserver().ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{Kind: sessiontrace.SessionRuntimeObservationTerminal, Error: "terminal trace-secret failure"})
	prepared.RuntimeObserver().ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{Kind: "unknown", Error: "unknown trace-secret failure"})
	if !reflect.DeepEqual(payload, []byte("Authorization: trace-secret")) {
		t.Fatalf("observer mutated caller payload: %q", payload)
	}
}

func publishCausalTrace(t *testing.T, prepared sessiontrace.Prepared, root string) []recording.Event {
	t.Helper()
	bundle := filepath.Join(root, "bundle")
	if err := os.Mkdir(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Finish(context.Background(), bundle, true); err != nil {
		t.Fatal(err)
	}
	tracePath := filepath.Join(bundle, "audio-trace")
	for _, name := range []string{"microphone-pre-gate.wav", "microphone-uploaded.wav", "speaker-enqueued.wav", "speaker-rendered.wav", "timeline.jsonl"} {
		if _, err := os.Stat(filepath.Join(tracePath, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	return readTraceEvents(t, filepath.Join(tracePath, "timeline.jsonl"))
}

func assertCausalEvents(t *testing.T, events []recording.Event) {
	t.Helper()
	var provider, unavailable, terminal, unknown *recording.Event
	for index := range events {
		event := &events[index]
		if event.RuntimeKind == "provider_wire_send" {
			provider = event
		}
		if event.RuntimeKind == "audio_render_tap_unavailable" {
			unavailable = event
		}
		if event.RuntimeKind == "terminal" {
			terminal = event
		}
		if event.RuntimeKind == "unknown" {
			unknown = event
		}
	}
	if provider == nil || string(provider.Payload) != "Authorization: [REDACTED]" || strings.Contains(provider.Error, "trace-secret") {
		t.Fatalf("provider evidence = %#v", provider)
	}
	if unavailable == nil || terminal == nil || unknown == nil || strings.Contains(terminal.Error, "trace-secret") || strings.Contains(unknown.Error, "trace-secret") {
		t.Fatalf("runtime evidence unavailable=%#v terminal=%#v unknown=%#v", unavailable, terminal, unknown)
	}
}

func TestFinishRetainsStagedTraceOnUnpublishedDuplicateAndRenameFailure(t *testing.T) {
	newTrace := func(t *testing.T) sessiontrace.Prepared {
		t.Helper()
		prepared, err := New().Prepare(sessiontrace.Request{TraceAudio: true, RecordDirectory: filepath.Join(t.TempDir(), "requested"), Clock: clock.Real{}})
		if err != nil {
			t.Fatal(err)
		}
		return prepared
	}

	unpublished := newTrace(t)
	unpublishedBundle := t.TempDir()
	if err := unpublished.Finish(context.Background(), unpublishedBundle, false); err == nil {
		t.Fatal("unpublished trace returned nil error")
	}
	if _, err := os.Stat(filepath.Join(unpublished.StagedPath(), "timeline.jsonl")); err != nil {
		t.Fatalf("unpublished trace was not retained: %v", err)
	}

	duplicate := newTrace(t)
	duplicateBundle := t.TempDir()
	duplicatePath := filepath.Join(duplicateBundle, "audio-trace")
	if err := os.Mkdir(duplicatePath, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(duplicatePath, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := duplicate.Finish(context.Background(), duplicateBundle, true); !errors.Is(err, sessiontrace.ErrDestinationExists) {
		t.Fatalf("duplicate error = %v", err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatalf("duplicate changed existing bundle: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(duplicate.StagedPath(), "timeline.jsonl")); err != nil {
		t.Fatalf("duplicate trace was not retained: %v", err)
	}

	renameFailure := newTrace(t)
	badBundle := filepath.Join(t.TempDir(), "bundle-file")
	if err := os.WriteFile(badBundle, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renameFailure.Finish(context.Background(), badBundle, true); err == nil {
		t.Fatal("rename failure returned nil error")
	}
	if _, err := os.Stat(filepath.Join(renameFailure.StagedPath(), "timeline.jsonl")); err != nil {
		t.Fatalf("rename-failed trace was not retained: %v", err)
	}
}

func TestFinishRejectsExistingAttachmentClaim(t *testing.T) {
	prepared, err := New().Prepare(sessiontrace.Request{TraceAudio: true, RecordDirectory: filepath.Join(t.TempDir(), "requested"), Clock: clock.Real{}})
	if err != nil {
		t.Fatal(err)
	}
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "audio-trace.claim"), []byte("claimed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Finish(context.Background(), bundle, true); !errors.Is(err, sessiontrace.ErrDestinationExists) {
		t.Fatalf("claimed destination error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(prepared.StagedPath(), "timeline.jsonl")); err != nil {
		t.Fatalf("claimed trace was not retained: %v", err)
	}
}

func TestFinishHonorsCancellationAndCanCompleteLater(t *testing.T) {
	prepared, err := New().Prepare(sessiontrace.Request{TraceAudio: true, RecordDirectory: filepath.Join(t.TempDir(), "requested"), Clock: clock.Real{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := prepared.Finish(ctx, "", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled finish error = %v", err)
	}
	if err := prepared.Finish(context.Background(), "", true); err != nil {
		t.Fatalf("finish after cancellation = %v", err)
	}
}

func TestFinishRejectsNilContext(t *testing.T) {
	prepared, err := New().Prepare(sessiontrace.Request{TraceAudio: true, RecordDirectory: filepath.Join(t.TempDir(), "requested"), Clock: clock.Real{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Finish(nilContext(), "", true); err == nil {
		t.Fatal("nil context returned nil error")
	}
	if err := prepared.Finish(context.Background(), "", true); err != nil {
		t.Fatalf("finish after nil context = %v", err)
	}
}

func nilContext() context.Context { return nil }

func TestObserverChainSkipsNilObserver(t *testing.T) {
	called := false
	chain := observerChain{nil, sessiontrace.RuntimeObserverFunc(func(sessiontrace.SessionRuntimeObservation) { called = true })}
	chain.ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{Payload: []byte("payload")})
	if !called {
		t.Fatal("non-nil observer was not called")
	}
}

func readTraceEvents(t *testing.T, path string) []recording.Event {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close timeline: %v", err)
		}
	}()
	var events []recording.Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event recording.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}
