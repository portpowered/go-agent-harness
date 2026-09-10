module github.com/portpowered/go-agent-harness/audio-runtime-c36-room-media-epoch-consumer

go 1.26.7

require (
	github.com/portpowered/go-agent-harness/go-agent-loop v0.0.3
	github.com/portpowered/go-agent-harness/go-agent-runtime v0.0.0
	github.com/portpowered/go-agent-harness/go-audio v0.0.0
)

require (
	github.com/google/wire v0.7.0 // indirect
	github.com/pion/opus v0.1.1-0.20260814200708-161621adf560 // indirect
	github.com/portpowered/go-agent-harness/go-llm-gateway v0.0.5-0.20260904005433-1a695c9c6934 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/portpowered/go-agent-harness/go-agent-loop => ../../../../../go-agent-loop

replace github.com/portpowered/go-agent-harness/go-agent-runtime => ../../../../../go-agent-runtime

replace github.com/portpowered/go-agent-harness/go-audio => ../../../../../go-audio

replace github.com/portpowered/go-agent-harness/go-device-gateway => ../../../../../go-device-gateway

replace github.com/portpowered/go-agent-harness/go-llm-gateway => ../../../../../go-llm-gateway
