package room

import (
	"fmt"

	analysisstream "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
)

type pcm16RoomAnalysisState struct {
	config        PCM16RoomAnalysisConfig
	normalized    PCM16RoomInput
	streams       map[string]PCM16TimedStream
	analyses      map[string]PCM16Analysis
	bargeAnalyses map[string]PCM16Analysis
	result        PCM16RoomAnalysis
}

func newPCM16RoomAnalysisState(input PCM16RoomInput, config PCM16RoomAnalysisConfig) (*pcm16RoomAnalysisState, error) {
	normalizedConfig, err := normalizePCM16RoomAnalysisConfig(config)
	if err != nil {
		return nil, err
	}
	normalized, streams, err := normalizePCM16RoomInput(input)
	if err != nil {
		return nil, err
	}
	return &pcm16RoomAnalysisState{
		config:     normalizedConfig,
		normalized: normalized,
		streams:    streams,
		analyses:   make(map[string]PCM16Analysis, len(normalized.Streams)),
		result: PCM16RoomAnalysis{
			Streams:        make([]PCM16Analysis, 0, len(normalized.Streams)),
			Overlaps:       make([]PCM16OverlapAnalysis, 0, len(normalized.Overlaps)),
			PeerDeliveries: make([]PCM16PeerDeliveryMeasurement, 0, len(normalized.Overlaps)*2),
			SelfHearings:   make([]PCM16SelfHearingMeasurement, 0, len(normalized.Overlaps)*2),
			BargeIns:       make([]PCM16BargeInMeasurement, 0, len(normalized.BargeIns)),
			Loudness:       make([]PCM16LoudnessMeasurement, 0, len(normalized.Overlaps)+len(normalized.Loudness)),
			Drift:          make([]PCM16DriftMeasurement, 0, len(normalized.Streams)),
		},
	}, nil
}

func (s *pcm16RoomAnalysisState) measureStreams() error {
	for index, stream := range s.normalized.Streams {
		if err := s.measureStream(index, stream); err != nil {
			return err
		}
	}
	return nil
}

func (s *pcm16RoomAnalysisState) measureStream(index int, timedStream PCM16TimedStream) error {
	analysis, err := analysisstream.AnalyzePCM16(timedStream.PCM16Input, s.config.StreamConfig)
	if err != nil {
		return fmt.Errorf("%w: streams[%d] %w", ErrInvalidPCM16RoomAnalysisInput, index, err)
	}
	drift, err := MeasurePCM16Drift(timedStream)
	if err != nil {
		return err
	}
	drift.Bound = configuredDriftBound(drift.SampleDuration, drift.TimestampSpan, s.config)
	drift.Passed = drift.Drift <= drift.Bound
	s.result.Streams = append(s.result.Streams, analysis)
	s.analyses[timedStream.StreamID] = analysis
	s.result.Failures = append(s.result.Failures, analysis.Failures...)
	s.result.Drift = append(s.result.Drift, drift)
	if !drift.Passed {
		s.appendDriftFailure(timedStream, drift)
	}
	return nil
}

func (s *pcm16RoomAnalysisState) appendDriftFailure(timedStream PCM16TimedStream, drift PCM16DriftMeasurement) {
	failure := roomFailure("timing-drift", timedStream, "")
	failure.Interval = "stream-timeline"
	failure.StartSample = 0
	failure.EndSample = len(timedStream.Samples)
	failure.Timestamp = timedStream.TimelineStart
	failure.Measured = durationMilliseconds(drift.Drift)
	failure.Comparison = ">"
	failure.Bound = durationMilliseconds(drift.Bound)
	failure.Unit = "milliseconds"
	failure.Detail = fmt.Sprintf("sample-duration=%s timestamp-span=%s", drift.SampleDuration, drift.TimestampSpan)
	s.result.Failures = append(s.result.Failures, failure)
}

func (s *pcm16RoomAnalysisState) prepareBargeAnalyses() error {
	s.bargeAnalyses = s.analyses
	if len(s.normalized.BargeIns) == 0 {
		return nil
	}
	s.bargeAnalyses = make(map[string]PCM16Analysis, len(s.normalized.BargeIns)*2)
	bargeConfig := s.config.StreamConfig
	bargeConfig.FrameDuration = PCM16AnalysisFrameDuration
	for _, annotation := range s.normalized.BargeIns {
		if err := s.prepareBargeStream(annotation.InterrupterStreamID, bargeConfig); err != nil {
			return err
		}
		if err := s.prepareBargeStream(annotation.InterruptedStreamID, bargeConfig); err != nil {
			return err
		}
	}
	return nil
}

