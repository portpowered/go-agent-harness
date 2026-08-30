package config

import (
	"reflect"
	"testing"
	"time"
)

func TestBrowserConfigApplyBrowserOverridesPreservesUnspecifiedValues(t *testing.T) {
	base := BrowserConfig{
		Tools: BrowserToolsConfig{Enabled: false, Backend: BrowserToolsBackendWebMCP},
		Connection: BrowserConnectionConfig{
			CDPURL:           "http://file.example:9222",
			WSEndpoint:       "ws://file.example/devtools/browser/id",
			UserDataDir:      "/file/profile",
			AllowProcessScan: true,
			AllowRemoteCDP:   true,
		},
		Managed: BrowserManagedConfig{
			Headless:    true,
			Open:        "https://file.example/start",
			CloseOnExit: true,
		},
		Selection: BrowserSelectionConfig{
			Browser:     "file-browser",
			Tab:         "file-tab",
			Origin:      "https://file.example",
			AutoSelect:  BrowserAutoSelectPersisted,
			ActivateTab: true,
			Persist:     true,
		},
		Policy: BrowserPolicyConfig{
			AllowedOrigins:    []string{"https://allowed.file"},
			DeniedOrigins:     []string{"https://denied.file"},
			Approval:          BrowserApprovalAlways,
			CancelOnInterrupt: BrowserCancelOnInterruptAlways,
		},
		Limits: BrowserLimitsConfig{
			InvocationTimeout:  5 * time.Second,
			MaxInputBytes:      11,
			MaxResultBytes:     12,
			SerializePerTarget: false,
		},
		Recording: BrowserRecordingConfig{
			Enabled:           true,
			IncludeArguments:  false,
			IncludeResults:    false,
			RedactURLQuery:    false,
			RedactURLFragment: false,
		},
		Replay: BrowserReplayConfig{Path: "/file/replay.jsonl", Strict: false},
	}

	allowed := []string{"https://cli.example"}
	maxResultBytes := 99
	overrides := BrowserOverrides{
		ToolsBackend:      stringPtr(BrowserToolsBackendWebMCP),
		AllowedOrigins:    &allowed,
		MaxResultBytes:    &maxResultBytes,
		PersistSelection:  boolPtr(false),
		InvocationTimeout: durationPtr(9 * time.Second),
	}

	got, err := base.ApplyBrowserOverrides(overrides)
	if err != nil {
		t.Fatalf("ApplyBrowserOverrides(): %v", err)
	}

	if !got.Tools.Enabled || got.Tools.Backend != BrowserToolsBackendWebMCP {
		t.Fatalf("tools = %+v, want explicitly enabled WebMCP", got.Tools)
	}
	if got.Selection.Persist || got.Limits.MaxResultBytes != 99 || got.Limits.InvocationTimeout != 9*time.Second {
		t.Fatalf("explicit overrides were not applied: %+v/%+v", got.Selection, got.Limits)
	}
	if !reflect.DeepEqual(got.Policy.AllowedOrigins, allowed) {
		t.Fatalf("allowed origins = %v, want %v", got.Policy.AllowedOrigins, allowed)
	}
	if got.Connection.CDPURL != base.Connection.CDPURL || got.Connection.WSEndpoint != base.Connection.WSEndpoint || got.Selection.Tab != base.Selection.Tab || got.Policy.Approval != base.Policy.Approval || got.Replay != base.Replay {
		t.Fatalf("unspecified values changed: got=%+v base=%+v", got, base)
	}
	if got.Managed != base.Managed {
		t.Fatalf("unspecified managed values changed: got=%+v base=%+v", got.Managed, base.Managed)
	}

	managedOpen := "https://cli.example/start"
	managedHeadless := false
	managedClose := false
	managed, err := base.ApplyBrowserOverrides(BrowserOverrides{
		ManagedOpen:        &managedOpen,
		ManagedHeadless:    &managedHeadless,
		ManagedCloseOnExit: &managedClose,
	})
	if err != nil {
		t.Fatalf("ApplyBrowserOverrides(managed): %v", err)
	}
	if managed.Managed.Open != managedOpen || managed.Managed.Headless || managed.Managed.CloseOnExit {
		t.Fatalf("managed overrides = %+v", managed.Managed)
	}
	if &got.Policy.AllowedOrigins[0] == &base.Policy.AllowedOrigins[0] {
		t.Fatal("allowed origin slice aliases the base config")
	}

	emptyBase := DefaultBrowserConfig()
	emptyBase.Policy.AllowedOrigins = []string{}
	emptyBase.Policy.DeniedOrigins = []string{}
	unchanged, err := emptyBase.ApplyBrowserOverrides(BrowserOverrides{})
	if err != nil {
		t.Fatalf("ApplyBrowserOverrides(empty): %v", err)
	}
	if unchanged.Policy.AllowedOrigins == nil || unchanged.Policy.DeniedOrigins == nil {
		t.Fatalf("empty origin slices lost their shape: %+v", unchanged.Policy)
	}
}

func stringPtr(value string) *string { return &value }

func boolPtr(value bool) *bool { return &value }

func durationPtr(value time.Duration) *time.Duration { return &value }
