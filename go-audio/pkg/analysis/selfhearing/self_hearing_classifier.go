package selfhearing

import (
	"math"
	"sort"
	"time"
)

func (d *PCM16SelfHearingDetector) classifyLocked() PCM16SelfHearingObservation {
	window, initial, ready := d.classificationWindow()
	if !ready {
		return initial
	}
	summary := d.measureClassification(window)
	return summary.observation(window.rate, d.config.CorrelationThreshold)
}

type pcm16SelfHearingWindow struct {
	rate           int
	minimumSamples int
	minLagSamples  int
	maxLagSamples  int
	threshold      float64
	base           time.Duration
}

type pcm16SelfHearingLagResult struct {
	coefficient float64
	compared    int
	evidence    int
	start       time.Duration
	end         time.Duration
}

type pcm16SelfHearingSummary struct {
	measurement    PCM16CorrelationMeasurement
	minimumSamples int
	bestEvidence   int
	anyEvidence    int
	foundSigned    bool
	foundAbsolute  bool
}

func (d *PCM16SelfHearingDetector) classificationWindow() (pcm16SelfHearingWindow, PCM16SelfHearingObservation, bool) {
	initial := PCM16SelfHearingObservation{Classification: PCM16SelfHearingNoEvidence}
	if len(d.playback.samples) == 0 || len(d.capture.samples) == 0 {
		return pcm16SelfHearingWindow{}, initial, false
	}
	if d.playback.rate != d.capture.rate {
		initial.Classification = PCM16SelfHearingRateMismatch
		return pcm16SelfHearingWindow{}, initial, false
	}
	rate := d.playback.rate
	minimumSamples, err := ceilDurationSamples(d.config.MinimumEvidence, rate)
	if err != nil {
		return pcm16SelfHearingWindow{}, insufficientPCM16SelfHearingObservation(), false
	}
	overlapLagMin := d.capture.start - d.playback.end
	overlapLagMax := d.capture.end - d.playback.start
	if d.config.CorrelationLagWindow.Max <= overlapLagMin || d.config.CorrelationLagWindow.Min >= overlapLagMax {
		return pcm16SelfHearingWindow{}, initial, false
	}
	if d.playback.end-d.playback.start < d.config.MinimumEvidence || d.capture.end-d.capture.start < d.config.MinimumEvidence {
		return pcm16SelfHearingWindow{}, d.insufficientActiveObservation(rate), false
	}
	minLagDuration, maxLagDuration := d.alignLagWindow()
	if maxLagDuration < minLagDuration {
		return pcm16SelfHearingWindow{}, d.insufficientActiveObservation(rate), false
	}
	minLagSamples, err := signedDurationToSamples(minLagDuration, rate)
	if err != nil {
		return pcm16SelfHearingWindow{}, insufficientPCM16SelfHearingObservation(), false
	}
	maxLagSamples, err := signedDurationToSamples(maxLagDuration, rate)
	if err != nil || maxLagSamples < minLagSamples {
		return pcm16SelfHearingWindow{}, insufficientPCM16SelfHearingObservation(), false
	}
	base := d.playback.start
	if d.capture.start < base {
		base = d.capture.start
	}
	return pcm16SelfHearingWindow{
		rate:           rate,
		minimumSamples: minimumSamples,
		minLagSamples:  minLagSamples,
		maxLagSamples:  maxLagSamples,
		threshold:      pcm16AmplitudeForDBFS(d.config.SilenceFloorDBFS),
		base:           base,
	}, PCM16SelfHearingObservation{}, true
}

func insufficientPCM16SelfHearingObservation() PCM16SelfHearingObservation {
	return PCM16SelfHearingObservation{Classification: PCM16SelfHearingInsufficientEvidence}
}

