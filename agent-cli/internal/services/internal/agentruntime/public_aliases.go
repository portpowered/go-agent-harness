package agentruntime

import public "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

type SessionCancellationIntent = public.SessionCancellationIntent
type SessionDiagnosticRecord = public.SessionDiagnosticRecord
type SessionDiagnosticSink = public.SessionDiagnosticSink
type SessionDiagnosticFunc = public.SessionDiagnosticFunc
type SessionStreamObserver = public.SessionStreamObserver
type SessionToolDiagnostic = public.SessionToolDiagnostic
type SessionToolDiagnosticSink = public.SessionToolDiagnosticSink
type SessionToolDiagnosticFunc = public.SessionToolDiagnosticFunc

func NewSessionCancellationIntent() *SessionCancellationIntent {
	return public.NewSessionCancellationIntent()
}

const SessionUserCancelledClassification = "user_cancelled"
