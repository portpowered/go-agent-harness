package agentruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestSessionRecordingClaimConcurrentPlansHaveOneProviderBuilder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "capture.json")
	loaded := &config.Config{Model: config.ModelConfig{
		Provider: config.ProviderGrok,
		Grok:     &config.GrokConfig{Model: "grok-claim-test", APIKey: "test-key"},
	}}
	var dialerCalls atomic.Int32
	var builderCalls atomic.Int32
	factory := sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer {
			dialerCalls.Add(1)
			return &stubRuntimeDialer{id: "claim-test"}
		},
		newRecordingDialer: defaultSessionRuntimeFactory.newRecordingDialer,
		newGrokSessionWithTools: func(_ config.GrokConfig, _ transport.Dialer, _ []messages.ToolDefinition) (messages.SessionInferencer, error) {
			builderCalls.Add(1)
			return &captureClaimNeverConnectInferencer{}, nil
		},
	}

	start := make(chan struct{})
	results := make(chan struct {
		plan sessionRuntimePlan
		err  error
	}, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			plan, err := planSessionRuntimeWithFactory(SessionRunOptions{ModelCatalog: testModelCatalog(),
				RecordPath:   path,
				Provider:     config.ProviderGrok,
				LoadedConfig: loaded,
			}, factory)
			results <- struct {
				plan sessionRuntimePlan
				err  error
			}{plan: plan, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	var winner sessionRuntimePlan
	var loser error
	winners := 0
	for result := range results {
		if result.err == nil {
			winners++
			winner = result.plan
			continue
		}
		loser = result.err
	}
	if winners != 1 || loser == nil {
		t.Fatalf("concurrent claim results = winners:%d loser:%v, want one winner and one loser", winners, loser)
	}
	defer func() { _ = winner.captureClaim.release() }()
	if builderCalls.Load() != 1 || dialerCalls.Load() != 1 {
		t.Fatalf("losing plan reached provider setup: builders=%d dialers=%d, want one each", builderCalls.Load(), dialerCalls.Load())
	}
	var claimErr *SessionRecordingClaimError
	if !errors.As(loser, &claimErr) || !errors.Is(loser, ErrSessionRecordingDestinationClaimed) {
		t.Fatalf("loser error = %T %v, want claimed destination error", loser, loser)
	}
	if !strings.Contains(loser.Error(), filepath.Clean(path)) || !strings.Contains(loser.Error(), "pid=") || !strings.Contains(loser.Error(), "host=") {
		t.Fatalf("loser error = %q, want path and non-secret holder identity", loser)
	}
	if _, err := os.Stat(path + sessionRecordingClaimSuffix); err != nil {
		t.Fatalf("winning claim sidecar disappeared before release: %v", err)
	}
}

func TestSessionRecordingClaimRejectsExistingCaptureWithoutChangingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	want := []byte(`{"version":1,"records":[{"payload":"keep me"}]}`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("write existing capture: %v", err)
	}

	err := RunSession(context.Background(), io.Discard, SessionRunOptions{ModelCatalog: testModelCatalog(),
		RecordPath: path,
		Provider:   config.ProviderGrok,
	})
	if err == nil || !errors.Is(err, ErrSessionRecordingDestinationOccupied) {
		t.Fatalf("existing capture error = %v, want occupied destination", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read existing capture: %v", readErr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("existing capture changed from %q to %q", want, got)
	}
}

func TestSessionRecordingClaimPublishesWithoutOverwriteAndCleansTemporaryState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	claim, err := acquireSessionRecordingClaim(path)
	if err != nil {
		t.Fatalf("acquire claim: %v", err)
	}
	defer func() { _ = claim.release() }()

	original := []byte("competitor bytes")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("create competing capture: %v", err)
	}
	err = claim.publish(func(tempPath string) error {
		return os.WriteFile(tempPath, []byte("claimant bytes"), 0o600)
	})
	if err == nil || !errors.Is(err, ErrSessionRecordingDestinationOccupied) {
		t.Fatalf("publish over competing capture = %v, want occupied destination", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read competing capture: %v", readErr)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("competing capture changed from %q to %q", original, got)
	}
	assertNoSessionRecordingTemporaryFiles(t, path)
}

func TestSessionRecordingClaimReleasesAfterPreDialAndWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	connectErr := errors.New("provider dial was rejected")
	flushErr := errors.New("capture write failed")
	factory := sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer { return &stubRuntimeDialer{id: "pre-dial"} },
		newRecordingDialer: func(transport.Dialer, string, string) sessionRecordingDialer {
			return &captureClaimFailingRecordingDialer{err: flushErr}
		},
		newGrokSessionWithTools: func(_ config.GrokConfig, _ transport.Dialer, _ []messages.ToolDefinition) (messages.SessionInferencer, error) {
			return &captureClaimConnectErrorInferencer{err: connectErr}, nil
		},
	}
	loaded := &config.Config{Model: config.ModelConfig{
		Provider: config.ProviderGrok,
		Grok:     &config.GrokConfig{Model: "grok-pre-dial-test", APIKey: "test-key"},
	}}
	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{ModelCatalog: testModelCatalog(),
		RecordPath:   path,
		Provider:     config.ProviderGrok,
		LoadedConfig: loaded,
	}, factory)
	if err != nil {
		t.Fatalf("plan pre-dial failure: %v", err)
	}
	var output bytes.Buffer
	err = plan.run(context.Background(), &output)
	if !errors.Is(err, connectErr) || !errors.Is(err, flushErr) {
		t.Fatalf("pre-dial/write failure = %v, want provider and capture errors", err)
	}
	if strings.Contains(output.String(), "Wrote session capture") {
		t.Fatalf("failed capture printed success summary: %q", output.String())
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed capture destination = %v, want absent", statErr)
	}
	if _, statErr := os.Stat(path + sessionRecordingClaimSuffix); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("claim sidecar = %v, want cleaned after plan failure", statErr)
	}
	assertNoSessionRecordingTemporaryFiles(t, path)
}

func assertNoSessionRecordingTemporaryFiles(t *testing.T, path string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*"))
	if err != nil {
		t.Fatalf("glob temporary captures: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary capture files remain: %v", matches)
	}
}

type captureClaimNeverConnectInferencer struct{}

func (*captureClaimNeverConnectInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, errors.New("unexpected provider connection")
}

type captureClaimConnectErrorInferencer struct{ err error }

func (i *captureClaimConnectErrorInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, i.err
}

type captureClaimFailingRecordingDialer struct {
	err error
}

func (*captureClaimFailingRecordingDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return nil, errors.New("unexpected dial")
}

func (d *captureClaimFailingRecordingDialer) FlushToFile(string) error { return d.err }
