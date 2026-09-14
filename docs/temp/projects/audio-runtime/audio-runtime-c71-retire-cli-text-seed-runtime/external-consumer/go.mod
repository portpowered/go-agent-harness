module example.com/audio-runtime-textseed-consumer

go 1.26.7

require (
	github.com/portpowered/go-agent-harness/go-agent-loop v0.0.3
	github.com/portpowered/go-agent-harness/go-agent-runtime v0.0.0
)

replace github.com/portpowered/go-agent-harness/go-agent-runtime => ../../../../../../go-agent-runtime

replace github.com/portpowered/go-agent-harness/go-agent-loop => ../../../../../../go-agent-loop

replace github.com/portpowered/go-agent-harness/go-audio => ../../../../../../go-audio

replace github.com/portpowered/go-agent-harness/go-device-gateway => ../../../../../../go-device-gateway

replace github.com/portpowered/go-agent-harness/go-llm-gateway => ../../../../../../go-llm-gateway
