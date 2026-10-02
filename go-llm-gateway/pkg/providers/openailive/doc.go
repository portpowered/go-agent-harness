// Package openailive speaks the OpenAI GPT-Live protocol (model gpt-live-1,
// endpoint /v1/live/sessions).
//
// GPT-Live is not the Realtime API. It has its own endpoint, its own
// session.* event vocabulary, no turn detector and no response lifecycle, so
// it is a separate session provider ("openai-live") rather than a model of
// the "openai" Realtime provider.
//
// The package holds:
//
//   - typed structs for every documented client and server event
//     (protocol.go, events.go);
//   - EncodeEvent, DecodeServerEvent and DecodeClientEvent (codec.go), where
//     an unknown event type decodes to UnknownEvent instead of failing;
//   - BuildSessionStart (start.go), which turns a models.SessionConfig and the
//     GPT-Live Options carried in its raw Config into a strict session.start
//     event;
//   - Provider (provider.go), the "openai-live" session provider: it dials the
//     primary WebSocket with headers from a CredentialProvider, waits for
//     session.started, and maps the session onto the harness stream with
//     synthesized speech segments (segments.go), a fail-closed outbound
//     mapping and the session.close handshake.
//
// Client delegations (session.delegation.created) are logged and ignored
// until the delegation phase. The design, the protocol spec it follows and
// the phased plan are in docs/architecture/gpt-live-provider.md. The scripted
// fake server used by tests lives in the fakelive subpackage.
package openailive
