package stream

import "fmt"

type preparedPCM16Analysis struct {
	input        PCM16Input
	config       PCM16AnalysisConfig
	streamID     string
	frameSamples int
	annotations  []normalizedSpeechAnnotation
	boundaries   []ChunkBoundary
}

func preparePCM16Analysis(input PCM16Input, config PCM16AnalysisConfig) (preparedPCM16Analysis, error) {
	normalizedConfig, err := normalizePCM16AnalysisConfig(config)
	if err != nil {
		return preparedPCM16Analysis{}, err
	}
	if input.SampleRate <= 0 {
		return preparedPCM16Analysis{}, invalidPCM16Analysis("sample_rate", "must be positive")
	}
	if len(input.Samples) == 0 {
		return preparedPCM16Analysis{}, invalidPCM16Analysis("samples", "must not be empty")
	}
	frameSamples, err := analysisFrameSamples(input.SampleRate, normalizedConfig.FrameDuration)
	if err != nil {
		return preparedPCM16Analysis{}, err
	}
	annotations, err := normalizeSpeechAnnotations(input.ExpectedSpeech, len(input.Samples), input.SampleRate)
	if err != nil {
		return preparedPCM16Analysis{}, err
	}
	boundaries, err := validateChunkBoundaries(input.ChunkBoundaries, len(input.Samples), frameSamples)
	if err != nil {
		return preparedPCM16Analysis{}, err
	}
	streamID := input.StreamID
	if streamID == "" {
		streamID = "stream"
	}
	return preparedPCM16Analysis{
		input:        input,
		config:       normalizedConfig,
		streamID:     streamID,
		frameSamples: frameSamples,
		annotations:  annotations,
		boundaries:   boundaries,
	}, nil
}

func newPCM16Analysis(prepared preparedPCM16Analysis) PCM16Analysis {
	samples := prepared.input.Samples
	result := PCM16Analysis{
		StreamID:      prepared.streamID,
		ParticipantID: prepared.input.ParticipantID,
		SampleRate:    prepared.input.SampleRate,
		FrameDuration: prepared.config.FrameDuration,
		FrameSamples:  prepared.frameSamples,
		SampleCount:   len(samples),
		Duration:      samplesToDuration(len(samples), prepared.input.SampleRate),
		RMS:           PCM16RMSEnergy(samples),
	}
	result.RMSDBFS = dbfs(result.RMS)
	result.AbsolutePeak = absolutePeak(samples)
	result.PeakDBFS = dbfs(float64(result.AbsolutePeak))
	result.HeadroomDBFS = headroomDBFS(result.AbsolutePeak)
	return result
}

func measurePCM16Frames(result *PCM16Analysis, prepared preparedPCM16Analysis) {
	samples := prepared.input.Samples
	frameCount := (len(samples) + prepared.frameSamples - 1) / prepared.frameSamples
	result.Frames = make([]PCM16Frame, 0, frameCount)
	for frameIndex, start := 0, 0; start < len(samples); frameIndex, start = frameIndex+1, start+prepared.frameSamples {
		end := start + prepared.frameSamples
		if end > len(samples) {
			end = len(samples)
		}
		frame := makePCM16Frame(samples[start:end], start, end, frameIndex, prepared.input.SampleRate, prepared.frameSamples, prepared.config.ClipSampleThreshold)
		result.ClipCount += frame.ClipCount
		result.ClippedSamples = append(result.ClippedSamples, frame.ClippedSamples...)
		result.Frames = append(result.Frames, frame)
	}
}

func appendPCM16ClipFailure(result *PCM16Analysis, prepared preparedPCM16Analysis) {
	if result.ClipCount == 0 {
		return
	}
	first := result.ClippedSamples[0]
	failure := analysisFailure("clipping", prepared.streamID, prepared.input.ParticipantID)
	failure.Interval = fmt.Sprintf("frame-%d", first.FrameIndex)
	failure.StartSample = first.SampleIndex
	failure.EndSample = first.SampleIndex + 1
	failure.SampleIndex = first.SampleIndex
	failure.FrameIndex = first.FrameIndex
	failure.Timestamp = first.Timestamp
	failure.Measured = float64(first.AbsValue)
	failure.Comparison = ">="
	failure.Bound = float64(prepared.config.ClipSampleThreshold)
	failure.Unit = "absolute PCM16 sample"
	failure.Detail = fmt.Sprintf("%d sample(s) reached |sample| >= %d", result.ClipCount, prepared.config.ClipSampleThreshold)
	result.Failures = append(result.Failures, failure)
}

func measurePCM16Silence(result *PCM16Analysis, prepared preparedPCM16Analysis) {
	result.SilentRuns = analyzeSilentRuns(prepared.input.Samples, result.Frames, prepared.annotations, prepared.input.SampleRate, prepared.config, prepared.streamID, prepared.input.ParticipantID, &result.Failures)
	result.Dropouts = make([]SilentRun, 0)
	for _, run := range result.SilentRuns {
		if run.Dropout {
			result.Dropouts = append(result.Dropouts, run)
		}
	}
}

