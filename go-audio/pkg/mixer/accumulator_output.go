package mixer

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	accumulatorDirectoryMode os.FileMode = 0o700
	accumulatorFileMode      os.FileMode = 0o600
)

func (a *PCMAccumulator) snapshot(span time.Duration) ([]int64, error) {
	target := durationSamples(span, a.rate)
	if target < 0 {
		target = 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	boundErr := a.limitFinalizeTarget(&target)
	a.ensureSampleLengthLocked(int(target))
	values := append([]int64(nil), a.samples[:int(target)]...)
	if a.truncated && boundErr == nil {
		boundErr = accumulatorDurationBoundError()
	}
	return values, boundErr
}

func (a *PCMAccumulator) limitFinalizeTarget(target *int64) error {
	var boundErr error
	if *target > int64(a.maxSample) {
		*target = int64(a.maxSample)
		boundErr = accumulatorDurationBoundError()
	}
	if *target < int64(len(a.samples)) {
		*target = int64(len(a.samples))
		if *target > int64(a.maxSample) {
			*target = int64(a.maxSample)
			boundErr = accumulatorDurationBoundError()
		}
	}
	return boundErr
}

func writePCMAccumulatorWAV(rate int, values []int64, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), accumulatorDirectoryMode); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, accumulatorFileMode)
	if err != nil {
		return err
	}
	closeFile := func(closeErr error) error {
		if err := file.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
		if closeErr != nil {
			if removeErr := os.Remove(path); removeErr != nil {
				closeErr = errors.Join(closeErr, removeErr)
			}
		}
		return closeErr
	}
	pcm := clippedPCM16Values(values)
	header, err := wavio.PCM16Header(rate, uint64(len(pcm)*2))
	if err != nil {
		return closeFile(err)
	}
	if _, err := file.Write(header[:]); err != nil {
		return closeFile(err)
	}
	if len(pcm) > 0 {
		if _, err := file.Write(codec.EncodePCM16(pcm)); err != nil {
			return closeFile(err)
		}
	}
	if err := file.Sync(); err != nil {
		return closeFile(err)
	}
	return file.Close()
}

func clippedPCM16Values(values []int64) []int16 {
	pcm := make([]int16, len(values))
	for index, value := range values {
		if value > pcm16MaxSample {
			value = pcm16MaxSample
		} else if value < pcm16MinSample {
			value = pcm16MinSample
		}
		pcm[index] = int16(value)
	}
	return pcm
}
