module audio-runtime-c116-rtc-transport-consumer

go 1.26.7

require (
	github.com/pion/rtp v1.10.5
	github.com/portpowered/go-agent-harness/go-agent-runtime v0.0.0
)

require (
	github.com/google/wire v0.7.0 // indirect
	github.com/pion/opus v0.1.1-0.20260814200708-161621adf560 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/portpowered/go-agent-harness/go-audio v0.0.0 // indirect
)

replace github.com/portpowered/go-agent-harness/go-agent-runtime => ../../../../../../go-agent-runtime

replace github.com/portpowered/go-agent-harness/go-audio => ../../../../../../go-audio
