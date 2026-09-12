package wire

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioncontinuation"
)

func TestNewServiceReturnsPublicContract(t *testing.T) {
	var service sessioncontinuation.Service = NewService()
	if service == nil {
		t.Fatal("NewService returned nil")
	}
	if got := service.FormatMetadata(map[string]string{"b": "2", "a": "1"}); got != "a=1, b=2" {
		t.Fatalf("metadata = %q, want deterministic ordering", got)
	}
}

func TestNewAliasReturnsPublicContract(t *testing.T) {
	if New() == nil {
		t.Fatal("New returned nil")
	}
}

func newService(t *testing.T) sessioncontinuation.Service {
	t.Helper()
	service := NewService()
	if service == nil {
		t.Fatal("NewService returned a nil continuation service")
	}
	return service
}

func TestNormalizeBlankDuplicateLexicalIDsAndFirstNonSuccess(t *testing.T) {
	service := newService(t)
	input := sessioncontinuation.Snapshot{Unresolved: sessioncontinuation.UnresolvedToolResultsSnapshot{
		CallIDs: []string{" z ", "", "a", "z", "b"},
		SendStatuses: map[string]messages.SessionSendStatus{
			"z": messages.SessionSendClosed,
			"b": messages.SessionSendCancelled,
		},
		SendAttempts: []sessioncontinuation.SendAttempt{
			{CallID: " z ", Status: messages.SessionSendBufferFull},
			{CallID: "z", Status: messages.SessionSendClosed},
			{CallID: " c ", Status: messages.SessionSendTimedOut},
		},
	}}
	got := service.Normalize(input)
	if want := []string{"a", "b", "c", "z"}; !reflect.DeepEqual(got.Unresolved.CallIDs, want) {
		t.Fatalf("call IDs = %v, want %v", got.Unresolved.CallIDs, want)
	}
	if got.Unresolved.SendStatuses["z"] != messages.SessionSendBufferFull {
		t.Fatalf("z status = %q, want first non-success %q", got.Unresolved.SendStatuses["z"], messages.SessionSendBufferFull)
	}
	if got.Unresolved.SendStatuses["c"] != messages.SessionSendTimedOut {
		t.Fatalf("c status = %q, want timed out", got.Unresolved.SendStatuses["c"])
	}
	if len(got.Unresolved.SendAttempts) != 3 || got.Unresolved.SendAttempts[0].CallID != "z" {
		t.Fatalf("normalized attempts = %#v, want copied ordered observations", got.Unresolved.SendAttempts)
	}
}

func TestNormalizeImmutableSnapshotsAndMetadata(t *testing.T) {
	service := newService(t)
	ids := []string{" b ", "a"}
	statuses := map[string]messages.SessionSendStatus{"a": messages.SessionSendCancelled}
	metadata := map[string]string{" tool ": "ok", "code": "E1"}
	input := sessioncontinuation.Snapshot{
		Unresolved: sessioncontinuation.UnresolvedToolResultsSnapshot{CallIDs: ids, SendStatuses: statuses},
		Tool:       sessioncontinuation.ContinuationSnapshot{CallIDs: []string{"tool"}, ProviderStatuses: metadata},
	}
	got := service.Normalize(input)
	ids[0] = "changed"
	statuses["a"] = messages.SessionSendClosed
	metadata[" status "] = "changed"
	if !reflect.DeepEqual(got.Unresolved.CallIDs, []string{"a", "b"}) || got.Unresolved.SendStatuses["a"] != messages.SessionSendCancelled {
		t.Fatalf("normalized unresolved snapshot changed after input mutation: %#v", got.Unresolved)
	}
	if got.Tool.ProviderStatuses["tool"] != "ok" {
		t.Fatalf("normalized metadata = %#v, want copied trimmed key", got.Tool.ProviderStatuses)
	}
	got.Unresolved.CallIDs[0] = "output-mutated"
	got.Unresolved.SendStatuses["a"] = messages.SessionSendTimedOut
	if ids[1] != "a" || statuses["a"] != messages.SessionSendClosed {
		t.Fatalf("output mutation aliased input: ids=%v statuses=%v", ids, statuses)
	}
}

func TestMetadataIsStableAndLexical(t *testing.T) {
	service := newService(t)
	values := map[string]string{"b": "2", "a": "1", " ": "ignored", " a ": "shadow"}
	if got, want := service.FormatMetadata(values), "a=1, b=2"; got != want {
		t.Fatalf("metadata = %q, want %q", got, want)
	}
	if got := service.FormatMetadata(nil); got != "" {
		t.Fatalf("empty metadata = %q, want empty", got)
	}
}

