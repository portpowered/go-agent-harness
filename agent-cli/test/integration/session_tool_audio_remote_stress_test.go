//go:build stress

package integration

import (
	"fmt"
	"testing"
	"time"
)

func TestAgentBinaryTest45HighRateToolAudioRegression(t *testing.T) {
	t.Parallel()
	slots := make(chan struct{}, 2)
	testCase := remoteToolAudioCase{
		name:            "test45_high_rate",
		responseSamples: []int{38400, 0, 66000, 66000, 0, 0, 0, 0, 96000},
		toolResponses:   map[int]bool{0: true, 1: true, 3: true, 4: true, 5: true, 6: true, 7: true},
	}
	for trial := range 20 {
		t.Run(fmt.Sprintf("trial_%02d", trial+1), func(t *testing.T) {
			t.Parallel()
			slots <- struct{}{}
			defer func() { <-slots }()
			runRemoteToolAudioScenario(t, testCase, 0, 0, time.Millisecond, 0, 0, 0)
		})
	}
}

func TestAgentBinaryTest46HighRateToolAudioRegression(t *testing.T) {
	t.Parallel()
	slots := make(chan struct{}, 2)
	testCase := remoteToolAudioCase{
		name:            "test46_high_rate",
		responseSamples: []int{46800, 0, 48000, 55200, 0, 0, 0, 0, 111600},
		toolResponses:   map[int]bool{0: true, 1: true, 3: true, 4: true, 5: true, 6: true, 7: true},
	}
	for trial := range 20 {
		t.Run(fmt.Sprintf("trial_%02d", trial+1), func(t *testing.T) {
			t.Parallel()
			slots <- struct{}{}
			defer func() { <-slots }()
			runRemoteToolAudioScenario(t, testCase, 0, 0, time.Millisecond, 0, 0, 0)
		})
	}
}

// remoteToolAudioStress registers the full fresh-process continuation matrix
// in TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio.
const remoteToolAudioStress = true
