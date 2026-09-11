package stream

import "fmt"

// ScheduledInput is kept local to the implementation file through the alias
// declared in source.go.

// CloneScheduled copies the finite payload before a host transfers ownership.
func CloneScheduled(input ScheduledInput) ScheduledInput {
	input.PCM = append([]byte(nil), input.PCM...)
	return input
}

// PrepareScheduled loads finite host-selected turns through one decoder
// callback, assigns stable completed-turn admission, and rejects empty turns.
func (s *Service) PrepareScheduled(paths []string, load func(string) ([]byte, int, error)) ([]ScheduledInput, error) {
	inputs := make([]ScheduledInput, 0, len(paths))
	for index, path := range paths {
		pcm, rate, err := load(path)
		if err != nil {
			return nil, fmt.Errorf("load audio turn %d from %q: %w", index+1, path, err)
		}
		if len(pcm) == 0 {
			return nil, fmt.Errorf("load audio turn %d from %q: %w", index+1, path, newEmptyError(path))
		}
		inputs = append(inputs, ScheduledInput{AfterCompletedTurns: index, PCM: pcm, SourceSampleRate: rate, EndOfTurn: true})
	}
	return inputs, nil
}

// PrepareScheduledAs keeps host-specific schedule names as thin adapters.
func PrepareScheduledAs[T any](paths []string, load func(string) ([]byte, int, error), decode func(ScheduledInput) T) ([]T, error) {
	inputs, err := (&Service{}).PrepareScheduled(paths, load)
	if err != nil {
		return nil, err
	}
	result := make([]T, len(inputs))
	for index, input := range inputs {
		result[index] = decode(input)
	}
	return result, nil
}

// ConvertScheduled adapts a host schedule to the public runtime representation
// without allowing conversion to alias or mutate the host slice.
func ConvertScheduled[T any](service AudioService, inputs []T, providerRate int, encode func(T) ScheduledInput, decode func(ScheduledInput) T) ([]T, error) {
	runtimeInputs := make([]ScheduledInput, len(inputs))
	for index, input := range inputs {
		runtimeInputs[index] = encode(input)
	}
	converted, err := service.ConvertScheduled(runtimeInputs, providerRate)
	if err != nil {
		return nil, err
	}
	result := make([]T, len(converted))
	for index, input := range converted {
		result[index] = decode(input)
	}
	return result, nil
}
