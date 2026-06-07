package agent

import (
	"net/http"
	"path/filepath"
	"testing"
)

func TestBuildProviderHTTPRuntime_LiveModeUsesExplicitDefaultTransport(t *testing.T) {
	runtime, err := buildProviderHTTPRuntime(&Config{})
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}
	if runtime.Client == nil {
		t.Fatal("expected HTTP client")
	}
	if runtime.Client.Transport != http.DefaultTransport {
		t.Fatalf("transport = %#v, want http.DefaultTransport", runtime.Client.Transport)
	}
	if runtime.Recorder != nil {
		t.Fatal("expected no recorder in live mode")
	}
}

func TestBuildProviderHTTPRuntime_RecordModeReturnsRecorderBackedClient(t *testing.T) {
	runtime, err := buildProviderHTTPRuntime(&Config{RecordCapturePath: filepath.Join(t.TempDir(), "capture.json")})
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}
	if runtime.Client == nil {
		t.Fatal("expected HTTP client")
	}
	if runtime.Recorder == nil {
		t.Fatal("expected recorder in record mode")
	}
	if runtime.Client.Transport != runtime.Recorder {
		t.Fatal("expected recorder transport to back the client")
	}
}

func TestBuildProviderHTTPRuntime_ReplayModeUsesReplayTransport(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "test", "integration", "testdata", "streaming_2_2.json")
	runtime, err := buildProviderHTTPRuntime(&Config{ReplayCapturePath: fixturePath})
	if err != nil {
		t.Fatalf("buildProviderHTTPRuntime() error = %v", err)
	}
	if runtime.Client == nil {
		t.Fatal("expected HTTP client")
	}
	if runtime.Client.Transport == nil {
		t.Fatal("expected replay transport")
	}
	if runtime.Client.Transport == http.DefaultTransport {
		t.Fatal("expected replay transport, got http.DefaultTransport")
	}
	if runtime.Recorder != nil {
		t.Fatal("expected no recorder in replay mode")
	}
}

func TestBuildProviderHTTPRuntime_ReplayModePropagatesFixtureErrors(t *testing.T) {
	_, err := buildProviderHTTPRuntime(&Config{ReplayCapturePath: filepath.Join(t.TempDir(), "missing.json")})
	if err == nil {
		t.Fatal("expected replay fixture error")
	}
}
