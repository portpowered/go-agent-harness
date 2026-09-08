package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
)

type familyBFunctionCall struct {
	ID       string
	ActionID string
	Name     string
	Args     string
}

type familyBProviderObservation struct {
	ConnectionCount          int
	SessionUpdates           int
	FunctionCalls            []familyBFunctionCall
	ToolObservations         []probe.ToolObservation
	CustomerTranscript       []probe.TranscriptEvent
	ProductTranscript        []probe.TranscriptEvent
	Correction               probe.CorrectionEvidence
	ResponseTerminalStatuses []string
	ProtocolError            string
}

type familyBProviderFixture struct {
	server   *httptest.Server
	upgrader websocket.Upgrader
	scenario probe.CustomerScenario

	mu                        sync.Mutex
	startedAt                 time.Time
	connectionCount           int
	sessionUpdates            int
	functionCalls             []familyBFunctionCall
	toolObservations          []probe.ToolObservation
	customerTranscript        []probe.TranscriptEvent
	productTranscript         []probe.TranscriptEvent
	responseTerminalStatuses  []string
	protocolError             string
	utteranceIndex            int
	originalToolStarted       time.Duration
	replacementToolStarted    time.Duration
	originalOutputStarted     time.Duration
	originalOutputEnded       time.Duration
	replacementOutputStarted  time.Duration
	replacementOutputEnded    time.Duration
	cancellationSent          time.Duration
	cancellationEventRecorded bool
	cancellationResponseID    string
	correctionStarted         time.Duration
	activeResponse            string
	cancelPending             bool
	originalResultSeen        bool
	replacementResultSeen     bool
}

func newFamilyBProviderFixture(scenario probe.CustomerScenario) *familyBProviderFixture {
	fixture := &familyBProviderFixture{
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		scenario: scenario,
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	return fixture
}

func (f *familyBProviderFixture) SetStartedAt(startedAt time.Time) {
	f.mu.Lock()
	f.startedAt = startedAt
	f.mu.Unlock()
}

func (f *familyBProviderFixture) WebSocketURL() string {
	return strings.Replace(f.server.URL, "http://", "ws://", 1)
}

func (f *familyBProviderFixture) Close() {
	if f.server != nil {
		f.server.Close()
	}
}

func (f *familyBProviderFixture) Snapshot() familyBProviderObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return familyBProviderObservation{
		ConnectionCount:    f.connectionCount,
		SessionUpdates:     f.sessionUpdates,
		FunctionCalls:      append([]familyBFunctionCall(nil), f.functionCalls...),
		ToolObservations:   append([]probe.ToolObservation(nil), f.toolObservations...),
		CustomerTranscript: append([]probe.TranscriptEvent(nil), f.customerTranscript...),
		ProductTranscript:  append([]probe.TranscriptEvent(nil), f.productTranscript...),
		Correction: probe.CorrectionEvidence{
			OriginalActionID:             probe.FamilyBOriginalActionID,
			ReplacementActionID:          probe.FamilyBReplacementActionID,
			OriginalTurnID:               "turn-1",
			CorrectionTurnID:             "turn-2",
			OriginalResponseID:           "response-original-output",
			OriginalResponseStartedAt:    f.originalOutputStarted,
			CorrectionStartedAt:          f.correctionStarted,
			CancellationSentAt:           f.cancellationSent,
			OriginalResponseEndedAt:      f.originalOutputEnded,
			ReplacementResponseStartedAt: f.replacementOutputStarted,
			ReplacementResponseEndedAt:   f.replacementOutputEnded,
			CancellationEventRecorded:    f.cancellationEventRecorded,
			CancellationResponseID:       f.cancellationResponseID,
			OriginalResponseStatus:       "cancelled",
			ReplacementResponseStatus:    "completed",
		},
		ResponseTerminalStatuses: append([]string(nil), f.responseTerminalStatuses...),
		ProtocolError:            f.protocolError,
	}
}
