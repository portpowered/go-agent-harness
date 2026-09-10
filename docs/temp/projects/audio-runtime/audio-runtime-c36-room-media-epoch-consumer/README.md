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
GOWORK=off go test -race -run 'TestRoomMediaRoutesAndDiscardsStaleEpoch|TestPlaybackBoundarySeparatesAdmissionConsumptionAndUnderflow|TestLifecycleCancellationAndRepeatedClose' -count=5
GOWORK=off go vet ./...
```

The bounded verifier records a deterministic source archive, input hashes,
child-process controls, causal reports, mutation negatives, partial-recording
rejection, and the two frozen C16/YUI parity fixtures:

```sh
python3 verify.py --action all --source "$FACTORY_ROOT" \
  --evidence evidence/all --child-timeout 60 --aggregate-timeout 600
```

`verify.py` supports the individual gates `boundary`, `provenance`,
`routing-epochs`, `mutations`, `partial-recording`, `consumption`,
`lifecycle`, `hang-control`, and `parity`, plus `all`.
