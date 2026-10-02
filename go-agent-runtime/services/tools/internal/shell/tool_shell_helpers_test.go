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
