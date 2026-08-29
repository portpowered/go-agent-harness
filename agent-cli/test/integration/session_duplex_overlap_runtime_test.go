package integration

import (
	"bytes"
	"context"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type v8DuplexRun struct {
	base       time.Time
	crossings  []v8Crossing
	harnesses  map[string]v8HarnessResult
	views      map[string]*v8RecordingView
	artifacts  map[string]string
	terminal   map[string]v8TerminalFact
	finalTick  uint64
	turnsBound int
}

func newV8CLI(t *testing.T, logicalClock *clock.Deterministic, observer *v8RuntimeObserver) *cli.AgentCLI {
	t.Helper()
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortClock, logicalClock),
		wire.NewPortSwap(wire.PortSessionRuntimeObserver, observer),
	)
	if err != nil {
		t.Fatalf("initialize v8 CLI with shared clock and runtime observer: %v", err)
	}
	return agentCLI
}

func runV8Duplex(t *testing.T, aToB, bToA []byte, mutateFirst bool) v8DuplexRun {
	t.Helper()
	base := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	logicalClock := clock.NewDeterministic(base, v8TickDuration)
	logicalClock.AdvanceTo(v8OverlapTick)
	coordinator := newV8CrossingCoordinator()

	silencePath := v8AudioFixturePath(t, "silence_16k.wav")
	silenceWAV, err := os.ReadFile(silencePath)
	if err != nil {
		t.Fatalf("read v8 silence fixture: %v", err)
	}
	_, silenceSamples, err := wavio.Read(bytes.NewReader(silenceWAV))
	if err != nil {
		t.Fatalf("parse v8 silence fixture: %v", err)
	}
	if len(silenceSamples) < audio.FrameSize {
		t.Fatalf("v8 silence fixture has %d samples, want at least %d", len(silenceSamples), audio.FrameSize)
	}
	silenceFrame := v8PCM16Bytes(silenceSamples[:audio.FrameSize])

	views := map[string]*v8RecordingView{
		"A/client": {Harness: "A", Role: "client"},
		"A/agent":  {Harness: "A", Role: "agent"},
		"B/client": {Harness: "B", Role: "client"},
		"B/agent":  {Harness: "B", Role: "agent"},
	}
	aToBBridge := newV8PCMBridge(coordinator, "A-to-B", views["A/client"], views["B/agent"], silenceFrame, mutateFirst)
	bToABridge := newV8PCMBridge(coordinator, "B-to-A", views["B/client"], views["A/agent"], silenceFrame, false)
	aObserver := &v8RuntimeObserver{outputBridge: aToBBridge}
	bObserver := &v8RuntimeObserver{outputBridge: bToABridge}

	runDir := t.TempDir()
	aReplay := filepath.Join(runDir, "harness-a.session.json")
	bReplay := filepath.Join(runDir, "harness-b.session.json")
	writeV8ReplayCapture(t, aReplay, "s2s-v8-harness-a", v8HarnessAInstruction, aToB, bToA)
	replayedAToB := aToB
	if mutateFirst {
		replayedAToB = silenceFrame
	}
	writeV8ReplayCapture(t, bReplay, "s2s-v8-harness-b", v8HarnessBInstruction, bToA, replayedAToB)

	// Construct both generated shipped CLIs before starting either command and
	// pass the same *clock.Deterministic identity and a runtime observer through
	// both composition graphs. The goroutines execute only `agent session`; no
	// loop, provider, or replay helper is the evidence path. The observer is
	// fed by the session runtime itself, including its clock-stamped output.
	aCLI := newV8CLI(t, logicalClock, aObserver)
	bCLI := newV8CLI(t, logicalClock, bObserver)

	ctx, cancel := context.WithTimeout(context.Background(), v8RunTimeout)
	defer cancel()
	results := make(chan v8HarnessResult, 2)
	startGate := make(chan struct{})
	var wg sync.WaitGroup
	start := func(name, instruction, replayPath string, input io.Reader, output io.Writer, commandCLI *cli.AgentCLI, observer *v8RuntimeObserver) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			started := time.Now()
			root := commandCLI.Generate()
			root.SetIn(input)
			root.SetOut(output)
			root.SetErr(io.Discard)
			root.SetArgs([]string{
				"session",
				"--replay", replayPath,
				"--audio-in", "-",
				"--audio-out", "-",
				"--wait-for-close",
				"--max-duration", v8CommandMaxDuration.String(),
				instruction,
			})
			results <- v8HarnessResult{
				Name:        name,
				Instruction: instruction,
				ReplayPath:  replayPath,
				Err:         root.ExecuteContext(ctx),
				Elapsed:     time.Since(started),
				Runtime:     observer.snapshot(),
			}
		}()
	}
	start("A", v8HarnessAInstruction, aReplay, v8PCMReader{bridge: bToABridge}, v8PCMWriter{bridge: aToBBridge}, aCLI, aObserver)
	start("B", v8HarnessBInstruction, bReplay, v8PCMReader{bridge: aToBBridge}, v8PCMWriter{bridge: bToABridge}, bCLI, bObserver)
	close(startGate)

	harnesses := make(map[string]v8HarnessResult, 2)
	contextDone := ctx.Done()
	cleanupTimer := time.NewTimer(v8RunTimeout + time.Second)
	defer cleanupTimer.Stop()
	for len(harnesses) < 2 {
		select {
		case result := <-results:
			harnesses[result.Name] = result
			if result.Err != nil {
				coordinator.abortRun()
				cancel()
			}
		case <-contextDone:
			coordinator.abortRun()
			cancel()
			contextDone = nil
		case <-cleanupTimer.C:
			coordinator.abortRun()
			cancel()
			t.Fatal("v8 CLI harnesses did not return after the bounded cleanup window")
		}
	}
	wg.Wait()

	finalTick := uint64(0)
	run := v8DuplexRun{
		base:       base,
		crossings:  coordinator.snapshot(),
		harnesses:  harnesses,
		views:      views,
		terminal:   map[string]v8TerminalFact{},
		finalTick:  finalTick,
		turnsBound: v8TurnBound,
	}
	for name, result := range harnesses {
		terminalObservation, err := v8RuntimeObservation(result.Runtime, services.SessionRuntimeObservationTerminal)
		if err != nil {
			t.Fatalf("harness %s terminal runtime observation: %v", name, err)
		}
		terminal := v8TerminalFact{
			Clean:          terminalObservation.Clean,
			Turns:          terminalObservation.TurnsCompleted,
			FinalTick:      terminalObservation.Tick,
			FinalTimestamp: terminalObservation.Timestamp,
			Error:          terminalObservation.Error,
		}
		if terminal.FinalTick > finalTick {
			finalTick = terminal.FinalTick
		}
		if name == "A" {
			terminal.InputEOF = bToABridge.observedEOF()
			terminal.OutputFrame = aToBBridge.wroteFrame()
		} else {
			terminal.InputEOF = aToBBridge.observedEOF()
			terminal.OutputFrame = bToABridge.wroteFrame()
		}
		run.terminal[name] = terminal
	}
	run.finalTick = finalTick
	for name, view := range views {
		terminal := run.terminal[view.Harness]
		viewPath := filepath.Join(runDir, strings.ReplaceAll(name, "/", "-")+".json")
		wavPath := filepath.Join(runDir, strings.ReplaceAll(name, "/", "-")+".wav")
		writeV8ViewArtifacts(t, view, terminal, viewPath, wavPath)
		run.artifacts = appendArtifactPaths(run.artifacts, name, viewPath, wavPath)
	}
	return run
}

