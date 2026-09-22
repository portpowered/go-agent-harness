package livehost

import (
	"context"
	"errors"
	"strings"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionTrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

type liveRunAdmission struct {
	runner           runtimeSession.LiveRunner
	liveRequest      runtimeSession.LiveRequest
	credentials      []string
	credentialsReady bool
	traceRun         runtimeSessionTrace.Prepared
}

func prepareLiveRun(ctx context.Context, request serviceSession.Request, deps Dependencies) (liveRunAdmission, error) {
	traceRequested := request.TraceAudio || strings.TrimSpace(request.RecordDirectory) != ""
	if traceRequested && deps.TraceService == nil {
		return liveRunAdmission{}, errors.New("live session trace service is unavailable")
	}
	runner, err := liveRunner(deps.LiveService)
	if err != nil {
		return liveRunAdmission{}, err
	}
	if deps.BuildRequest == nil {
		return liveRunAdmission{}, errors.New("live request builder is unavailable")
	}
	liveRequest, err := deps.BuildRequest(ctx, request, deps.ReplayInspection)
	if err != nil {
		return liveRunAdmission{}, err
	}
	credentials, credentialsReady, err := resolveTraceCredentials(request, &liveRequest, deps, traceRequested)
	if err != nil {
		return liveRunAdmission{}, err
	}
	traceRun, err := prepareLiveTrace(request, liveRequest, deps, credentials)
	if err != nil {
		return liveRunAdmission{}, err
	}
	return liveRunAdmission{runner: runner, liveRequest: liveRequest, credentials: credentials, credentialsReady: credentialsReady, traceRun: traceRun}, nil
}
