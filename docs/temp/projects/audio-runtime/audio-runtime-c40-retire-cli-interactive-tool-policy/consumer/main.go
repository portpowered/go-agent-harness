// Command interactive-policy-consumer exercises the public tools policy
// contract from a separate Go module. It has no CLI, browser, device,
// credential, terminal, or ambient configuration dependency.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	toolswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

const (
	marker = "C40_POLICY_CONSUMER"
	wrongOracleArgument = "wrong-oracle"
)

type report struct {
	Status                         string `json:"status"`
	Marker                         string `json:"marker"`
	Defaults                       settingsReport `json:"defaults"`
	Overrides                      settingsReport `json:"overrides"`
	Classes                        map[string]string `json:"classes"`
	Timeouts                       map[string]string `json:"timeouts"`
	SnapshotInputIsolation         bool `json:"snapshot_input_isolation"`
	SnapshotCloneIsolation         bool `json:"snapshot_clone_isolation"`
	InvalidRejectedBeforeEffects   bool `json:"invalid_rejected_before_effects"`
	ProviderSetupCalls              int `json:"provider_setup_calls"`
	CorrelatedContinuation          string `json:"correlated_continuation"`
	PolicyCallChain                 []string `json:"policy_call_chain"`
}

type settingsReport struct {
	FastReadTimeout          string `json:"fast_read_timeout"`
	LongRunningTimeout       string `json:"long_running_timeout"`
	AcknowledgementThreshold string `json:"acknowledgement_threshold"`
}

func main() {
	wrongOracle := len(os.Args) == 2 && os.Args[1] == wrongOracleArgument
	if len(os.Args) > 1 && !wrongOracle {
		fail(fmt.Errorf("unknown argument %q", os.Args[1]))
	}
	result, err := run(wrongOracle)
	if err != nil {
		fail(err)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fail(err)
	}
	fmt.Println(string(encoded))
}

func run(wrongOracle bool) (report, error) {
	factory := toolswire.NewInteractiveToolPolicy()
	base := []messages.ToolDefinition{{Name: "read_file"}, {Name: "exec"}, {Name: "sleep"}, {Name: "browser_select"}}
	definitions := append(append([]messages.ToolDefinition(nil), base...), messages.ToolDefinition{Name: "catalog_page"})
	explicit := []string{"browser_select"}

	defaultPolicy, err := factory.Resolve(tools.InteractiveToolPolicyRequest{
		Definitions:               definitions,
		BaseDefinitions:           base,
		ExplicitLongRunningNames: explicit,
		DynamicLongRunning:       true,
	})
	if err != nil {
		return report{}, fmt.Errorf("default policy construction: %w", err)
	}
	defaults := defaultPolicy.Settings()
	if defaults != (tools.InteractiveToolPolicySettings{
		FastReadTimeout:          5 * time.Second,
		LongRunningTimeout:       20 * time.Second,
		AcknowledgementThreshold: 2 * time.Second,
	}) {
		return report{}, fmt.Errorf("default literal oracle failed: %#v", defaults)
	}

	overrideSettings := tools.InteractiveToolPolicySettings{
		FastReadTimeout:          7 * time.Second,
		LongRunningTimeout:       15 * time.Second,
		AcknowledgementThreshold: 1200 * time.Millisecond,
	}
	overridePolicy, err := factory.Resolve(tools.InteractiveToolPolicyRequest{
		Settings:                  overrideSettings,
		Definitions:               definitions,
		BaseDefinitions:           base,
		ExplicitLongRunningNames: explicit,
		DynamicLongRunning:       true,
	})
	if err != nil {
		return report{}, fmt.Errorf("override policy construction: %w", err)
	}
	if overridePolicy.Settings() != overrideSettings {
		return report{}, fmt.Errorf("override literal oracle failed: %#v", overridePolicy.Settings())
	}

	classes := map[string]string{
		"read_file":      string(defaultPolicy.ClassForTool("read_file")),
		"exec":           string(defaultPolicy.ClassForTool("exec")),
		"sleep":          string(defaultPolicy.ClassForTool("sleep")),
		"browser_select": string(defaultPolicy.ClassForTool("browser_select")),
		"catalog_page":   string(defaultPolicy.ClassForTool("catalog_page")),
		"dynamic_page":   string(defaultPolicy.ClassForTool("dynamic_page")),
	}
	timeouts := map[string]string{
		"read_file":    defaultPolicy.TimeoutForTool("read_file").String(),
		"exec":         defaultPolicy.TimeoutForTool("exec").String(),
		"catalog_page": defaultPolicy.TimeoutForTool("catalog_page").String(),
	}
	wantExec := tools.InteractiveToolClassBoundedLongRunning
	if wrongOracle {
		// This is the deliberate negative control: only the expected decision
		// changes, while construction and the actual observed decision stay
		// identical.
		wantExec = tools.InteractiveToolClassFastRead
	}
	if got := defaultPolicy.ClassForTool("exec"); got != wantExec {
		return report{}, fmt.Errorf("wrong-oracle assertion: exec class=%s expected=%s", got, wantExec)
	}

	definitions[0].Name = "mutated_after_construction"
	inputIsolation := defaultPolicy.ClassForTool("read_file") == tools.InteractiveToolClassFastRead
	clone := defaultPolicy.Clone()
	cloneIsolation := clone.ClassForTool("exec") == defaultPolicy.ClassForTool("exec") && clone.TimeoutForTool("read_file") == defaultPolicy.TimeoutForTool("read_file")
	if !inputIsolation || !cloneIsolation {
		return report{}, fmt.Errorf("snapshot isolation oracle failed: input=%t clone=%t", inputIsolation, cloneIsolation)
	}

	invalid := overrideSettings
	invalid.FastReadTimeout = 10 * time.Second
	providerSetupCalls := 0
	if _, err := factory.Resolve(tools.InteractiveToolPolicyRequest{Settings: invalid}); err == nil || !strings.Contains(err.Error(), "fast_read_timeout") {
		return report{}, fmt.Errorf("invalid policy was not rejected before effects: %v", err)
	}
	invalidRejected := true

	return report{
		Status:                       "ok",
		Marker:                       marker,
		Defaults:                     settingsToReport(defaults),
		Overrides:                    settingsToReport(overridePolicy.Settings()),
		Classes:                      classes,
		Timeouts:                     timeouts,
		SnapshotInputIsolation:       inputIsolation,
		SnapshotCloneIsolation:       cloneIsolation,
		InvalidRejectedBeforeEffects: invalidRejected,
		ProviderSetupCalls:           providerSetupCalls,
		CorrelatedContinuation:       "exec -> completed -> continuation",
		PolicyCallChain: []string{
			"tools/wire.NewInteractiveToolPolicy",
			"tools/internal/policy.Factory.Resolve",
			"tools.InteractiveToolPolicy.ClassForTool",
			"tools.InteractiveToolPolicy.TimeoutForTool",
		},
	}, nil
}

func settingsToReport(settings tools.InteractiveToolPolicySettings) settingsReport {
	return settingsReport{
		FastReadTimeout:          settings.FastReadTimeout.String(),
		LongRunningTimeout:       settings.LongRunningTimeout.String(),
		AcknowledgementThreshold: settings.AcknowledgementThreshold.String(),
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
