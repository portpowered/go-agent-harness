// Package codexrtc is the transport of the GPT-Live route a ChatGPT login can
// open (model gpt-live-1-codex, docs/architecture/chatgpt-oauth.md route C):
//
//  1. CallClient.Create posts a local SDP offer and the session to
//     https://chatgpt.com/backend-api/codex/realtime/calls?intent=quicksilver&architecture=avas
//     as JSON {sdp, session}, with the ChatGPT bearer, chatgpt-account-id,
//     OpenAI-Alpha: quicksilver=v2, originator and the three session ids. The
//     response body is the SDP answer and Location names the call id.
//  2. Peer is the WebRTC side: one send-and-receive Opus audio track. It
//     creates the offer, applies the answer, and converts 48 kHz mono PCM16
//     frames of 20 ms to and from Opus with go-audio's pure-Go codec.
//  3. CallClient.DialSideband opens the control WebSocket at
//     wss://api.openai.com/v1/live/{call_id} with the same identity headers,
//     and Sideband carries the quicksilver dialect (package quicksilver).
//
// The credential comes from a CredentialSource on every request, so a
// refreshed ChatGPT token is used without rebuilding the client. The ChatGPT
// token manager plugs in through CredentialFunc: chatgptauth.Manager.Credential
// (go-llm-gateway/pkg/providers/openai/chatgptauth) returns a credential whose
// AccessToken and AccountID map one to one onto Credential.
//
// No production session reaches this package yet. Tests run it against the
// fakecodex subpackage: an httptest call-creation endpoint backed by an
// in-process pion answerer on a virtual network, and a fake sideband.
package codexrtc
