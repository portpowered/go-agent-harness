package shell

import (
	"io"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func NewExecTool(workingDir string, restrict bool) *ExecTool {
	return NewExecToolWithDiagnosticWriter(workingDir, restrict, io.Discard)
}

// NewExecToolWithDiagnosticWriter binds constructor diagnostics to a host
// supplied writer. A nil writer discards diagnostics.
func NewExecToolWithDiagnosticWriter(workingDir string, restrict bool, diagnosticWriter io.Writer) *ExecTool {
	return newExecToolWithDiagnosticWriter(workingDir, restrict, public.ExecPolicy{}, diagnosticWriter)
}

// NewExecToolWithPolicyAndDiagnosticWriter binds the normalized shell policy
// and diagnostics to one execution surface. Config-file types stay at the
// host composition edge.
func NewExecToolWithPolicyAndDiagnosticWriter(workingDir string, restrict bool, policy public.ExecPolicy, diagnosticWriter io.Writer) *ExecTool {
	return newExecToolWithDiagnosticWriter(workingDir, restrict, policy, diagnosticWriter)
}