func (d *PCM16SelfHearingDetector) insufficientActiveObservation(rate int) PCM16SelfHearingObservation {
	result := PCM16SelfHearingObservation{Classification: PCM16SelfHearingNoEvidence}
	threshold := pcm16AmplitudeForDBFS(d.config.SilenceFloorDBFS)
	playbackActive := countActivePCM16Samples(d.playback.samples, threshold)
	captureActive := countActivePCM16Samples(d.capture.samples, threshold)
	if evidence := min(playbackActive, captureActive); evidence > 0 {
		result.Classification = PCM16SelfHearingInsufficientEvidence
		result.EvidenceSamples = evidence
		result.EvidenceDuration = samplesToDuration(evidence, rate)
	}
	return result
}

func countActivePCM16Samples(samples []int16, threshold float64) int {
	active := 0
	for _, sample := range samples {
		if float64(absoluteSample(sample)) > threshold {
			active++
		}
	}
	return active
}

func (d *PCM16SelfHearingDetector) alignLagWindow() (time.Duration, time.Duration) {
	minLag := d.config.CorrelationLagWindow.Min
	if eligible := d.capture.start + d.config.MinimumEvidence - d.playback.end; eligible > minLag {
		minLag = eligible
	}
	maxLag := d.config.CorrelationLagWindow.Max
	if eligible := d.capture.end - d.config.MinimumEvidence - d.playback.start; eligible < maxLag {
		maxLag = eligible
	}
	return minLag, maxLag
}

func (d *PCM16SelfHearingDetector) measureClassification(window pcm16SelfHearingWindow) pcm16SelfHearingSummary {
	summary := pcm16SelfHearingSummary{
		minimumSamples: window.minimumSamples,
		measurement: PCM16CorrelationMeasurement{
			SourceStreamID:        "assistant-playback",
			SourceParticipantID:   "assistant",
			ReceivedStreamID:      "microphone-capture",
			ReceivedParticipantID: "local-microphone",
			IntervalID:            "local-self-hearing-window",
		},
	}
	results := make(map[int]pcm16SelfHearingLagResult, (window.maxLagSamples-window.minLagSamples)/streamingPCM16LagStride+1)
	measure := func(lagSamples int) (float64, int) {
		result := d.measureClassificationLag(window, results, lagSamples)
		return result.coefficient, result.compared
	}
	visit := func(lagSamples int, coefficient float64, compared int) {
		result := d.measureClassificationLag(window, results, lagSamples)
		summary.observe(window, lagSamples, coefficient, compared, result)
	}
	forEachStreamingPCM16CorrelationCandidate(window.minLagSamples, window.maxLagSamples, visit, measure)
	return summary
}

func (d *PCM16SelfHearingDetector) measureClassificationLag(window pcm16SelfHearingWindow, results map[int]pcm16SelfHearingLagResult, lagSamples int) pcm16SelfHearingLagResult {
	if result, ok := results[lagSamples]; ok {
		return result
	}
	lag := samplesToSignedDuration(lagSamples, window.rate)
	sourceStart, sourceEnd, ok := d.classificationSourceWindow(lag, d.config.MinimumEvidence, d.config.AnalysisWindow)
	if !ok {
		return pcm16SelfHearingLagResult{}
	}
	playbackStart, playbackEnd, captureStart, captureEnd, ok := d.classificationSampleRanges(window.rate, sourceStart, sourceEnd, lag)
	if !ok {
		return pcm16SelfHearingLagResult{}
	}
	playback := d.playback.samples[playbackStart:playbackEnd]
	capture := d.capture.samples[captureStart:captureEnd]
	playback, capture = equalPCM16SampleLengths(playback, capture)
	coefficient, compared := normalizedCorrelationAtLag(playback, nil, capture, 0, window.threshold)
	result := pcm16SelfHearingLagResult{
		coefficient: coefficient,
		compared:    compared,
		evidence:    pairedNonSilentSamples(playback, capture, 0, window.threshold),
		start:       sourceStart - window.base,
		end:         sourceEnd - window.base,
	}
	results[lagSamples] = result
	return result
}

