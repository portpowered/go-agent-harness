package testtiming

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParse_ExtractsSortedPackageAndTestTimings(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"Action":"run","Package":"github.com/portpowered/agent-cli/internal/tools","Test":"TestFast"}`,
		`{"Action":"pass","Package":"github.com/portpowered/agent-cli/internal/tools","Test":"TestFast","Elapsed":0.01}`,
		`{"Action":"pass","Package":"github.com/portpowered/agent-cli/test/integration","Test":"TestSessionReplay","Elapsed":1.25}`,
		`{"Action":"pass","Package":"github.com/portpowered/agent-cli/internal/tools","Elapsed":0.02}`,
		`not-json`,
		`{"Action":"pass","Package":"github.com/portpowered/agent-cli/test/integration","Elapsed":1.30}`,
	}, "\n"))

	summary, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(summary.Packages) != 2 {
		t.Fatalf("packages length = %d, want 2", len(summary.Packages))
	}
	if summary.Packages[0].Package != "github.com/portpowered/agent-cli/test/integration" {
		t.Fatalf("slowest package = %q, want integration package", summary.Packages[0].Package)
	}
	if len(summary.Tests) != 2 {
		t.Fatalf("tests length = %d, want 2", len(summary.Tests))
	}
	if summary.Tests[0].Test != "TestSessionReplay" {
		t.Fatalf("slowest test = %q, want TestSessionReplay", summary.Tests[0].Test)
	}
	if summary.Tests[0].Category != "session replay or session fixture" {
		t.Fatalf("category = %q, want session replay or session fixture", summary.Tests[0].Category)
	}
}

func TestWriteReport_DistinguishesPreflightFromSuiteTiming(t *testing.T) {
	summary := Summary{
		Packages: []Entry{{
			Package:  "github.com/portpowered/agent-cli/test/integration",
			Action:   "pass",
			Elapsed:  1200 * time.Millisecond,
			Category: "integration fixture",
		}},
		Tests: []Entry{{
			Package:  "github.com/portpowered/agent-cli/test/integration",
			Test:     "TestSessionReplay",
			Action:   "pass",
			Elapsed:  900 * time.Millisecond,
			Category: "session replay or session fixture",
		}},
	}

	var out bytes.Buffer
	if err := WriteReport(&out, summary, 2*time.Second, 3*time.Second, 5, 0); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"preflight no-test build/cache duration: 2s",
		"suite wall duration: 3s",
		"dependency download, package discovery, compilation, and first-run cache warmup",
		"-count=1",
		"TestSessionReplay",
		"session replay or session fixture",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
}
