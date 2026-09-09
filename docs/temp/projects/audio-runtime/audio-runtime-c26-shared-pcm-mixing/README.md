# C26 shared PCM mixing evidence

This directory contains the standalone exported-API consumer, its bounded
runtime verifier, and the source characterization for the shared PCM16 sample
operation. Build and run the verifier from the isolated worktree root:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c26-shared-pcm-mixing/verify_runtime.py
```

The verifier builds the consumer and the current-source `agent-cli/cmd/yui`,
then runs the consumer, the accepted credential-free tool/audio capture with
and without `--trace-audio`, strict directory replay, the accepted interruption
capture, and strict replay of its resulting bundle. Every child has a 60-second
deadline and is launched in a new process group; fixture hashes are checked
before and after. Generated binaries and run directories are ignored locally;
the JSON report is the exact run record when the verifier is executed.

The yui checks are software/file replay and process-shutdown evidence. They do
not establish physical device consumption, acoustic output, live Realtime
behavior, or completion of the remaining audio-runtime project criteria.
