package roomaudiofixture

import "encoding/binary"

// Pseudo-speech synthesis and PCM16/WAV encoding parameters.
const (
	lcgMultiplier    = 1664525
	lcgIncrement     = 1013904223
	lcgShift         = 8
	amplitudeLevels  = 16001
	amplitudeOffset  = 8000
	edgeRampSamples  = 20
	turnSeedStride   = 7919
	pcm16Max         = 32767
	pcm16Min         = -32768
	boundaryImpulse  = 7000
	bytesPerSample   = 2
	bitsPerSample    = 16
	wavHeaderBytes   = 44
	wavRIFFOverhead  = 36
	wavFmtChunkBytes = 16
	wavPCMFormatTag  = 1
	wavChannelCount  = 1
)

func syntheticSpeech(sampleCount int, turns []turn, seed uint32) []int16 {
	samples := make([]int16, sampleCount)
	for _, currentTurn := range turns {
		state := seed + uint32(currentTurn.Start)
		for index := currentTurn.Start; index < currentTurn.End; index++ {
			state = state*lcgMultiplier + lcgIncrement
			value := int32((state>>lcgShift)%amplitudeLevels) - amplitudeOffset
			if distance := index - currentTurn.Start; distance < edgeRampSamples {
				value = value * int32(distance+1) / edgeRampSamples
			}
			if distance := currentTurn.End - index; distance <= edgeRampSamples {
				value = value * int32(distance) / edgeRampSamples
			}
			samples[index] = int16(value)
		}
		seed += turnSeedStride
	}
	return samples
}

func delayedCopy(source []int16, delay int) []int16 {
	result := make([]int16, len(source))
	copy(result[delay:], source[:len(source)-delay])
	return result
}

func shiftTurns(turns []turn, samples int) []turn {
	result := make([]turn, len(turns))
	for index, currentTurn := range turns {
		result[index] = turn{ID: currentTurn.ID, Start: currentTurn.Start + samples, End: currentTurn.End + samples}
	}
	return result
}

func mix(streams ...[]int16) []int16 {
	sampleCount := duration
	for _, stream := range streams {
		if len(stream) > sampleCount {
			sampleCount = len(stream)
		}
	}
	result := make([]int16, sampleCount)
	for index := range result {
		var value int32
		for _, stream := range streams {
			if index >= len(stream) {
				continue
			}
			value += int32(stream[index])
		}
		value = min(max(value, pcm16Min), pcm16Max)
		result[index] = int16(value)
	}
	return result
}

func setLoudBoundaryImpulse(samples []int16, boundary int) {
	if boundary <= 0 || boundary >= len(samples) {
		return
	}
	samples[boundary-1] = -boundaryImpulse
	samples[boundary] = boundaryImpulse
}

func pcmBytes(samples []int16) []byte {
	data := make([]byte, len(samples)*bytesPerSample)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(data[index*bytesPerSample:], uint16(sample))
	}
	return data
}

func wavBytes(samples []int16) []byte {
	pcm := pcmBytes(samples)
	le := binary.LittleEndian
	data := make([]byte, 0, wavHeaderBytes+len(pcm))
	data = append(data, "RIFF"...)
	data = le.AppendUint32(data, uint32(wavRIFFOverhead+len(pcm)))
	data = append(data, "WAVEfmt "...)
	data = le.AppendUint32(data, wavFmtChunkBytes)
	data = le.AppendUint16(data, wavPCMFormatTag)
	data = le.AppendUint16(data, wavChannelCount)
	data = le.AppendUint32(data, sampleRate)
	data = le.AppendUint32(data, sampleRate*bytesPerSample)
	data = le.AppendUint16(data, bytesPerSample)
	data = le.AppendUint16(data, bitsPerSample)
	data = append(data, "data"...)
	data = le.AppendUint32(data, uint32(len(pcm)))
	return append(data, pcm...)
}
