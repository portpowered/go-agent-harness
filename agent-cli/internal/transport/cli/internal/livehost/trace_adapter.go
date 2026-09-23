package livehost

import (
	"context"
	"errors"
	"strings"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func liveTraceSource(scheduler clock.Scheduler) clock.Source {
	if scheduler != nil {
		return scheduler
	}
	return clock.Real{}
}

func prepareLiveTrace(request serviceSession.Request, liveRequest session.LiveRequest, deps Dependencies, credentials []string) (sessiontrace.Prepared, error) {
	if !request.TraceAudio && strings.TrimSpace(request.RecordDirectory) == "" {
		return nil, nil
	}
	prepared, err := deps.TraceService.Prepare(sessiontrace.Request{
		TraceAudio:      request.TraceAudio,
		RecordDirectory: request.RecordDirectory,
		Clock:           liveTraceSource(deps.FileDeviceService.Scheduler),
		Credentials:     append([]string(nil), credentials...),
	})
	if err != nil {
		return nil, err
	}
	if prepared == nil {
		return nil, errors.New("live session trace preparation returned nil")
	}
	return prepared, nil
}

// wrapTraceRecorder is a stateless CLI boundary delegation. The prepared
// sessiontrace service owns recorder observation and its mutable state.
func wrapTraceRecorder(prepared sessiontrace.Prepared, inner session.LiveRecorder, request session.LiveRequest) session.LiveRecorder {
	if prepared == nil {
		return inner
	}
	return prepared.WrapLiveRecorder(inner, request)
}

func finishTrace(prepared sessiontrace.Prepared, ctx context.Context, bundle string, published bool) error {
	if prepared == nil {
		return nil
	}
	return prepared.Finish(ctx, bundle, published)
}

func wrapTraceFilePorts(prepared sessiontrace.Prepared, filePorts *FilePorts) {
	if prepared == nil || filePorts == nil {
		return
	}
	if filePorts.Input != nil {
		filePorts.Input.Source = prepared.WrapAudioSource(filePorts.Input.Source, filePorts.Input.SampleRate)
	}
	for index := range filePorts.InputTurns {
		filePorts.InputTurns[index].Source = prepared.WrapAudioSource(filePorts.InputTurns[index].Source, filePorts.InputTurns[index].SampleRate)
	}
}

// wrapTraceDeviceService delegates device instrumentation to sessiontrace;
// no device policy or lifecycle state remains at the CLI boundary.
func wrapTraceDeviceService(service runtimeDevices.Service, prepared sessiontrace.Prepared) runtimeDevices.Service {
	if prepared == nil {
		return service
	}
	return prepared.WrapDeviceService(service)
}
