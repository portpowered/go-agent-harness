package wire

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roommanifest "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/manifest"
)

// ManifestProvider is the small, inert admission boundary for room
// documents. It owns no files, goroutines, providers, devices, or resolved
// credentials; every Parse/Read call uses the copied validation callbacks and
// returns only the normalized public contract.
type ManifestProvider struct {
	options rooms.ValidationOptions
}

// ManifestAdmission is the public surface needed by embedders that only need
// room-document admission and do not need the live room service graph.
type ManifestAdmission interface {
	Parse([]byte, ...rooms.ValidationOptions) (rooms.Manifest, error)
	Read(string, ...rooms.ValidationOptions) (rooms.Manifest, error)
	Admit([]byte, ...rooms.ValidationOptions) (rooms.Manifest, error)
	AdmitFile(string, ...rooms.ValidationOptions) (rooms.Manifest, error)
	Validate(rooms.Manifest, ...rooms.ValidationOptions) error
	Close() error
}

// NewManifestProvider creates an admission provider from explicit host
// validation callbacks. A nil credential callback checks only the credential
// name and never reads the process environment.
func NewManifestProvider(options rooms.ValidationOptions) *ManifestProvider {
	return &ManifestProvider{options: options}
}

// NewManifestAdmission is a descriptive constructor alias for embedders.
func NewManifestAdmission(options rooms.ValidationOptions) *ManifestProvider {
	return NewManifestProvider(options)
}

// NewManifestProviderFromRegistry creates an admission provider from finite
// provider/model/tool/voice registries and an optional credential-name lookup.
// The registry callbacks are captured by value; no resolved credential is
// retained by the provider.
func NewManifestProviderFromRegistry(registry rooms.ValidationRegistry, lookupCredential func(string) (string, bool)) *ManifestProvider {
	registry = cloneValidationRegistry(registry)
	options := registry.Options()
	options.LookupCredential = lookupCredential
	return NewManifestProvider(options)
}

// NewManifestAdmissionFromRegistry is the matching constructor alias.
func NewManifestAdmissionFromRegistry(registry rooms.ValidationRegistry, lookupCredential func(string) (string, bool)) *ManifestProvider {
	return NewManifestProviderFromRegistry(registry, lookupCredential)
}

// Parse admits one JSON or YAML document with the provider's callbacks. A
// single per-call override is supported for hosts that reuse the provider
// while changing an injected registry; the override is not retained.
func (p *ManifestProvider) Parse(data []byte, overrides ...rooms.ValidationOptions) (rooms.Manifest, error) {
	options, err := p.optionsFor(overrides)
	if err != nil {
		return rooms.Manifest{}, err
	}
	return roommanifest.Parse(data, options)
}

// Read admits one document from a file path.
func (p *ManifestProvider) Read(path string, overrides ...rooms.ValidationOptions) (rooms.Manifest, error) {
	options, err := p.optionsFor(overrides)
	if err != nil {
		return rooms.Manifest{}, err
	}
	return roommanifest.Read(path, options)
}

// Admit is an explicit alias for Parse for callers describing admission as a
// boundary operation.
func (p *ManifestProvider) Admit(data []byte, overrides ...rooms.ValidationOptions) (rooms.Manifest, error) {
	return p.Parse(data, overrides...)
}

// AdmitFile is an explicit alias for Read.
func (p *ManifestProvider) AdmitFile(path string, overrides ...rooms.ValidationOptions) (rooms.Manifest, error) {
	return p.Read(path, overrides...)
}

// Validate checks an already normalized manifest with the provider's
// callbacks. It performs no host lookup unless one was injected.
func (p *ManifestProvider) Validate(value rooms.Manifest, overrides ...rooms.ValidationOptions) error {
	options, err := p.optionsFor(overrides)
	if err != nil {
		return err
	}
	return value.Validate(options)
}

// Close is intentionally a no-op: admission owns no external resources.
func (*ManifestProvider) Close() error { return nil }

func (p *ManifestProvider) optionsFor(overrides []rooms.ValidationOptions) (rooms.ValidationOptions, error) {
	if p == nil {
		return rooms.ValidationOptions{}, fmt.Errorf("%w: manifest provider is nil", rooms.ErrInvalidManifest)
	}
	if len(overrides) > 1 {
		return rooms.ValidationOptions{}, &rooms.ValidationError{Field: "options", Problem: "at most one validation option set is supported", Cause: rooms.ErrInvalidManifest}
	}
	if len(overrides) == 1 {
		return overrides[0], nil
	}
	return p.options, nil
}

func cloneValidationRegistry(value rooms.ValidationRegistry) rooms.ValidationRegistry {
	clone := rooms.ValidationRegistry{}
	if value.Providers != nil {
		clone.Providers = make(map[string]struct{}, len(value.Providers))
		for key := range value.Providers {
			clone.Providers[key] = struct{}{}
		}
	}
	if value.Models != nil {
		clone.Models = make(map[string]map[string]struct{}, len(value.Models))
		for provider, models := range value.Models {
			modelClone := make(map[string]struct{}, len(models))
			for model := range models {
				modelClone[model] = struct{}{}
			}
			clone.Models[provider] = modelClone
		}
	}
	if value.Tools != nil {
		clone.Tools = make(map[string]struct{}, len(value.Tools))
		for key := range value.Tools {
			clone.Tools[key] = struct{}{}
		}
	}
	if value.Voices != nil {
		clone.Voices = make(map[string]map[string]struct{}, len(value.Voices))
		for provider, voices := range value.Voices {
			voiceClone := make(map[string]struct{}, len(voices))
			for voice := range voices {
				voiceClone[voice] = struct{}{}
			}
			clone.Voices[provider] = voiceClone
		}
	}
	return clone
}

var _ ManifestAdmission = (*ManifestProvider)(nil)
