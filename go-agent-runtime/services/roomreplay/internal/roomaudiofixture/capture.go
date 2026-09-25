package roomaudiofixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func captureJSONForModel(id, model string) []byte {
	payload, err := json.Marshal(map[string]any{"type": "session.created", "session": map[string]any{"id": id + "-offline-session"}})
	if err != nil {
		panic(err)
	}
	type providerMetadata struct {
		Name  string `json:"name,omitempty"`
		Model string `json:"model,omitempty"`
	}
	type sessionMetadata struct {
		ID                string `json:"id,omitempty"`
		StartedAtUTC      string `json:"started_at_utc,omitempty"`
		FixtureProvenance string `json:"fixture_provenance,omitempty"`
	}
	type record struct {
		Sequence    int             `json:"sequence"`
		Direction   string          `json:"direction"`
		Timestamp   int64           `json:"timestamp_ms"`
		Type        string          `json:"type"`
		PayloadType string          `json:"payload_type"`
		Payload     json.RawMessage `json:"payload,omitempty"`
	}
	type integrity struct {
		Algorithm string `json:"algorithm"`
		Coverage  string `json:"coverage"`
		Digest    string `json:"digest"`
	}
	type captureEnvelope struct {
		Version            int              `json:"version"`
		Provider           providerMetadata `json:"provider"`
		Session            sessionMetadata  `json:"session"`
		Records            []record         `json:"records"`
		Integrity          *integrity       `json:"integrity,omitempty"`
		EndsWithDisconnect bool             `json:"ends_with_disconnect,omitempty"`
	}
	base := captureEnvelope{
		Version:  2,
		Provider: providerMetadata{Name: "offline", Model: model},
		Session: sessionMetadata{
			ID:                id + "-offline-session",
			StartedAtUTC:      "2026-08-30T12:00:00Z",
			FixtureProvenance: "synthetic",
		},
		Records: []record{{
			Sequence:    1,
			Direction:   "server_to_client",
			Timestamp:   0,
			Type:        "session.created",
			PayloadType: "websocket_message",
			Payload:     json.RawMessage(payload),
		}},
	}
	coverage, err := json.Marshal(base)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(coverage)
	base.Integrity = &integrity{
		Algorithm: "sha256",
		Coverage:  "session_capture.v2:json(version,provider,session,records,ends_with_disconnect)",
		Digest:    hex.EncodeToString(digest[:]),
	}
	data, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}
