# C81 external consumer proof

This separate module imports only `roomaudio` and `roomaudio/wire`, constructs an admitted literal plan, loads literal PCM16/WAV artifacts, observes stream and delta order, and verifies a mutated delta returns the typed reconstruction error.

Run from this directory with:

```sh
GOWORK=off go test ./...
```
