# C13 baseline public integrity comparison

This ledger is the pre-edit C13 baseline. The source fixture was never
modified; both public processes ran against private copies.

- Task: `audio-runtime-c13-recorded-pcm-integrity`
- Branch: `codex/audio-runtime-c13-recorded-pcm-integrity`
- Baseline source: `668f2d8816beaa078d058b3f0bcc59600b71a023`
- Fetched `origin/main`: `668f2d8816beaa078d058b3f0bcc59600b71a023`
- Startup integration ancestor: `8bdafc7f947a3a2c9856220abdc539437035bd21`
- Original baseline ancestor: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`
- Baseline executable: `$FACTORY_ROOT/docs/temp/probes/audio-runtime-c12-interruption-replay-vertical-probe/artifact-0`
- Baseline executable SHA256: `fd6450d7e50766f5f19bceebd1e7525f559e3b5c396269e771e3db6dfad89764`
- Source fixture: `$FACTORY_ROOT/docs/temp/probes/audio-runtime-c12-interruption-replay-vertical-probe/evidence/runs/interruption/bundle`
- Private driver run: `/private/tmp/audio-runtime-c13-baseline-driver/baseline-93461-1788893846`
- Source `manifest.json` SHA256: `0b874fb3f6c996814c49f8d15c411db32d7810ed44105871ba3aca5b8b61e39e`
- Source `provider.json` SHA256: `154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206`
- Declared `audio/out-000.pcm` SHA256: `6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`
- Driver same-size one-byte mutation SHA256: `039aade3a3a49c85ea2801b8257b3ace6b533916aa9d240c95ab8d6f7a282dc9`
- Source/mutated PCM size: `3840` bytes
- Preserved historical mutated-fixture SHA256: `bb310ce7a07c08508592c27486edc72cf1f27bcf65ca5305b281f62ea5362a29`

The bounded driver was added as
`public_integrity_probe.py` and run with:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c13-recorded-pcm-integrity/public_integrity_probe.py baseline --binary "$BASELINE_YUI" --evidence "$PRIVATE_EVIDENCE"
```

The driver copied the source into `untouched/` and `mutated/`, flipped one byte
of `audio/out-000.pcm` in the latter, verified identical `manifest.json`,
`provider.json`, and PCM size, then ran both `session replay <bundle>` and
`session --replay <bundle> --audio-out <sink>`. Each process had an external
60-second process-group deadline and retained stdout, stderr, exit code, and
command in its result JSON. The complete result is retained at
`/private/tmp/audio-runtime-c13-baseline-driver/baseline-93461-1788893846/result.json`;
the private run directory is intentionally outside the repository.

Observed pre-edit outcome:

- Untouched `session replay`: exit 0, `Replay verified`.
- Mutated `session replay`: exit 0, same `Replay verified` — false pass.
- Untouched `session --replay`: exit 0, `classification=replay_complete` and
  `output_state=complete`.
- Mutated `session --replay`: exit 0, same completion — false pass.

This establishes the causal bypass: the existing public paths verify the
provider/trace scope but do not verify the already-declared derived boundary
PCM artifact. It is not proof of device consumption, acoustic output, or the
separate `--audio-out` sink.
