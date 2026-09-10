# C43 retire CLI image staging

This admitted slice moves session image staging out of the CLI adapter and
behind the public `go-agent-runtime/services/tools` contract. The private
`services/tools/internal/imagestaging` package owns permissions, extension
selection, path advertisement, refresh decoration, and idempotent cleanup;
the CLI retains only host configuration resolution and composition.

The evidence consumer exercises the public Wire/executor contract with a
literal PNG, exact `read_image` bytes and typed projection, refreshed
definitions, cleanup, and independent negative controls. The shipped-process
probe builds `yui`, a deterministic loopback WebSocket provider, and a real
Chrome native WebMCP page. It proves image staging, a second native page-tool
registration, provider `session.update` refresh, induced timeout cleanup, and
credential-free strict replay.

Run the bounded focused verifier from the repository root:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c43-retire-cli-image-staging/verify.py --mode focused
```

Exact-head implementation evidence is recorded in
`implementation-handoff.md`. The authoritative focused run is
`runs/verify-20260910T200021Z-37030` at source commit
`97b1141b10b13c19ef73750694fa4acda4cc1bb2`, with status `PASS` and 18
recorded steps. Disposable run artifacts stay ignored under `runs/`; the
selected textual outcome, provenance, protocol, and per-case records for this
run are archived there with the handoff. No CI result is claimed by this
evidence.

That run predates the current-main merge and is retained as historical evidence.
The fresh merged-head run is
`runs/verify-20260910T211145Z-67634` at source
`169490ea69ff1886fc07ec333ac98dfa142aef05`; it passed all 18 steps, including
the built CLI process workflow and induced timeout. The prior linker
`no space left on device` observation is retained only as historical
prerequisite evidence.

The latest exact-head run is
`runs/verify-20260910T220504Z-18231` at source
`ba6fffadc76e392b6086c86128cb4ae889c2aedf`; it passed all 18 steps. The
normal/race live checks now run the existing
`TestLiveCapabilityHandleOwnsLifecycleAndBrowserEvents` test; the shipped
process case separately proves semantic catalog/navigation refresh. The image
process exited 0 with provider `PASS`, page events `initial, refreshed`, exact
70-byte PNG/readback SHA-256
`4ff6ab670a58c14270e034e2090d9a432caa263a14e0a25785386b0c12f880b5`, refreshed
`c43_refreshed_probe`, no staging leftovers and no CLI/provider/Chrome
survivors. The induced `--max-duration 2s` case and strict credential-free
replay also exited 0 with no survivors. The deliberate wrong-oracle control
exited 1 with the expected 70-versus-71 mismatch. This selected run is
force-archived with its complete logs and provenance; no CI result is claimed.
