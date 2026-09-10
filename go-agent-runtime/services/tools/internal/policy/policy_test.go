package policy

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func TestFactoryDefaultsAndOverrides(t *testing.T) {
	factory := New()
	resolved, err := factory.Resolve(public.InteractiveToolPolicyRequest{
		Definitions: []messages.ToolDefinition{
			{Name: "read_file"},
			{Name: "exec"},
			{Name: "sleep"},
		},
	})
	if err != nil {
		t.Fatalf("Resolve defaults: %v", err)
	}
	if got, want := resolved.Settings(), (public.InteractiveToolPolicySettings{
		FastReadTimeout:          public.DefaultInteractiveFastReadTimeout,
		LongRunningTimeout:       public.DefaultInteractiveLongRunningTimeout,
		AcknowledgementThreshold: public.DefaultInteractiveAcknowledgementThreshold,
	}); got != want {
		t.Fatalf("default settings = %#v, want %#v", got, want)
	}
	if got := resolved.ClassForTool("read_file"); got != public.InteractiveToolClassFastRead {
		t.Fatalf("read_file class = %s, want fast/read", got)
	}
	for _, name := range []string{"exec", "sleep"} {
		if got := resolved.ClassForTool(name); got != public.InteractiveToolClassBoundedLongRunning {
			t.Fatalf("%s class = %s, want bounded-long-running", name, got)
		}
		if got := resolved.TimeoutForTool(name); got != public.DefaultInteractiveLongRunningTimeout {
			t.Fatalf("%s timeout = %s, want %s", name, got, public.DefaultInteractiveLongRunningTimeout)
		}
	}
	if got := resolved.TimeoutForTool("read_file"); got != public.DefaultInteractiveFastReadTimeout {
		t.Fatalf("read_file timeout = %s, want %s", got, public.DefaultInteractiveFastReadTimeout)
	}

	override := public.InteractiveToolPolicySettings{
		FastReadTimeout:          7 * time.Second,
		LongRunningTimeout:       15 * time.Second,
		AcknowledgementThreshold: 1200 * time.Millisecond,
	}
	resolved, err = factory.Resolve(public.InteractiveToolPolicyRequest{
		Settings:    override,
		Definitions: []messages.ToolDefinition{{Name: "slow_read"}, {Name: "exec"}},
	})
	if err != nil {
		t.Fatalf("Resolve override: %v", err)
	}
	if got := resolved.Settings(); got != override {
		t.Fatalf("override settings = %#v, want %#v", got, override)
	}
	if got := resolved.TimeoutForTool("slow_read"); got != 7*time.Second {
		t.Fatalf("overridden fast timeout = %s", got)
	}
	if got := resolved.TimeoutForTool("exec"); got != 15*time.Second {
		t.Fatalf("overridden long timeout = %s", got)
	}
}

func TestFactoryValidationOrderAndBoundaries(t *testing.T) {
	factory := New()
	defaults := public.InteractiveToolPolicySettings{
		FastReadTimeout:          public.DefaultInteractiveFastReadTimeout,
		LongRunningTimeout:       public.DefaultInteractiveLongRunningTimeout,
		AcknowledgementThreshold: public.DefaultInteractiveAcknowledgementThreshold,
	}
	tests := []struct {
		name   string
		mutate func(*public.InteractiveToolPolicySettings)
		want   string
	}{
		{name: "zero fast read", mutate: func(s *public.InteractiveToolPolicySettings) { s.FastReadTimeout = 0 }, want: "fast_read_timeout"},
		{name: "negative fast read", mutate: func(s *public.InteractiveToolPolicySettings) { s.FastReadTimeout = -time.Second }, want: "fast_read_timeout"},
		{name: "fast read limit", mutate: func(s *public.InteractiveToolPolicySettings) {
			s.FastReadTimeout = public.InteractiveFastReadTimeoutLimit
		}, want: "less than 10s"},
		{name: "zero long running", mutate: func(s *public.InteractiveToolPolicySettings) { s.LongRunningTimeout = 0 }, want: "long_running_timeout"},
		{name: "negative long running", mutate: func(s *public.InteractiveToolPolicySettings) { s.LongRunningTimeout = -time.Second }, want: "long_running_timeout"},
		{name: "long running limit", mutate: func(s *public.InteractiveToolPolicySettings) {
			s.LongRunningTimeout = public.InteractiveLongRunningTimeoutLimit
		}, want: "less than 30s"},
		{name: "zero acknowledgement", mutate: func(s *public.InteractiveToolPolicySettings) { s.AcknowledgementThreshold = 0 }, want: "acknowledgement_threshold"},
		{name: "late acknowledgement", mutate: func(s *public.InteractiveToolPolicySettings) {
			s.AcknowledgementThreshold = public.InteractiveAcknowledgementThresholdLimit + time.Nanosecond
		}, want: "no greater than 2s"},
		{name: "long running does not exceed acknowledgement", mutate: func(s *public.InteractiveToolPolicySettings) { s.LongRunningTimeout = 2 * time.Second }, want: "must exceed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := defaults
			tt.mutate(&settings)
			err := factory.ValidateSettings(settings)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateSettings() = %v, want an error containing %q", err, tt.want)
			}
			_, err = factory.Resolve(public.InteractiveToolPolicyRequest{Settings: settings})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Resolve() = %v, want an error containing %q", err, tt.want)
			}
		})
	}

	if err := factory.ValidateSettings(public.InteractiveToolPolicySettings{}); err == nil {
		t.Fatal("ValidateSettings(zero) succeeded; direct zero policy must remain invalid")
	}
	if _, err := factory.Resolve(public.InteractiveToolPolicyRequest{}); err != nil {
		t.Fatalf("Resolve(zero request) = %v, want runtime defaults", err)
	}
}

