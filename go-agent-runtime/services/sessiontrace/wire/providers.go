//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire composes the session trace service as a whole.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/internal/service"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func NewCancellationIntent() sessiontrace.CancellationIntent {
	return service.NewCancellationIntent()
}

func NewUnresolvedToolResultsError(ids []string, statuses map[string]messages.SessionSendStatus) *sessiontrace.UnresolvedToolResultsError {
	return service.NewUnresolvedToolResultsError(ids, statuses)
}

func NewService() sessiontrace.Service {
	wire.Build(service.New, wire.Bind(new(sessiontrace.Service), new(*service.Service)))
	return nil
}

func NewLiveRecorder(options sessiontrace.LiveRecorderOptions) session.LiveRecorder {
	wire.Build(service.NewLiveRecorder)
	return nil
}

func NewRuntimeRecorder(observer sessiontrace.RuntimeObserver, source clock.Source) sessiontrace.RuntimeRecorder {
	wire.Build(service.NewRuntimeRecorder)
	return nil
}

func NewProviderWireDialer(inner transport.Dialer, observer sessiontrace.RuntimeObserver, source clock.Source) sessiontrace.ProviderDialer {
	wire.Build(service.NewProviderWireDialer)
	return nil
}

func NewReplayMetricsCollector(options sessiontrace.MetricsCollectorOptions) sessiontrace.MetricsCollector {
	wire.Build(service.NewReplayMetricsCollector)
	return nil
}

func NewPlaybackDiagnostics(options sessiontrace.PlaybackDiagnosticsOptions) sessiontrace.PlaybackDiagnostics {
	wire.Build(service.NewPlaybackDiagnostics)
	return nil
}
