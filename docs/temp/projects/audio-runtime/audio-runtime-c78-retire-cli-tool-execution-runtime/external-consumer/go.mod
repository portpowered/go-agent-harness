module example.com/audio-runtime-c78-consumer

go 1.26.7

require (
	github.com/portpowered/go-agent-harness/go-agent-loop v0.0.3
	github.com/portpowered/go-agent-harness/go-agent-runtime v0.0.0
)

require (
	github.com/google/wire v0.7.0 // indirect
	github.com/kr/pretty v0.2.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pion/opus v0.1.1-0.20260814200708-161621adf560 // indirect
	github.com/portpowered/go-agent-harness/go-audio v0.0.0 // indirect
	golang.org/x/image v0.36.0 // indirect
	gopkg.in/check.v1 v1.0.0-20190902080502-41f04d3bba15 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/portpowered/go-agent-harness/go-audio => ../../../../../../go-audio

replace github.com/portpowered/go-agent-harness/go-agent-loop => ../../../../../../go-agent-loop

replace github.com/portpowered/go-agent-harness/go-agent-runtime => ../../../../../../go-agent-runtime

replace github.com/portpowered/go-agent-harness/go-device-gateway => ../../../../../../go-device-gateway

replace github.com/portpowered/go-agent-harness/go-llm-gateway => ../../../../../../go-llm-gateway