func (s *pcm16RoomAnalysisState) prepareBargeStream(streamID string, config PCM16AnalysisConfig) error {
	if _, measured := s.bargeAnalyses[streamID]; measured {
		return nil
	}
	timedStream := s.streams[streamID]
	analysis, err := analysisstream.AnalyzePCM16(timedStream.PCM16Input, config)
	if err != nil {
		return fmt.Errorf("%w: barge-in stream %q: %w", ErrInvalidPCM16RoomAnalysisInput, streamID, err)
	}
	s.bargeAnalyses[streamID] = analysis
	return nil
}

type pcm16RoomOverlapMeasurements struct {
	result  PCM16OverlapAnalysis
	forward PCM16PeerDeliveryMeasurement
	reverse PCM16PeerDeliveryMeasurement
	selfA   PCM16SelfHearingMeasurement
	selfB   PCM16SelfHearingMeasurement
}

func (s *pcm16RoomAnalysisState) measureOverlaps() error {
	for _, overlap := range s.normalized.Overlaps {
		measured, err := s.measureOverlap(overlap)
		if err != nil {
			return err
		}
		s.result.Overlaps = append(s.result.Overlaps, measured.result)
		s.result.PeerDeliveries = append(s.result.PeerDeliveries, measured.forward, measured.reverse)
		s.result.SelfHearings = append(s.result.SelfHearings, measured.selfA, measured.selfB)
		s.result.Loudness = append(s.result.Loudness, measured.result.Loudness)
		appendCorrelationFailures(&s.result.Failures, measured.forward, s.config.MinPeerCorrelation)
		appendCorrelationFailures(&s.result.Failures, measured.reverse, s.config.MinPeerCorrelation)
		appendSelfHearingFailures(&s.result.Failures, measured.selfA, s.config.MaxSelfCorrelation)
		appendSelfHearingFailures(&s.result.Failures, measured.selfB, s.config.MaxSelfCorrelation)
		appendLoudnessFailure(&s.result.Failures, measured.result.Loudness, s.config.MaxLoudnessDifferenceDB)
	}
	return nil
}

func (s *pcm16RoomAnalysisState) measureOverlap(overlap PCM16OverlapInterval) (pcm16RoomOverlapMeasurements, error) {
	a := s.streams[overlap.A.SentStreamID]
	b := s.streams[overlap.B.SentStreamID]
	aReceived := s.streams[overlap.A.ReceivedStreamID]
	bReceived := s.streams[overlap.B.ReceivedStreamID]
	forwardCorrelation, err := s.measureOverlapCorrelation(overlap, a, bReceived, "forward correlation")
	if err != nil {
		return pcm16RoomOverlapMeasurements{}, err
	}
	reverseCorrelation, err := s.measureOverlapCorrelation(overlap, b, aReceived, "reverse correlation")
	if err != nil {
		return pcm16RoomOverlapMeasurements{}, err
	}
	selfA, err := s.measureOverlapCorrelation(overlap, a, aReceived, fmt.Sprintf("self correlation for %q", overlap.A.ParticipantID))
	if err != nil {
		return pcm16RoomOverlapMeasurements{}, err
	}
	selfB, err := s.measureOverlapCorrelation(overlap, b, bReceived, fmt.Sprintf("self correlation for %q", overlap.B.ParticipantID))
	if err != nil {
		return pcm16RoomOverlapMeasurements{}, err
	}
	loudness, err := MeasurePCM16Loudness(a, b, overlap.PCM16TimeInterval)
	if err != nil {
		return pcm16RoomOverlapMeasurements{}, fmt.Errorf("%w: overlap %q loudness: %w", ErrInvalidPCM16RoomAnalysisInput, overlap.ID, err)
	}
	loudness.Passed = loudness.LeftSamples > 0 && loudness.RightSamples > 0 && loudness.DifferenceDB <= s.config.MaxLoudnessDifferenceDB
	forward := peerDeliveryMeasurement(forwardCorrelation, overlap.A.ParticipantID, overlap.B.ParticipantID, s.config.MinPeerCorrelation)
	reverse := peerDeliveryMeasurement(reverseCorrelation, overlap.B.ParticipantID, overlap.A.ParticipantID, s.config.MinPeerCorrelation)
	selfAMeasurement := selfHearingMeasurement(selfA, overlap.A.ParticipantID, s.config.MaxSelfCorrelation)
	selfBMeasurement := selfHearingMeasurement(selfB, overlap.B.ParticipantID, s.config.MaxSelfCorrelation)
	return pcm16RoomOverlapMeasurements{
		result:  PCM16OverlapAnalysis{Interval: overlap, Forward: forward, Reverse: reverse, SelfA: selfAMeasurement, SelfB: selfBMeasurement, Loudness: loudness},
		forward: forward,
		reverse: reverse,
		selfA:   selfAMeasurement,
		selfB:   selfBMeasurement,
	}, nil
}

