//go:build !stress

package integration

// remoteToolAudioStress keeps TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio
// to its representative test45/captured_cadence case outside stress builds.
const remoteToolAudioStress = false
