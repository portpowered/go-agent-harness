module example.com/audio-runtime-c70-metricsreplay-consumer

go 1.26.7

require github.com/portpowered/go-agent-harness/go-agent-runtime v0.0.0

require (
	github.com/pion/opus v0.1.1-0.20260814200708-161621adf560 // indirect
	github.com/portpowered/go-agent-harness/go-audio v0.0.0 // indirect
)

replace github.com/portpowered/go-agent-harness/go-agent-runtime => ../../../../../../../go-agent-runtime

replace github.com/portpowered/go-agent-harness/go-audio => ../../../../../../../go-audio