func (s *pcm16RoomAnalysisState) measureOverlapCorrelation(overlap PCM16OverlapInterval, source, received PCM16TimedStream, label string) (PCM16CorrelationMeasurement, error) {
	measurement, err := NormalizedPCM16CrossCorrelation(source, received, overlap.PCM16TimeInterval, s.config.CorrelationLagWindow, s.config.CorrelationSilenceFloorDBFS)
	if err != nil {
		return PCM16CorrelationMeasurement{}, fmt.Errorf("%w: overlap %q %s: %w", ErrInvalidPCM16RoomAnalysisInput, overlap.ID, label, err)
	}
	return measurement, nil
}

func peerDeliveryMeasurement(correlation PCM16CorrelationMeasurement, sourceParticipant, receivedParticipant string, bound float64) PCM16PeerDeliveryMeasurement {
	return PCM16PeerDeliveryMeasurement{
		PCM16CorrelationMeasurement: correlation,
		Direction:                   fmt.Sprintf("%s->%s", sourceParticipant, receivedParticipant),
		Passed:                      correlation.ComparedSamples > 0 && correlation.BestCorrelation >= bound,
	}
}

func selfHearingMeasurement(correlation PCM16CorrelationMeasurement, participant string, bound float64) PCM16SelfHearingMeasurement {
	return PCM16SelfHearingMeasurement{
		PCM16CorrelationMeasurement: correlation,
		Direction:                   fmt.Sprintf("%s->%s", participant, participant),
		Passed:                      correlation.BestAbsoluteCorrelation < bound,
	}
}

func (s *pcm16RoomAnalysisState) measureLoudness() error {
	for _, interval := range s.normalized.Loudness {
		left := s.streams[interval.LeftStreamID]
		right := s.streams[interval.RightStreamID]
		loudness, err := MeasurePCM16Loudness(left, right, interval.PCM16TimeInterval)
		if err != nil {
			return fmt.Errorf("%w: loudness interval %q: %w", ErrInvalidPCM16RoomAnalysisInput, interval.ID, err)
		}
		loudness.Passed = loudness.LeftSamples > 0 && loudness.RightSamples > 0 && loudness.DifferenceDB <= s.config.MaxLoudnessDifferenceDB
		s.result.Loudness = append(s.result.Loudness, loudness)
		appendLoudnessFailure(&s.result.Failures, loudness, s.config.MaxLoudnessDifferenceDB)
	}
	return nil
}

func (s *pcm16RoomAnalysisState) measureBargeIns() error {
	for _, annotation := range s.normalized.BargeIns {
		interrupter := s.streams[annotation.InterrupterStreamID]
		interrupted := s.streams[annotation.InterruptedStreamID]
		measurement := measureBargeInFromAnalyses(interrupter, interrupted, annotation, s.bargeAnalyses[interrupter.StreamID], s.bargeAnalyses[interrupted.StreamID], s.config.BargeInSpeechThresholdDBFS, s.config.MaxBargeInLatency)
		s.result.BargeIns = append(s.result.BargeIns, measurement)
		appendBargeInFailures(&s.result.Failures, measurement, interrupter, interrupted, s.config.BargeInSpeechThresholdDBFS, s.config.MaxBargeInLatency)
	}
	return nil
}
