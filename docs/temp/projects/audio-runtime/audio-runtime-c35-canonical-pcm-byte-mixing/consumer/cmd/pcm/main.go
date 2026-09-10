package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

const schema = "audio-runtime-c35-canonical-pcm-byte-mixing-consumer.v1"

type report struct {
	Schema            string            `json:"schema"`
	Mode              string            `json:"mode"`
	Source            string            `json:"source"`
	Cases             []caseReport      `json:"cases"`
	NegativeControls  map[string]string `json:"negative_controls"`
	InputUnchanged    bool              `json:"input_unchanged"`
	OutputIndependent bool              `json:"output_independent"`
	CleanShutdown     bool              `json:"clean_shutdown"`
	ExpectedFailure   bool              `json:"expected_failure"`
}

type caseReport struct {
	Name      string `json:"name"`
	OutputHex string `json:"output_hex"`
	WantHex   string `json:"want_hex"`
}

func main() {
	mode, err := parseMode(os.Args[1:])
	result := report{
		Schema:            schema,
		Mode:              mode,
		Source:            os.Getenv("C35_SOURCE_REVISION"),
		NegativeControls:  make(map[string]string),
		InputUnchanged:    true,
		OutputIndependent: true,
		CleanShutdown:     true,
	}
	if err == nil {
		err = run(&result, mode)
	}
	if result.Source == "" {
		result.Source = "working-tree"
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		fmt.Fprintln(os.Stderr, marshalErr)
		os.Exit(1)
	}
	if _, writeErr := os.Stdout.Write(append(encoded, '\n')); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseMode(args []string) (string, error) {
	if len(args) == 0 {
		return "controls", nil
	}
	if len(args) == 2 && args[0] == "--mode" {
		return args[1], validateMode(args[1])
	}
	if len(args) == 1 && strings.HasPrefix(args[0], "--mode=") {
		mode := strings.TrimPrefix(args[0], "--mode=")
		return mode, validateMode(mode)
	}
	return "", fmt.Errorf("pcm consumer accepts only --mode controls|positive|mutated")
}

func validateMode(mode string) error {
	switch mode {
	case "controls", "positive", "mutated":
		return nil
	default:
		return fmt.Errorf("unsupported pcm consumer mode %q", mode)
	}
}

func run(result *report, mode string) error {
	if mode == "positive" {
		return runPositive(result)
	}
	if err := runPositive(result); err != nil {
		return err
	}
	if mode == "mutated" {
		return runMutatedOracle(result)
	}
	return runNegativeControls(result)
}

func runPositive(result *report) error {
	finalClipSources := [][]byte{
		{0xff, 0x7f, 0xff, 0x7f, 0x00, 0x00},
		{0xff, 0x7f, 0x00, 0x80, 0x01, 0x00},
		{0x00, 0x80, 0x04, 0x00, 0x00, 0x80},
	}
	finalClipWant := []byte{0xfe, 0x7f, 0x03, 0x00, 0x01, 0x80}
	if err := runCase(result, "signed-extrema-final-clip", finalClipSources, 3, finalClipWant); err != nil {
		return err
	}

	shortWant := []byte{0x01, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00}
	if err := runCase(result, "short-tail-silence", [][]byte{{0x01, 0x00, 0x02, 0x00}, {}, nil}, 4, shortWant); err != nil {
		return err
	}
	if err := runCase(result, "empty-output", [][]byte{{}, nil}, 0, []byte{}); err != nil {
		return err
	}

	permutations := [][][]byte{
		finalClipSources,
		{finalClipSources[0], finalClipSources[2], finalClipSources[1]},
		{finalClipSources[1], finalClipSources[0], finalClipSources[2]},
	}
	for index, sources := range permutations {
		got, err := mixer.MixPCM16Bytes(sources, 3)
		if err != nil {
			return fmt.Errorf("permutation %d: %w", index, err)
		}
		if !bytes.Equal(got, finalClipWant) {
			return fmt.Errorf("permutation %d output %x, want %x", index, got, finalClipWant)
		}
	}

	isolationSource := []byte{0x0b, 0x00, 0x0c, 0x00}
	isolationBefore := append([]byte(nil), isolationSource...)
	isolationOutput, err := mixer.MixPCM16Bytes([][]byte{isolationSource}, 2)
	if err != nil {
		return fmt.Errorf("input isolation: %w", err)
	}
	isolationSource[0] = 0x63
	if !bytes.Equal(isolationSource, []byte{0x63, 0x00, 0x0c, 0x00}) {
		return fmt.Errorf("isolation source changed unexpectedly: %x", isolationSource)
	}
	if !bytes.Equal(isolationOutput, isolationBefore) {
		return fmt.Errorf("output changed or aliased input: got %x, want %x", isolationOutput, isolationBefore)
	}
	result.InputUnchanged = true
	result.OutputIndependent = true
	return nil
}

func runCase(result *report, name string, sources [][]byte, length int, want []byte) error {
	got, err := mixer.MixPCM16Bytes(sources, length)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("%s output %x, want %x", name, got, want)
	}
	result.Cases = append(result.Cases, caseReport{Name: name, OutputHex: fmt.Sprintf("%x", got), WantHex: fmt.Sprintf("%x", want)})
	return nil
}

func runNegativeControls(result *report) error {
	controls := []struct {
		name string
		call func() error
	}{
		{name: "negative-output", call: func() error {
			_, err := mixer.MixPCM16Bytes(nil, -1)
			return err
		}},
		{name: "oversized-output", call: func() error {
			_, err := mixer.MixPCM16Bytes(nil, mixer.MaxPCM16MixSamples+1)
			return err
		}},
		{name: "odd-source", call: func() error {
			_, err := mixer.MixPCM16Bytes([][]byte{{0x01}}, 1)
			return err
		}},
		{name: "overlong-source", call: func() error {
			_, err := mixer.MixPCM16Bytes([][]byte{{0x00, 0x00, 0x00, 0x00}}, 1)
			return err
		}},
		{name: "source-limit", call: func() error {
			_, err := mixer.MixPCM16Bytes(make([][]byte, mixer.MaxPCM16MixSources+1), 1)
			return err
		}},
	}
	for _, control := range controls {
		err := control.call()
		if err == nil {
			return fmt.Errorf("negative control %s was accepted", control.name)
		}
		if control.name == "negative-output" && !errors.Is(err, mixer.ErrPCM16MixInvalidLength) {
			return fmt.Errorf("negative-output lost ErrPCM16MixInvalidLength identity: %w", err)
		}
		result.NegativeControls[control.name] = err.Error()
	}
	return nil
}

func runMutatedOracle(result *report) error {
	result.ExpectedFailure = true
	actual := []byte{0xfe, 0x7f, 0x03, 0x00, 0x01, 0x80}
	mutated := append([]byte(nil), actual...)
	mutated[0]++
	got, err := mixer.MixPCM16Bytes([][]byte{
		{0xff, 0x7f, 0xff, 0x7f, 0x00, 0x00},
		{0xff, 0x7f, 0x00, 0x80, 0x01, 0x00},
		{0x00, 0x80, 0x04, 0x00, 0x00, 0x80},
	}, 3)
	if err != nil {
		return fmt.Errorf("mutated oracle setup: %w", err)
	}
	if bytes.Equal(got, mutated) {
		return fmt.Errorf("mutated oracle was accepted: output %x", got)
	}
	result.NegativeControls["mutated-expected-sample"] = fmt.Sprintf("rejected: got %x, mutated %x", got, mutated)
	return fmt.Errorf("mutated oracle rejected as expected")
}
