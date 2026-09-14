# C81 evidence harness

The public probe in `external-consumer/cmd/roomaudio-probe` is a separate
`GOWORK=off` executable. It imports only `roomaudio` and `roomaudio/wire`,
constructs a literal synthetic admitted bundle, checks decoded PCM16/WAV data,
delta order, timeline offsets, overlap/barge-in/loudness annotations, detached
views, and typed corrupted-delta rejection.

Run the bounded shipped checks from the repository root:

```sh
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c81-retire-cli-room-audio-bundle/run.py --case room-audio-bundle --child-timeout 60 --aggregate-timeout 300
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c81-retire-cli-room-audio-bundle/run.py --case corrupted-bundle --child-timeout 60 --aggregate-timeout 180
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c81-retire-cli-room-audio-bundle/run.py --case non-room-audio-tool --child-timeout 60 --aggregate-timeout 240
```

`run.py` removes credential-like variables from children, caps child output at
64 KiB and retained run data at 8 MiB, bounds each child at 60 seconds and
reaps the complete process group. It builds the candidate `yui` for the public
room entrypoint and the existing non-room audio/tool replay; the roomaudio
bundle effect is asserted by the separate public consumer because the current
`yui room --replay` scheduler consumes a different provider-capture schema.

The focused mutation and retirement checks are exposed by `verify.py` using the
mode names in `prd.json`. Reports and run logs are generated locally under
`reports/` and `runs/` and bind their results to the current candidate.
