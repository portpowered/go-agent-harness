package wire

import (
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roommanifest "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/manifest"
)

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
func NewManifestProvider(options rooms.ValidationOptions) ManifestAdmission {
	return roommanifest.NewProvider(options)
}

// NewManifestAdmission is a descriptive constructor alias for embedders.
func NewManifestAdmission(options rooms.ValidationOptions) ManifestAdmission {
	return NewManifestProvider(options)
}

// NewManifestProviderFromRegistry creates an admission provider from finite
// provider/model/tool/voice registries and an optional credential-name lookup.
// The registry callbacks are captured by value; no resolved credential is
// retained by the provider.
func NewManifestProviderFromRegistry(registry rooms.ValidationRegistry, lookupCredential func(string) (string, bool)) ManifestAdmission {
	return roommanifest.NewProviderFromRegistry(registry, lookupCredential)
}

// NewManifestAdmissionFromRegistry is the matching constructor alias.
func NewManifestAdmissionFromRegistry(registry rooms.ValidationRegistry, lookupCredential func(string) (string, bool)) ManifestAdmission {
	return NewManifestProviderFromRegistry(registry, lookupCredential)
}

// NewValidationRegistry builds a copied registry for a deterministic host.
func NewValidationRegistry(providers []string, models map[string][]string, tools []string, voices map[string][]string) rooms.ValidationRegistry {
	return rooms.ValidationRegistry{
		Providers: registrySet(providers, true),
		Models:    registryMap(models),
		Tools:     registrySet(tools, true),
		Voices:    registryMap(voices),
	}
}

func registrySet(values []string, lower bool) map[string]struct{} {
	if values == nil {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[normalizeRegistryValue(value, lower)] = struct{}{}
	}
	return set
}

func registryMap(values map[string][]string) map[string]map[string]struct{} {
	if values == nil {
		return nil
	}
	result := make(map[string]map[string]struct{}, len(values))
	for provider, entries := range values {
		result[normalizeRegistryValue(provider, true)] = registrySet(entries, false)
	}
	return result
}

func normalizeRegistryValue(value string, lower bool) string {
	value = strings.TrimSpace(value)
	if lower {
		return strings.ToLower(value)
	}
	return value
}
