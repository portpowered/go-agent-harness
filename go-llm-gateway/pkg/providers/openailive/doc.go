// Package openailive speaks the OpenAI GPT-Live protocol (model gpt-live-1,
// endpoint /v1/live/sessions).
//
// GPT-Live is not the Realtime API. It has its own endpoint, its own
// session.* event vocabulary, no turn detector and no response lifecycle, so
// it is a separate session provider ("openai-live") rather than a model of
// the "openai" Realtime provider.
//
// This package currently holds the wire layer only:
//
//   - typed structs for every documented client and server event
//     (protocol.go, events.go);
//   - EncodeEvent, DecodeServerEvent and DecodeClientEvent (codec.go), where
//     an unknown event type decodes to UnknownEvent instead of failing;
//   - BuildSessionStart (start.go), which turns a models.SessionConfig and the
//     GPT-Live Options carried in its raw Config into a strict session.start
//     event.
//
// No production session reaches this package yet. The design, the protocol
// spec it follows and the phased plan are in
// docs/architecture/gpt-live-provider.md. The scripted fake server used by
// tests lives in the fakelive subpackage.
package openailive
