module example.com/audio-runtime-c43-retire-cli-image-staging-consumer

go 1.26.7

require (
	github.com/portpowered/go-agent-harness/go-agent-loop v0.0.3
	github.com/portpowered/go-agent-harness/go-agent-runtime v0.0.0
)

require (
	github.com/google/wire v0.7.0 // indirect
	github.com/pion/opus v0.1.1-0.20260814200708-161621adf560 // indirect
	github.com/portpowered/go-agent-harness/go-audio v0.0.0 // indirect
	golang.org/x/image v0.36.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/portpowered/go-agent-harness/go-agent-loop => ../../../../../../go-agent-loop

replace github.com/portpowered/go-agent-harness/go-agent-runtime => ../../../../../../go-agent-runtime

replace github.com/portpowered/go-agent-harness/go-audio => ../../../../../../go-audio
