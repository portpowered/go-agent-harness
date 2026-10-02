package shell

import (
	"io"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// NewExecToolWithPolicyAndDiagnosticWriter binds the normalized shell policy
// and diagnostics to one execution surface. Config-file types stay at the
// host composition edge.
func NewExecToolWithPolicyAndDiagnosticWriter(workingDir string, restrict bool, policy public.ExecPolicy, diagnosticWriter io.Writer) *ExecTool {
	return newExecToolWithDiagnosticWriter(workingDir, restrict, policy, diagnosticWriter)
}
