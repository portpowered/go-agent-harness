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
//   - Provider (provider.go), the "openai-live" session provider for
//     gpt-live-1: it dials the primary WebSocket with headers from a
//     CredentialProvider, waits for session.started, and runs the session on
//     the shared state machine (internal/livesession: synthesized speech
//     segments, a fail-closed outbound mapping and the close handshake) with
//     the public dialect (session.go).
//
// gpt-live-1-codex, the model a ChatGPT login opens, is the same provider
// name on a different route: the codexlive subpackage runs the same state
// machine over WebRTC and a sideband in the quicksilver dialect.
//
// A client delegation (session.delegation.created) becomes DELEGATION.CREATED
// with no response id, once the user transcript covering it arrives or a
// settle window passes (delegations.go), and CONTEXT.APPEND becomes the
// matching append command, split into chunks of at most 500 bytes
// (context_append.go). The design, the protocol spec it follows and the
// phased plan are in docs/architecture/gpt-live-provider.md. The scripted
// fake server used by tests lives in the fakelive subpackage.
package openailive
