module example.com/audio-runtime-c96-devicebinding-consumer

go 1.26.7

require (
	github.com/portpowered/go-agent-harness/go-agent-runtime v0.0.0
	github.com/portpowered/go-agent-harness/go-device-gateway v0.0.0
)

require (
	github.com/ebitengine/purego v0.11.0 // indirect
	github.com/gen2brain/malgo v0.11.24 // indirect
	github.com/google/wire v0.7.0 // indirect
	github.com/pion/opus v0.1.1-0.20260814200708-161621adf560 // indirect
	github.com/portpowered/go-agent-harness/go-audio v0.0.0 // indirect
)

replace github.com/portpowered/go-agent-harness/go-agent-runtime => ../../../../../../go-agent-runtime

replace github.com/portpowered/go-agent-harness/go-audio => ../../../../../../go-audio

replace github.com/portpowered/go-agent-harness/go-device-gateway => ../../../../../../go-device-gateway