func TestFactoryCatalogNormalizationExplicitNamesAndDynamicFallback(t *testing.T) {
	explicitNames := []string{
		"browser.select",
		"browser.invoke",
		"browser.list-tools",
		"browser.list-tabs",
		"browser.context",
		"browser.cancel",
		"browser.list-cast-devices",
		"browser.cast",
		"browser.stop-casting",
	}
	base := []messages.ToolDefinition{{Name: " read_file "}}
	definitions := append([]messages.ToolDefinition{{Name: ""}, {Name: "  "}}, base...)
	for _, name := range explicitNames {
		definitions = append(definitions, messages.ToolDefinition{Name: " " + name + " "})
	}
	definitions = append(definitions,
		messages.ToolDefinition{Name: "page_tool"},
		messages.ToolDefinition{Name: "page_tool"},
	)

	resolved, err := New().Resolve(public.InteractiveToolPolicyRequest{
		Definitions:              definitions,
		BaseDefinitions:          base,
		ExplicitLongRunningNames: explicitNames,
		DynamicLongRunning:       true,
	})
	if err != nil {
		t.Fatalf("Resolve catalog: %v", err)
	}
	if got := resolved.ClassForTool("read_file"); got != public.InteractiveToolClassFastRead {
		t.Fatalf("trimmed base class = %s, want fast/read", got)
	}
	for _, name := range explicitNames {
		if got := resolved.ClassForTool(name); got != public.InteractiveToolClassBoundedLongRunning {
			t.Fatalf("explicit name %q class = %s, want bounded-long-running", name, got)
		}
	}
	if got := resolved.ClassForTool("page_tool"); got != public.InteractiveToolClassBoundedLongRunning {
		t.Fatalf("non-base catalog class = %s, want bounded-long-running", got)
	}
	if got := resolved.ClassForTool("new_page_tool"); got != public.InteractiveToolClassBoundedLongRunning {
		t.Fatalf("dynamic class = %s, want bounded-long-running", got)
	}
	if got := resolved.ClassForTool(" read_file "); got != public.InteractiveToolClassBoundedLongRunning {
		// Exact lookup is intentionally preserved. This name was not
		// advertised after constructor trimming, so dynamic fallback applies.
		t.Fatalf("whitespace lookup class = %s, want dynamic bounded-long-running", got)
	}

	static, err := New().Resolve(public.InteractiveToolPolicyRequest{
		Definitions:     []messages.ToolDefinition{{Name: "read_file"}},
		BaseDefinitions: []messages.ToolDefinition{{Name: "read_file"}},
	})
	if err != nil {
		t.Fatalf("Resolve static catalog: %v", err)
	}
	if got := static.ClassForTool("unknown"); got != public.InteractiveToolClassFastRead {
		t.Fatalf("static unknown class = %s, want fast/read", got)
	}
}

func TestSnapshotCloneAndInputIsolation(t *testing.T) {
	definitions := []messages.ToolDefinition{{Name: "read_file"}, {Name: "exec"}}
	resolved, err := New().Resolve(public.InteractiveToolPolicyRequest{Definitions: definitions})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	definitions[0].Name = "exec"
	if got := resolved.ClassForTool("read_file"); got != public.InteractiveToolClassFastRead {
		t.Fatalf("resolved policy changed after input mutation: %s", got)
	}

	clonePolicy := resolved.Clone()
	clone, ok := clonePolicy.(*snapshot)
	if !ok {
		t.Fatalf("Clone() returned %T, want *snapshot", clonePolicy)
	}
	clone.classes["read_file"] = public.InteractiveToolClassBoundedLongRunning
	if got := resolved.ClassForTool("read_file"); got != public.InteractiveToolClassFastRead {
		t.Fatalf("original changed after clone mutation: %s", got)
	}
	if clone.Settings() != resolved.Settings() {
		t.Fatal("clone settings differ from original")
	}
}

func TestSnapshotConcurrentReads(t *testing.T) {
	resolved, err := New().Resolve(public.InteractiveToolPolicyRequest{
		Definitions:        []messages.ToolDefinition{{Name: "read_file"}, {Name: "exec"}},
		DynamicLongRunning: true,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if resolved.ClassForTool("read_file") != public.InteractiveToolClassFastRead {
					t.Errorf("read_file was not fast/read")
				}
				if resolved.TimeoutForTool("exec") != public.DefaultInteractiveLongRunningTimeout {
					t.Errorf("exec timeout changed")
				}
				_ = resolved.Clone()
			}
		}()
	}
	wg.Wait()
}
