# C74 session configuration retirement

This task extracts provider-neutral realtime session admission and selection
into `go-agent-runtime/services/sessionconfig`. The public contract is backed
by a private implementation and dedicated Wire composition. The CLI keeps its
large `SessionRunOptions` compatibility type and concrete provider/dialer
construction at the edge; it delegates capture, runtime selection, model
admission, credential classification, replay timing, URL normalization, and
request cloning policy.

The standalone `external-consumer/` module is always tested with `GOWORK=off`. It
exercises default WebSocket selection, explicit WebRTC selection, recorded
replay admission without credentials, and typed invalid-model/invalid-
transport controls. Run the bounded checks with:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c74-retire-cli-session-config/verify.py --mode all
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c74-retire-cli-session-config/run.py --case all
```

The current task slice does not claim script CI, independent review, guarded
merge, vertical hardware/acoustic proof, or broad project completion.
