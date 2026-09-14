module github.com/portpowered/go-agent-harness/audio-runtime-c72-session-update-consumer

go 1.26.7

require (
	github.com/portpowered/go-agent-harness/go-agent-loop v0.0.3
	github.com/portpowered/go-agent-harness/go-agent-runtime v0.0.0
)

require github.com/google/wire v0.7.0 // indirect

replace github.com/portpowered/go-agent-harness/go-agent-loop => ../../../../../../go-agent-loop

replace github.com/portpowered/go-agent-harness/go-agent-runtime => ../../../../../../go-agent-runtime
