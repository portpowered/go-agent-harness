package stream

import (
	"fmt"
	"math"
	"time"
)

func makePCM16Frame(samples []int16, start, end, index, sampleRate, frameSamples, clipThreshold int) PCM16Frame {
	frame := PCM16Frame{
		Index:       index,
		StartSample: start,
		EndSample:   end,
		SampleCount: end - start,
		Timestamp:   samplesToDuration(start, sampleRate),
		Duration:    samplesToDuration(end-start, sampleRate),
		Partial:     end-start < frameSamples,
		RMS:         PCM16RMSEnergy(samples),
	}
	frame.RMSDBFS = dbfs(frame.RMS)
	frame.AbsolutePeak = absolutePeak(samples)
	frame.PeakDBFS = dbfs(float64(frame.AbsolutePeak))
	frame.HeadroomDBFS = headroomDBFS(frame.AbsolutePeak)
	for offset, sample := range samples {
		absValue := absoluteSample(sample)
		if absValue < clipThreshold {
			continue
		}
		location := PCM16SampleLocation{
			SampleIndex: start + offset,
			FrameIndex:  index,
			Timestamp:   samplesToDuration(start+offset, sampleRate),
			Value:       sample,
			AbsValue:    absValue,
		}
		frame.ClippedSamples = append(frame.ClippedSamples, location)
	}
	frame.ClipCount = len(frame.ClippedSamples)
	return frame
}

func analyzeSilentRuns(samples []int16, frames []PCM16Frame, annotations []normalizedSpeechAnnotation, sampleRate int, config PCM16AnalysisConfig, streamID, participantID string, failures *[]PropertyFailure) []SilentRun {
	runs := make([]SilentRun, 0)
	for frameIndex := 0; frameIndex < len(frames); {
		if frames[frameIndex].RMSDBFS > config.SilenceFloorDBFS {
			frameIndex++
			continue
		}
		startFrame := frameIndex
		for frameIndex+1 < len(frames) && frames[frameIndex+1].RMSDBFS <= config.SilenceFloorDBFS {
			frameIndex++
		}
		startSample := frames[startFrame].StartSample
		endSample := frames[frameIndex].EndSample
		overlapSamples, label := expectedSpeechOverlap(startSample, endSample, annotations)
		run := SilentRun{
			StartSample:           startSample,
			EndSample:             endSample,
			Start:                 samplesToDuration(startSample, sampleRate),
			End:                   samplesToDuration(endSample, sampleRate),
			Duration:              samplesToDuration(endSample-startSample, sampleRate),
			RMS:                   PCM16RMSEnergy(samples[startSample:endSample]),
			ExpectedSpeechOverlap: samplesToDuration(overlapSamples, sampleRate),
			InExpectedSpeech:      overlapSamples > 0,
			NaturalPause:          samplesToDuration(overlapSamples, sampleRate) <= config.MaxNaturalPause,
			Dropout:               samplesToDuration(overlapSamples, sampleRate) > config.MaxNaturalPause,
		}
		run.RMSDBFS = dbfs(run.RMS)
		runs = append(runs, run)
		if run.Dropout {
			failure := analysisFailure("dropout", streamID, participantID)
			failure.Interval = label
			failure.StartSample = startSample
			failure.EndSample = endSample
			failure.SampleIndex = startSample
			failure.FrameIndex = startFrame
			failure.Timestamp = run.Start
			failure.Measured = float64(run.ExpectedSpeechOverlap) / float64(time.Millisecond)
			failure.Comparison = ">"
			failure.Bound = float64(config.MaxNaturalPause) / float64(time.Millisecond)
			failure.Unit = "milliseconds of expected-speech silence"
			failure.Detail = fmt.Sprintf("silent run is %.2f ms overall at %.2f dBFS; expected-speech overlap is %.2f ms", float64(run.Duration)/float64(time.Millisecond), run.RMSDBFS, float64(run.ExpectedSpeechOverlap)/float64(time.Millisecond))
			*failures = append(*failures, failure)
		}
		frameIndex++
	}
	return runs
}

func makeBoundaryCheck(samples []int16, boundary ChunkBoundary, sampleRate, frameSamples int, config PCM16AnalysisConfig) BoundaryCheck {
	previousWindow := samples[boundary.SampleIndex-frameSamples : boundary.SampleIndex]
	nextWindow := samples[boundary.SampleIndex : boundary.SampleIndex+frameSamples]
	previous := samples[boundary.SampleIndex-1]
	next := samples[boundary.SampleIndex]
	delta := absoluteSampleDifference(previous, next)
	previousRMSDBFS := dbfs(PCM16RMSEnergy(previousWindow))
	nextRMSDBFS := dbfs(PCM16RMSEnergy(nextWindow))
	quiet := previousRMSDBFS < config.BoundaryQuietDBFS && nextRMSDBFS < config.BoundaryQuietDBFS
	largeJump := delta > config.BoundaryDelta
	return BoundaryCheck{
		ID:                    boundary.ID,
		SampleIndex:           boundary.SampleIndex,
		Timestamp:             samplesToDuration(boundary.SampleIndex, sampleRate),
		PreviousSample:        previous,
		NextSample:            next,
		Delta:                 delta,
		PreviousWindowRMSDBFS: previousRMSDBFS,
		NextWindowRMSDBFS:     nextRMSDBFS,
		SuspiciousClick:       largeJump && quiet,
		ImpulseCandidate:      largeJump && !quiet,
	}
}

func makeEdgeMeasurement(samples []int16, frameSamples int) EdgeMeasurement {
	finalStart := len(samples) - frameSamples
	if finalStart < 0 {
		finalStart = 0
	}
	finalEnd := len(samples)
	finalSamples := samples[finalStart:finalEnd]
	return EdgeMeasurement{
		FirstSample:      samples[0],
		FirstAbsValue:    absoluteSample(samples[0]),
		LastSample:       samples[len(samples)-1],
		LastAbsValue:     absoluteSample(samples[len(samples)-1]),
		FinalStartSample: finalStart,
		FinalEndSample:   finalEnd,
		FinalRMS:         PCM16RMSEnergy(finalSamples),
		FinalRMSDBFS:     dbfs(PCM16RMSEnergy(finalSamples)),
	}
}

// PCM16RMSEnergy returns the root-mean-square energy of signed PCM16 samples
// in the original integer amplitude units. Empty input has zero energy.
func PCM16RMSEnergy(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range samples {
		value := float64(sample)
		sum += value * value
	}
	return math.Sqrt(sum / float64(len(samples)))
}