func TestEnrichUnresolvedImageAndToolErrorsPreservesIdentityMetadataAndPrecedence(t *testing.T) {
	service := newService(t)
	providerErr := errors.New("provider closed before continuation")
	got := service.Enrich(providerErr, sessioncontinuation.Snapshot{
		Unresolved: sessioncontinuation.UnresolvedToolResultsSnapshot{
			CallIDs:      []string{" unresolved ", "unresolved"},
			SendStatuses: map[string]messages.SessionSendStatus{"unresolved": messages.SessionSendClosed},
		},
		Tool: sessioncontinuation.ContinuationSnapshot{
			CallIDs:          []string{"image-call", "ordinary-call"},
			ProviderStatuses: map[string]string{"image-call": "should-be-image", "ordinary-call": "failed"},
			ProviderCodes:    map[string]string{"ordinary-call": "E_TOOL"},
			ProviderDetails:  map[string]string{"ordinary-call": "provider detail"},
		},
		Image: sessioncontinuation.ContinuationSnapshot{
			CallIDs:          []string{" image-call "},
			ProviderStatuses: map[string]string{"image-call": "incomplete"},
			ProviderCodes:    map[string]string{"image-call": "E_IMAGE"},
			ProviderDetails:  map[string]string{"image-call": "image detail"},
		},
	})
	if !errors.Is(got, providerErr) || !errors.Is(got, sessioncontinuation.ErrUnresolvedToolResults) || !errors.Is(got, sessioncontinuation.ErrImageContinuationIncomplete) || !errors.Is(got, sessioncontinuation.ErrToolContinuationIncomplete) {
		t.Fatalf("enriched error lost a cause: %v", got)
	}
	var unresolved *sessioncontinuation.UnresolvedToolResultsError
	if !errors.As(got, &unresolved) || !reflect.DeepEqual(unresolved.CallIDs, []string{"unresolved"}) || unresolved.SendStatuses["unresolved"] != messages.SessionSendClosed {
		t.Fatalf("unresolved error = %#v", unresolved)
	}
	var image *sessioncontinuation.ImageContinuationError
	if !errors.As(got, &image) || !reflect.DeepEqual(image.CallIDs, []string{"image-call"}) || image.ProviderCodes["image-call"] != "E_IMAGE" {
		t.Fatalf("image error = %#v", image)
	}
	if !strings.Contains(image.Error(), "status=incomplete") || !strings.Contains(image.Error(), "detail=image detail") {
		t.Fatalf("image error omitted provider metadata: %v", image)
	}
	var tool *sessioncontinuation.ToolContinuationError
	if !errors.As(got, &tool) || !reflect.DeepEqual(tool.CallIDs, []string{"ordinary-call"}) || tool.ProviderDetails["ordinary-call"] != "provider detail" {
		t.Fatalf("tool error = %#v", tool)
	}
	if strings.Contains(tool.Error(), "image-call") {
		t.Fatalf("ordinary tool error contains image obligation: %v", tool)
	}
}

func TestEnrichSuppressesDuplicatesAndPreservesPrimaryCauses(t *testing.T) {
	service := newService(t)
	primary := errors.Join(errors.New("provider"), context.Canceled, context.DeadlineExceeded)
	snapshot := sessioncontinuation.Snapshot{Tool: sessioncontinuation.ContinuationSnapshot{CallIDs: []string{"call"}}}
	once := service.Enrich(primary, snapshot)
	twice := service.Enrich(once, snapshot)
	if !errors.Is(twice, primary) || !errors.Is(twice, context.Canceled) || !errors.Is(twice, context.DeadlineExceeded) {
		t.Fatalf("duplicate enrichment lost primary causes: %v", twice)
	}
	if countTyped[*sessioncontinuation.ToolContinuationError](twice) != 1 {
		t.Fatalf("tool continuation count = %d, want one", countTyped[*sessioncontinuation.ToolContinuationError](twice))
	}
}

func TestJoinAudioOutputErrorPreservesDistinctOutputCause(t *testing.T) {
	service := newService(t)
	primary := errors.New("response incomplete")
	output := errors.New("wav write failed")
	joined := service.JoinAudioOutputError(primary, "answer.wav", output)
	if !errors.Is(joined, primary) || !errors.Is(joined, output) || !strings.Contains(joined.Error(), `--audio-out "answer.wav"`) {
		t.Fatalf("joined audio error = %v", joined)
	}
	if got := service.JoinAudioOutputError(joined, "answer.wav", output); got != joined {
		t.Fatalf("duplicate output cause returned a new error: %v", got)
	}
	if got := service.JoinAudioOutputError(primary, "answer.wav", nil); got != primary {
		t.Fatalf("nil output changed primary: %v", got)
	}
}

func TestCancelAndZeroSurvivors(t *testing.T) {
	service := newService(t)
	if got := service.Enrich(nil, sessioncontinuation.Snapshot{}); got != nil {
		t.Fatalf("empty enrichment = %v, want nil", got)
	}
	if got := service.Enrich(context.Canceled, sessioncontinuation.Snapshot{}); !errors.Is(got, context.Canceled) {
		t.Fatalf("cancellation cause was lost: %v", got)
	}
}

func TestIndependentConcurrentInstances(t *testing.T) {
	first, second := newService(t), newService(t)
	const workers = 32
	var wait sync.WaitGroup
	errorsOut := make(chan error, workers)
	wait.Add(workers)
	for i := 0; i < workers; i++ {
		service := first
		if i%2 == 0 {
			service = second
		}
		go func(i int) {
			defer wait.Done()
			id := "call-" + string(rune('a'+i))
			got := service.Enrich(nil, sessioncontinuation.Snapshot{Tool: sessioncontinuation.ContinuationSnapshot{CallIDs: []string{id}}})
			var typed *sessioncontinuation.ToolContinuationError
			if !errors.As(got, &typed) || len(typed.CallIDs) != 1 || typed.CallIDs[0] != id {
				errorsOut <- errors.New("concurrent instance returned the wrong typed snapshot")
			}
		}(i)
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Fatal(err)
	}
}

func countTyped[T error](err error) int {
	if err == nil {
		return 0
	}
	count := 0
	var typed T
	if errors.As(err, &typed) {
		count = 1
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		count = 0
		for _, child := range joined.Unwrap() {
			count += countTyped[T](child)
		}
		return count
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return count + countTyped[T](wrapped.Unwrap())
	}
	return count
}
