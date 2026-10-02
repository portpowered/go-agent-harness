//go:build stress

package integration

import (
	"fmt"
	"testing"
	"time"
)

// TestAgentBinaryHighRateToolAudioRepeatedTrials replays both captured
// high-rate tool-audio topologies through fresh agent and device processes
// twenty times each, hunting rare races in the burst audio and serial tool
// chain at process edges.
func TestAgentBinaryHighRateToolAudioRepeatedTrials(t *testing.T) {
	t.Parallel()
	slots := make(chan struct{}, 2)
	for _, testCase := range remoteToolAudioContinuationCases() {
		if testCase.healthyControl {
			continue
		}
		for trial := range 20 {
			t.Run(fmt.Sprintf("%s/trial_%02d", testCase.name, trial+1), func(t *testing.T) {
				t.Parallel()
				slots <- struct{}{}
				defer func() { <-slots }()
				runRemoteToolAudioScenario(t, testCase, 0, 0, time.Millisecond, 0, 0, 0)
			})
		}
	}
}

// TestAgentBinaryToolContinuationStressMatrix runs the fresh-process
// continuation matrix beyond the representative case that
// TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio keeps on every
// pull request.
func TestAgentBinaryToolContinuationStressMatrix(t *testing.T) {
	t.Parallel()
	runAgentBinaryContinuationMatrix(t, func(testCase remoteToolAudioCase, delivery remoteToolAudioDelivery) bool {
		return !isRepresentativeRemoteToolAudioContinuation(testCase, delivery)
	})
}
