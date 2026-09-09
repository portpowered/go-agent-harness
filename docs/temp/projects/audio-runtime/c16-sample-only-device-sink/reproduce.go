package main
import("context";"fmt";audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio";d "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices")
type handle struct{calls int}
func(h *handle) Close()error{return nil}
func(h *handle) WriteSamples(_ context.Context,s []int16)error{h.calls++;fmt.Println("backend samples",len(s));return nil}
type registry struct{h *handle}
func(r registry) List()([]d.Device,error){return nil,nil}
func(r registry) Default(d.Direction)(d.Device,error){return d.Device{},nil}
func(r registry) Open(d.DeviceID)(d.OpenedDevice,error){return r.h,nil}
func main(){h:=&handle{};s,e:=d.NewDeviceSink(registry{h},"sample-only");fmt.Println("open",e);if e!=nil{return};defer s.Close();fmt.Println("partial",s.WriteSamples(context.Background(),[]int16{1,2,3}));fmt.Println("full",s.WriteSamples(context.Background(),make([]int16,audio.FrameSize)));fmt.Println("calls",h.calls)}