func (d *PCM16SelfHearingDetector) classificationSourceWindow(lag, minimumEvidence, analysisWindow time.Duration) (time.Duration, time.Duration, bool) {
	start := d.playback.start
	if candidate := d.capture.start - lag; candidate > start {
		start = candidate
	}
	end := d.playback.end
	if candidate := d.capture.end - lag; candidate < end {
		end = candidate
	}
	if end-start < minimumEvidence {
		return 0, 0, false
	}
	if end-start > analysisWindow {
		start = end - analysisWindow
	}
	return start, end, true
}

func (d *PCM16SelfHearingDetector) classificationSampleRanges(rate int, sourceStart, sourceEnd, lag time.Duration) (int, int, int, int, bool) {
	playbackStart, playbackStartErr := signedDurationToSamples(sourceStart-d.playback.start, rate)
	playbackEnd, playbackEndErr := signedDurationToSamples(sourceEnd-d.playback.start, rate)
	captureStart, captureStartErr := signedDurationToSamples(sourceStart+lag-d.capture.start, rate)
	captureEnd, captureEndErr := signedDurationToSamples(sourceEnd+lag-d.capture.start, rate)
	if playbackStartErr != nil || playbackEndErr != nil || captureStartErr != nil || captureEndErr != nil {
		return 0, 0, 0, 0, false
	}
	if playbackStart < 0 || playbackEnd > len(d.playback.samples) || captureStart < 0 || captureEnd > len(d.capture.samples) || playbackEnd <= playbackStart || captureEnd <= captureStart {
		return 0, 0, 0, 0, false
	}
	return playbackStart, playbackEnd, captureStart, captureEnd, true
}

func equalPCM16SampleLengths(playback, capture []int16) ([]int16, []int16) {
	if len(playback) > len(capture) {
		return playback[:len(capture)], capture
	}
	if len(capture) > len(playback) {
		return playback, capture[:len(playback)]
	}
	return playback, capture
}

func (s *pcm16SelfHearingSummary) observe(window pcm16SelfHearingWindow, lagSamples int, coefficient float64, compared int, result pcm16SelfHearingLagResult) {
	if result.evidence > s.anyEvidence {
		s.anyEvidence = result.evidence
	}
	if compared == 0 || result.evidence < s.minimumSamples {
		return
	}
	if !s.foundSigned || coefficient > s.measurement.BestCorrelation {
		s.measurement.BestCorrelation = coefficient
		s.measurement.BestLag = samplesToSignedDuration(lagSamples, window.rate)
		s.measurement.ComparedSamples = compared
		s.foundSigned = true
	}
	if !s.foundAbsolute || math.Abs(coefficient) > s.measurement.BestAbsoluteCorrelation {
		s.measurement.BestAbsoluteCorrelation = math.Abs(coefficient)
		s.measurement.BestAbsoluteLag = samplesToSignedDuration(lagSamples, window.rate)
		s.measurement.Start = result.start
		s.measurement.End = result.end
		s.bestEvidence = result.evidence
		s.foundAbsolute = true
	}
}

func (s pcm16SelfHearingSummary) observation(rate int, threshold float64) PCM16SelfHearingObservation {
	if s.anyEvidence == 0 {
		return PCM16SelfHearingObservation{Classification: PCM16SelfHearingNoEvidence}
	}
	if !s.foundAbsolute {
		return PCM16SelfHearingObservation{
			Classification:   PCM16SelfHearingInsufficientEvidence,
			Measurement:      s.measurement,
			EvidenceSamples:  s.anyEvidence,
			EvidenceDuration: samplesToDuration(s.anyEvidence, rate),
		}
	}
	classification := PCM16SelfHearingNonFeedback
	if s.measurement.BestAbsoluteCorrelation >= threshold {
		classification = PCM16SelfHearingConfirmed
	}
	return PCM16SelfHearingObservation{
		Classification:   classification,
		Measurement:      s.measurement,
		EvidenceSamples:  s.bestEvidence,
		EvidenceDuration: samplesToDuration(s.bestEvidence, rate),
	}
}