func measurePCM16Boundaries(result *PCM16Analysis, prepared preparedPCM16Analysis) {
	result.BoundaryChecks = make([]BoundaryCheck, 0, len(prepared.boundaries))
	for boundaryIndex, boundary := range prepared.boundaries {
		check := makeBoundaryCheck(prepared.input.Samples, boundary, prepared.input.SampleRate, prepared.frameSamples, prepared.config)
		result.BoundaryChecks = append(result.BoundaryChecks, check)
		if check.SuspiciousClick {
			result.BoundaryClicks = append(result.BoundaryClicks, check)
			result.Failures = append(result.Failures, pcm16BoundaryFailure(prepared, boundaryIndex, boundary, check))
		} else if check.ImpulseCandidate {
			result.ImpulseCandidates = append(result.ImpulseCandidates, check)
		}
	}
}

func pcm16BoundaryFailure(prepared preparedPCM16Analysis, boundaryIndex int, boundary ChunkBoundary, check BoundaryCheck) PropertyFailure {
	failure := analysisFailure("quiet-boundary-click", prepared.streamID, prepared.input.ParticipantID)
	failure.Interval = boundaryLabel(boundary)
	failure.SampleIndex = boundary.SampleIndex
	failure.FrameIndex = boundary.SampleIndex / prepared.frameSamples
	failure.BoundaryID = boundary.ID
	failure.BoundaryIndex = boundaryIndex
	failure.Timestamp = check.Timestamp
	failure.Measured = float64(check.Delta)
	failure.Comparison = ">"
	failure.Bound = float64(prepared.config.BoundaryDelta)
	failure.Unit = "PCM16 sample delta"
	failure.Detail = fmt.Sprintf("neighbor windows %.2f/%.2f dBFS are both quieter than %.2f dBFS", check.PreviousWindowRMSDBFS, check.NextWindowRMSDBFS, prepared.config.BoundaryQuietDBFS)
	return failure
}

func measurePCM16Edges(result *PCM16Analysis, prepared preparedPCM16Analysis) {
	result.Edges = makeEdgeMeasurement(prepared.input.Samples, prepared.frameSamples)
	if result.Edges.FirstAbsValue > prepared.config.EdgeSampleThreshold {
		result.Failures = append(result.Failures, pcm16EdgeFailure(prepared, "leading-click", "turn-start", 0, 0, result.Edges.FirstAbsValue, "leading turn edge exceeds the clean-edge bound"))
	}
	lastSampleIndex := len(prepared.input.Samples) - 1
	if result.Edges.LastAbsValue > prepared.config.EdgeSampleThreshold {
		result.Failures = append(result.Failures, pcm16EdgeFailure(prepared, "trailing-click", "turn-end", lastSampleIndex, lastSampleIndex/prepared.frameSamples, result.Edges.LastAbsValue, "trailing turn edge exceeds the clean-edge bound"))
	}
	if result.Edges.FinalRMSDBFS > prepared.config.FinalFrameMaxRMSDBFS {
		failure := analysisFailure("probable-truncation-pop", prepared.streamID, prepared.input.ParticipantID)
		failure.Interval = "final-20ms"
		failure.StartSample = result.Edges.FinalStartSample
		failure.EndSample = result.Edges.FinalEndSample
		failure.SampleIndex = result.Edges.FinalStartSample
		failure.FrameIndex = result.Edges.FinalStartSample / prepared.frameSamples
		failure.Timestamp = samplesToDuration(result.Edges.FinalStartSample, prepared.input.SampleRate)
		failure.Measured = result.Edges.FinalRMSDBFS
		failure.Comparison = ">"
		failure.Bound = prepared.config.FinalFrameMaxRMSDBFS
		failure.Unit = "dBFS"
		failure.Detail = "final 20 ms window is loud enough to suggest a truncated turn"
		result.Failures = append(result.Failures, failure)
	}
}

func pcm16EdgeFailure(prepared preparedPCM16Analysis, property, interval string, sampleIndex, frameIndex, measured int, detail string) PropertyFailure {
	failure := analysisFailure(property, prepared.streamID, prepared.input.ParticipantID)
	failure.Interval = interval
	failure.SampleIndex = sampleIndex
	failure.FrameIndex = frameIndex
	failure.Timestamp = samplesToDuration(sampleIndex, prepared.input.SampleRate)
	failure.Measured = float64(measured)
	failure.Comparison = ">"
	failure.Bound = float64(prepared.config.EdgeSampleThreshold)
	failure.Unit = "absolute PCM16 sample"
	failure.Detail = detail
	return failure
}
