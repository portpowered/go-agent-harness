# C36 room-media epoch consumer

This is an external consumer of the public room/media contracts. It injects a
deterministic `session.LiveService`, `rooms.MediaFactory`, and canonical clock
through `rooms/wire.NewService`, then exercises `rooms.Service.Run` with two
agent participants and a software listener.

The consumer proves peer-only routing, stale-epoch discard, end-of-response
ordering, terminal provenance, cancellation/close/join behavior, and the
public `audio.PlaybackQueue` admission/consumption boundary. The listener
probe intentionally does not open a physical device; its report names that
capability gap instead of claiming acoustic playback.

The module is standalone and must be built with the workspace disabled:

```sh
GOWORK=off go test ./...
GOWORK=off go test -race -run 'TestRoomMediaRoutesPeersAndDiscardsStaleEpoch|TestPlaybackBoundarySeparatesAdmissionConsumptionAndUnderflow|TestLifecycleCancellationAndRepeatedClose' -count=5
GOWORK=off go vet ./...
```

The bounded verifier records a deterministic archive and hash manifest for the
admitted consumer, every workspace module it builds, and the frozen fixtures.
It binds the selected toolchain, build flags, package paths, and binary hashes;
`--no-build` is accepted only with a matching prior build record. It also
records child-process group cleanup, causal frame observations, subprocess
mutation negatives, partial-recording rejection, and strict replay of each
generated C16/YUI bundle:

```sh
python3 verify.py --action all --source "$FACTORY_ROOT" \
  --evidence evidence/all --child-timeout 60 --aggregate-timeout 600
```

`verify.py` supports the individual gates `boundary`, `provenance`,
`routing-epochs`, `mutations`, `partial-recording`, `consumption`,
`lifecycle`, `hang-control`, and `parity`, plus `all`. The positive report
retains raw source, peer-output, playback, and terminal events so unexpected
frames cannot be hidden by summary filtering; it explicitly reports the
software-only playback boundary and physical-device capability gap.