func runV8MultiTurnDuplex(t *testing.T, aToB, bToA [][]byte) v8DuplexRun {
	t.Helper()
	if len(aToB) != v8MultiTurnCount || len(bToA) != v8MultiTurnCount {
		t.Fatalf("v8 multi-turn run has A-to-B=%d B-to-A=%d frames, want %d each", len(aToB), len(bToA), v8MultiTurnCount)
	}
	base := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	logicalClock := clock.NewDeterministic(base, v8TickDuration)
	logicalClock.AdvanceTo(v8OverlapTick)
	coordinator := newV8MultiTurnCoordinator(logicalClock, base)
	aTurnTwoReady := make(chan struct{})
	bTurnTwoReady := make(chan struct{})
	views := map[string]*v8RecordingView{
		"A/client": {Harness: "A", Role: "client"},
		"A/agent":  {Harness: "A", Role: "agent"},
		"B/client": {Harness: "B", Role: "client"},
		"B/agent":  {Harness: "B", Role: "agent"},
	}
	aToBBridge := newV8MultiTurnBridge(coordinator, "A-to-B", views["A/client"], views["B/agent"], bTurnTwoReady)
	bToABridge := newV8MultiTurnBridge(coordinator, "B-to-A", views["B/client"], views["A/agent"], aTurnTwoReady)
	aObserver := &v8RuntimeObserver{outputBridge: aToBBridge, inputBridge: bToABridge, turnTwoReady: aTurnTwoReady}
	bObserver := &v8RuntimeObserver{outputBridge: bToABridge, inputBridge: aToBBridge, turnTwoReady: bTurnTwoReady}
	aStream := &v8StreamRecorder{}
	bStream := &v8StreamRecorder{}

	runDir := t.TempDir()
	aReplay := filepath.Join(runDir, "harness-a-multiturn.session.json")
	bReplay := filepath.Join(runDir, "harness-b-multiturn.session.json")
	writeV8MultiTurnReplayCapture(t, aReplay, "s2s-v8-multiturn-harness-a", v8HarnessAInstruction, "A", aToB, bToA)
	writeV8MultiTurnReplayCapture(t, bReplay, "s2s-v8-multiturn-harness-b", v8HarnessBInstruction, "B", bToA, aToB)

	// Both commands are generated from the same composition path and share
	// the exact deterministic clock object. Stream markers are observed only
	// after they cross the command's session-loop boundary.
	aCLI := newV8CLI(t, logicalClock, aObserver)
	bCLI := newV8CLI(t, logicalClock, bObserver)
	aCLI.SetSessionStreamObserver(aStream.Observe)
	bCLI.SetSessionStreamObserver(bStream.Observe)

	ctx, cancel := context.WithTimeout(context.Background(), v8RunTimeout)
	defer cancel()
	results := make(chan v8HarnessResult, 2)
	startGate := make(chan struct{})
	var wg sync.WaitGroup
	start := func(name, instruction, replayPath string, input io.Reader, output io.Writer, commandCLI *cli.AgentCLI, observer *v8RuntimeObserver, stream *v8StreamRecorder) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			started := time.Now()
			root := commandCLI.Generate()
			root.SetIn(input)
			root.SetOut(output)
			root.SetErr(io.Discard)
			root.SetArgs([]string{
				"session",
				"--replay", replayPath,
				"--audio-in", "-",
				"--audio-out", "-",
				"--max-duration", v8CommandMaxDuration.String(),
				instruction,
			})
			results <- v8HarnessResult{
				Name:        name,
				Instruction: instruction,
				ReplayPath:  replayPath,
				Err:         root.ExecuteContext(ctx),
				Elapsed:     time.Since(started),
				Runtime:     observer.snapshot(),
				Stream:      stream.snapshot(),
			}
		}()
	}
	start("A", v8HarnessAInstruction, aReplay, &v8MultiTurnPCMReader{bridge: bToABridge}, v8MultiTurnPCMWriter{bridge: aToBBridge}, aCLI, aObserver, aStream)
	start("B", v8HarnessBInstruction, bReplay, &v8MultiTurnPCMReader{bridge: aToBBridge}, v8MultiTurnPCMWriter{bridge: bToABridge}, bCLI, bObserver, bStream)
	close(startGate)

	harnesses := make(map[string]v8HarnessResult, 2)
	contextDone := ctx.Done()
	cleanupTimer := time.NewTimer(v8RunTimeout + time.Second)
	defer cleanupTimer.Stop()
	for len(harnesses) < 2 {
		select {
		case result := <-results:
			harnesses[result.Name] = result
			if result.Err != nil {
				coordinator.abortRun()
				cancel()
			}
		case <-contextDone:
			coordinator.abortRun()
			cancel()
			contextDone = nil
		case <-cleanupTimer.C:
			coordinator.abortRun()
			cancel()
			t.Fatal("v8 multi-turn CLI harnesses did not return after the bounded cleanup window")
		}
	}
	wg.Wait()

	run := v8DuplexRun{
		base:       base,
		crossings:  coordinator.snapshot(),
		harnesses:  harnesses,
		views:      views,
		terminal:   map[string]v8TerminalFact{},
		turnsBound: v8MultiTurnCount,
	}
	for name, result := range harnesses {
		terminalObservation, err := v8RuntimeObservation(result.Runtime, services.SessionRuntimeObservationTerminal)
		if err != nil {
			t.Fatalf("harness %s terminal runtime observation: %v", name, err)
		}
		terminal := v8TerminalFact{
			Clean:          terminalObservation.Clean,
			Turns:          terminalObservation.TurnsCompleted,
			FinalTick:      terminalObservation.Tick,
			FinalTimestamp: terminalObservation.Timestamp,
			Error:          terminalObservation.Error,
		}
		if terminal.FinalTick > run.finalTick {
			run.finalTick = terminal.FinalTick
		}
		if name == "A" {
			terminal.InputEOF = bToABridge.observedEOF()
			terminal.OutputFrame = aToBBridge.wroteFrames() == v8MultiTurnCount
		} else {
			terminal.InputEOF = aToBBridge.observedEOF()
			terminal.OutputFrame = bToABridge.wroteFrames() == v8MultiTurnCount
		}
		run.terminal[name] = terminal
	}
	for name, view := range views {
		terminal := run.terminal[view.Harness]
		viewPath := filepath.Join(runDir, strings.ReplaceAll(name, "/", "-")+"-multiturn.json")
		wavPath := filepath.Join(runDir, strings.ReplaceAll(name, "/", "-")+"-multiturn.wav")
		writeV8ViewArtifacts(t, view, terminal, viewPath, wavPath)
		run.artifacts = appendArtifactPaths(run.artifacts, name, viewPath, wavPath)
	}
	return run
}
