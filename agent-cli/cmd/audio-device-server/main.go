package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

func main() {
	listenAddress := flag.String("listen", "127.0.0.1:0", "loopback listen address")
	sampleRate := flag.Int("sample-rate", audio.SampleRate, "mock device PCM sample rate")
	renderQuantum := flag.Int("render-quantum", audio.FrameSize, "render callback samples")
	captureQuantum := flag.Int("capture-quantum", audio.FrameSize, "capture callback samples")
	manualClock := flag.Bool("manual-clock", false, "advance device callbacks only through the test control API")
	flag.Parse()

	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		fatal(err)
	}
	host, _, err := net.SplitHostPort(listener.Addr().String())
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		_ = listener.Close()
		fatal(fmt.Errorf("audio-device server must listen on loopback, got %q", listener.Addr()))
	}
	registry, err := audio.NewSimulatedDuplexRegistry(audio.DuplexScenario{
		Render:  audio.ClockSpec{NominalRate: *sampleRate, Quanta: []int{*renderQuantum}},
		Capture: audio.ClockSpec{NominalRate: *sampleRate, Quanta: []int{*captureQuantum}},
	})
	if err != nil {
		_ = listener.Close()
		fatal(err)
	}
	server, err := audio.NewDeviceServer(registry)
	if err != nil {
		_ = listener.Close()
		fatal(err)
	}
	defer func() { _ = server.Close() }()
	if !*manualClock {
		if *sampleRate <= 0 || *renderQuantum <= 0 || *captureQuantum <= 0 {
			_ = listener.Close()
			fatal(fmt.Errorf("sample rate and callback quanta must be positive"))
		}
		period := time.Duration(*renderQuantum) * time.Second / time.Duration(*sampleRate)
		capturePeriod := time.Duration(*captureQuantum) * time.Second / time.Duration(*sampleRate)
		if period <= 0 || capturePeriod <= 0 || period != capturePeriod {
			_ = listener.Close()
			fatal(fmt.Errorf("realtime render and capture callback periods must be equal and non-zero; use --manual-clock for asymmetric clocks"))
		}
		go runRealtimeClock(registry, period)
	}

	ready := struct {
		Endpoint string `json:"endpoint"`
		Input    string `json:"input_device"`
		Output   string `json:"output_device"`
	}{Endpoint: listener.Addr().String(), Input: "simulated-duplex:input", Output: "simulated-duplex:output"}
	if err := json.NewEncoder(os.Stdout).Encode(ready); err != nil {
		_ = listener.Close()
		fatal(err)
	}

	httpServer := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second}
	if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		fatal(err)
	}
}

func runRealtimeClock(registry *audio.SimulatedDuplexRegistry, period time.Duration) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for range ticker.C {
		if err := registry.Advance(1); err != nil {
			fatal(fmt.Errorf("advance realtime device clock: %w", err))
		}
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "audio-device-server:", err)
	os.Exit(1)
}
