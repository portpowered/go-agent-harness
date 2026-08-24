package testing

import (
	"context"
	"fmt"
	"strings"
)

// SessionProbeObservation records one observed frame/tick during a replay
// probe pass, derived deterministically from the fixture record stream.
type SessionProbeObservation struct {
	Sequence  int
	Direction SessionEventDirection
	Type      string
}

// SessionReplayProbeReport summarizes one full probe pass over a recorded
// session fixture. It contains no wall-clock timestamps so repeated runs are
// byte-for-byte identical.
type SessionReplayProbeReport struct {
	Fixture            string
	Provider           string
	Model              string
	Provenance         string
	InboundFrames      int
	OutboundTicks      int
	Observations       []SessionProbeObservation
	EndsWithDisconnect bool
}

// SessionReplayProbeFunc drives one probe pass against a named session fixture.
type SessionReplayProbeFunc func(ctx context.Context, fixturePath string) (SessionReplayProbeReport, error)

// RunSessionReplayProbe validates the fixture through the shared fixture
// validator, replays it over the record/replay transport contract, and returns
// deterministic frame/tick observations without touching the network.
func RunSessionReplayProbe(ctx context.Context, fixturePath string) (SessionReplayProbeReport, error) {
	if validationErrs := ValidateSessionCaptureFile(fixturePath); len(validationErrs) > 0 {
		messages := make([]string, 0, len(validationErrs))
		for _, validationErr := range validationErrs {
			messages = append(messages, validationErr.Error())
		}
		return SessionReplayProbeReport{}, fmt.Errorf("session fixture validation failed before any probe observation: %s", strings.Join(messages, "; "))
	}

	capture, err := LoadSessionCapture(fixturePath)
	if err != nil {
		return SessionReplayProbeReport{}, fmt.Errorf("load replay session fixture: %w", err)
	}
	dialer, err := NewReplayWebSocketDialer(fixturePath)
	if err != nil {
		return SessionReplayProbeReport{}, fmt.Errorf("open replay dialer: %w", err)
	}
	conn, err := dialer.Dial("", nil)
	if err != nil {
		return SessionReplayProbeReport{}, fmt.Errorf("open replay connection: %w", err)
	}

	report := SessionReplayProbeReport{
		Fixture:            fixturePath,
		Provider:           capture.Provider.Name,
		Model:              capture.Provider.Model,
		Provenance:         capture.Session.FixtureProvenance,
		EndsWithDisconnect: capture.EndsWithDisconnect,
		Observations:       make([]SessionProbeObservation, 0, len(capture.Records)),
	}

	fail := func(format string, args ...any) (SessionReplayProbeReport, error) {
		_ = conn.Close()
		return SessionReplayProbeReport{}, fmt.Errorf(format, args...)
	}

	for _, record := range capture.Records {
		if ctx.Err() != nil {
			return fail("replay probe canceled at sequence %d: %w", record.Sequence, ctx.Err())
		}
		switch record.Direction {
		case DirectionClientToServer:
			if writeErr := conn.WriteMessage(1, eventPayload(record)); writeErr != nil {
				return fail("replay probe outbound tick at sequence %d diverged: %w", record.Sequence, writeErr)
			}
			report.OutboundTicks++
			report.Observations = append(report.Observations, SessionProbeObservation{
				Sequence:  record.Sequence,
				Direction: record.Direction,
				Type:      record.Type,
			})
		default:
			_, payload, readErr := conn.ReadMessage()
			if readErr != nil {
				return fail("replay probe inbound frame at sequence %d failed: %w", record.Sequence, readErr)
			}
			report.InboundFrames++
			report.Observations = append(report.Observations, SessionProbeObservation{
				Sequence:  record.Sequence,
				Direction: DirectionServerToClient,
				Type:      record.Type,
			})
			_ = payload
		}
	}

	if closeErr := conn.Close(); closeErr != nil {
		return fail("close replay connection: %w", closeErr)
	}
	if replayErr := dialer.Err(); replayErr != nil {
		return SessionReplayProbeReport{}, fmt.Errorf("replay probe divergence: %w", replayErr)
	}
	select {
	case <-dialer.Done():
	default:
		return SessionReplayProbeReport{}, fmt.Errorf("replay probe did not reach session end")
	}
	return report, nil
}