// streamingPCM16CorrelationCandidate is a cheap first-pass lag estimate used
// by the synchronous device gate. The full room-analysis primitive still
// visits every sample lag; the live gate refines the strongest bounded set so
// detector work cannot starve the microphone pump while a response is being
// rendered.
type streamingPCM16CorrelationCandidate struct {
	lag         int
	coefficient float64
	compared    int
}

const (
	streamingPCM16LagStride       = 4
	streamingPCM16RefinementCount = 8
)

func forEachStreamingPCM16CorrelationCandidate(
	minLagSamples, maxLagSamples int,
	visit func(lagSamples int, coefficient float64, compared int),
	measure func(lagSamples int) (float64, int),
) {
	if visit == nil || measure == nil || maxLagSamples < minLagSamples {
		return
	}
	coarse := collectStreamingPCM16Candidates(minLagSamples, maxLagSamples, visit, measure)
	refineStreamingPCM16Candidates(minLagSamples, maxLagSamples, coarse, visit, measure)
}

func collectStreamingPCM16Candidates(
	minLagSamples, maxLagSamples int,
	visit func(lagSamples int, coefficient float64, compared int),
	measure func(lagSamples int) (float64, int),
) []streamingPCM16CorrelationCandidate {
	coarse := make([]streamingPCM16CorrelationCandidate, 0, (maxLagSamples-minLagSamples)/streamingPCM16LagStride+1)
	for lag := minLagSamples; ; lag += streamingPCM16LagStride {
		coarse = appendStreamingPCM16Candidate(coarse, lag, visit, measure)
		if lag > maxLagSamples-streamingPCM16LagStride {
			if lag != maxLagSamples {
				coarse = appendStreamingPCM16Candidate(coarse, maxLagSamples, visit, measure)
			}
			break
		}
	}
	return coarse
}

func appendStreamingPCM16Candidate(
	coarse []streamingPCM16CorrelationCandidate,
	lag int,
	visit func(lagSamples int, coefficient float64, compared int),
	measure func(lagSamples int) (float64, int),
) []streamingPCM16CorrelationCandidate {
	coefficient, compared := measure(lag)
	visit(lag, coefficient, compared)
	return append(coarse, streamingPCM16CorrelationCandidate{lag: lag, coefficient: coefficient, compared: compared})
}

func refineStreamingPCM16Candidates(
	minLagSamples, maxLagSamples int,
	coarse []streamingPCM16CorrelationCandidate,
	visit func(lagSamples int, coefficient float64, compared int),
	measure func(lagSamples int) (float64, int),
) {
	sort.Slice(coarse, func(i, j int) bool {
		left, right := math.Abs(coarse[i].coefficient), math.Abs(coarse[j].coefficient)
		if left != right {
			return left > right
		}
		return coarse[i].compared > coarse[j].compared
	})
	seen := make(map[int]struct{}, streamingPCM16RefinementCount*streamingPCM16LagStride*2)
	for index := 0; index < len(coarse) && index < streamingPCM16RefinementCount; index++ {
		center := coarse[index].lag
		start, end := center-streamingPCM16LagStride, center+streamingPCM16LagStride
		if start < minLagSamples {
			start = minLagSamples
		}
		if end > maxLagSamples {
			end = maxLagSamples
		}
		for lag := start; lag <= end; lag++ {
			if _, ok := seen[lag]; ok {
				continue
			}
			seen[lag] = struct{}{}
			coefficient, compared := measure(lag)
			visit(lag, coefficient, compared)
		}
	}
}

func pairedNonSilentSamples(source, received []int16, receivedStart int, threshold float64) int {
	paired := 0
	for sourceIndex, sourceSample := range source {
		receivedIndex := receivedStart + sourceIndex
		if receivedIndex < 0 || receivedIndex >= len(received) {
			continue
		}
		if float64(absoluteSample(sourceSample)) <= threshold || float64(absoluteSample(received[receivedIndex])) <= threshold {
			continue
		}
		paired++
	}
	return paired
}
