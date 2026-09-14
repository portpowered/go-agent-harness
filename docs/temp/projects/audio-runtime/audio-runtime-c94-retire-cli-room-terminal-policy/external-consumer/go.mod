module example.com/audio-runtime-c94-room-terminal-consumer

go 1.26.7

require (
	github.com/portpowered/go-agent-harness/go-agent-loop v0.0.3
	github.com/portpowered/go-agent-harness/go-agent-runtime v0.0.0
	github.com/portpowered/go-agent-harness/go-llm-gateway v0.0.5-0.20260904005433-1a695c9c6934
)

replace github.com/portpowered/go-agent-harness/go-agent-loop => ../../../../../../go-agent-loop
replace github.com/portpowered/go-agent-harness/go-agent-runtime => ../../../../../../go-agent-runtime
replace github.com/portpowered/go-agent-harness/go-llm-gateway => ../../../../../../go-llm-gateway
