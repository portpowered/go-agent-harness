// Package events serves a room's live event stream as server-sent events. It
// is presentation only: event projection, participant filtering, redaction,
// and the bounded drop policy belong to the rooms service stream it serves.
package events

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// Path is the only route the room stream serves.
const Path = "/events"

// Handler serves GET /events[?participant=<id>] from one room stream.
type Handler struct {
	stream rooms.RoomEventStream
}

// NewHandler binds the SSE transport to a room event stream.
func NewHandler(stream rooms.RoomEventStream) *Handler { return &Handler{stream: stream} }

// ServeHTTP streams forward-only frames until the client disconnects, the
// subscriber is dropped for falling behind, or the room stream closes.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.validRoute(w, r) {
		return
	}
	subscription, err := h.stream.Subscribe(r.URL.Query().Get("participant"))
	if err != nil {
		writeSubscribeError(w, err)
		return
	}
	defer subscription.Close()
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "room event stream requires an HTTP flusher", http.StatusInternalServerError)
		return
	}
	setStreamHeaders(w)
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	writeFrames(w, r, flusher, subscription.Frames())
}

func (h *Handler) validRoute(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.stream == nil || r.URL.Path != Path {
		http.NotFound(w, r)
		return false
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "room event stream requires GET /events", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

func writeSubscribeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, rooms.ErrUnknownRoomStreamParticipant):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, rooms.ErrRoomEventStreamClosed):
		http.Error(w, err.Error(), http.StatusGone)
	default:
		http.Error(w, fmt.Sprintf("room event stream: %v", err), http.StatusInternalServerError)
	}
}

func setStreamHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

// writeFrames frames each JSON event as one SSE "data:" record.
func writeFrames(w http.ResponseWriter, r *http.Request, flusher http.Flusher, frames <-chan []byte) {
	for {
		select {
		case <-r.Context().Done():
			return
		case payload, open := <-frames:
			if !open {
				return
			}
			frame := make([]byte, 0, len(payload)+len("data: \n\n"))
			frame = append(append(append(frame, "data: "...), payload...), '\n', '\n')
			if _, err := w.Write(frame); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

var _ http.Handler = (*Handler)(nil)
