# PR 455 integration rejection after evidence-only checkpoint

The latest hosted PR 455 run before the C64 merge was run 34666727240 at
head 016a3fa575bd2ae14d754c8823e7cb72c382622b, integration job
103479993028.

* URL: https://github.com/portpowered/go-agent-harness/actions/runs/34666727240/job/103479993028
* all other required checks passed; CI (integration) failed in the deterministic integration step;
* failing test: TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/slow_device;
* terminal observation: rendered_pcm=319200, nonzero_pcm=150871, expected_pcm=174391, final_marker=false;
* playback counters: queued=0, dropped=0, overflow=0, discarded=0, discard_events=0, callbacks=665, rendered=319200;
* provider evidence: responsesSent=9, responseCreates=7, inputSamplesSeen=478799, toolResults=7; the child was still running at the scenario timeout.

This is the C64 provider-audio/remote-device terminal-drain family, not a C65 AgentLoop publication-buffer path. The failure is retained as immutable negative evidence; C65 did not edit C64-owned source or tests. After C64 reviewed merge 59af6325614d80173447fe2018a0471e27b4e7b1, the same shipped high-rate control passed all 20 trials in normal, coverage, and race modes. The new merged-main candidate must still be evaluated by the script CI gate; this record does not claim hosted CI success.
