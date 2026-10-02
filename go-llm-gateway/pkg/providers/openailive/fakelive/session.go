package fakelive

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
)

// Fake-only error codes. The published reference documents only
// unknown_parameter, immutable_field_update and invalid_audio, so these name
// the other failures the fake detects.
const (
	CodeSessionStartRequired     = "session_start_required"
	CodeSessionAlreadyStarted    = "session_already_started"
	CodeMissingRequiredParameter = "missing_required_parameter"
	CodeInvalidValue             = "invalid_value"
	CodeUnknownDelegation        = "unknown_delegation"
	CodeUnknownEventType         = "unknown_event_type"
	CodeMalformedEvent           = "malformed_event"
)

const millisPerSecond = 1000

// errSessionDone stops a script step because the connection ended.
var errSessionDone = errors.New("fakelive: connection ended")

// session is the server state of one connection.
type session struct {
	server *Server
	conn   wireConn

	writeMu sync.Mutex

	mu          sync.Mutex
	changed     chan struct{}
	started     bool
	resource    openailive.SessionResource
	format      openailive.AudioFormat
	counts      map[string]int
	delegations map[string]bool
	inputBytes  int64
	nextID      int

	done     chan struct{}
	doneOnce sync.Once
}

func (s *Server) serve(conn wireConn, recordWrites bool) {
	sess := &session{
		server:      s,
		conn:        conn,
		changed:     make(chan struct{}),
		counts:      map[string]int{},
		delegations: map[string]bool{},
		done:        make(chan struct{}),
	}
	scriptDone := make(chan struct{})
	go func() {
		defer close(scriptDone)
		sess.runScript(s.script)
	}()
	sess.readLoop(recordWrites)
	sess.finish()
	<-scriptDone
}

// finish ends the connection once: it stops the script and closes the socket.
func (sess *session) finish() {
	sess.doneOnce.Do(func() {
		close(sess.done)
		if err := sess.conn.Close(); err != nil {
			sess.server.recordError(err)
		}
	})
}

func (sess *session) runScript(steps []Step) {
	for _, step := range steps {
		if err := step.run(sess); err != nil {
			if !errors.Is(err, errSessionDone) && !sess.isDone() {
				sess.server.recordError(fmt.Errorf("fakelive script step %s: %w", step.name, err))
			}
			return
		}
	}
}

func (sess *session) readLoop(recordWrites bool) {
	for {
		messageType, payload, err := sess.conn.ReadMessage()
		if err != nil {
			return
		}
		if recordWrites {
			sess.server.recordWrite(messageType, payload)
		}
		if sess.handleFrame(payload) {
			return
		}
	}
}

// handleFrame processes one client frame and reports whether the
// connection is finished.
func (sess *session) handleFrame(payload []byte) bool {
	event, err := openailive.DecodeClientEvent(payload)
	if err != nil {
		sess.sendError(CodeMalformedEvent, err.Error(), "", "")
		return false
	}
	sess.server.recordEvent(event)
	sess.count(event.EventType())
	if !sess.isStarted() {
		sess.handleStart(event, payload)
		return false
	}
	return sess.handleCommand(event, payload)
}

func (sess *session) handleStart(event openailive.Event, payload []byte) {
	start, ok := event.(openailive.SessionStart)
	if !ok {
		sess.sendError(CodeSessionStartRequired, "send session.start before any other event", "type", eventID(payload))
		return
	}
	if failure := validateStart(payload, start); failure != nil {
		sess.sendError(failure.code, failure.message, failure.param, start.EventID)
		return
	}
	resource := resolveSession(start.Session)
	sess.mu.Lock()
	sess.resource = resource
	sess.format = *resource.Audio.Format
	sess.mu.Unlock()
	sess.send(openailive.SessionStarted{EventID: sess.newID(), ClientEventID: start.EventID, Session: resource})
	// Steps waiting for the start run only after session.started is written.
	sess.mu.Lock()
	sess.started = true
	sess.broadcastLocked()
	sess.mu.Unlock()
}

func (sess *session) handleCommand(event openailive.Event, payload []byte) bool {
	switch command := event.(type) {
	case openailive.SessionStart:
		sess.sendError(CodeSessionAlreadyStarted, "session.start was already accepted", "type", command.EventID)
	case openailive.InputAudioAppend:
		sess.handleAudio(command)
	case openailive.InputAudioMute:
		sess.ack(event.EventType(), openailive.InputAudioMuted{EventID: sess.newID(), ClientEventID: command.EventID})
	case openailive.InputAudioUnmute:
		sess.ack(event.EventType(), openailive.InputAudioUnmuted{EventID: sess.newID(), ClientEventID: command.EventID})
	case openailive.InstructionsAppend:
		sess.handleAppend(payload, event.EventType(), openailive.ContextAppend(command), func(ack openailive.TimelineAck) openailive.Event {
			return openailive.InstructionsAppended(ack)
		})
	case openailive.ThinkingAppend:
		sess.handleAppend(payload, event.EventType(), openailive.ContextAppend(command), func(ack openailive.TimelineAck) openailive.Event {
			return openailive.ThinkingAppended(ack)
		})
	case openailive.CommentaryAppend:
		sess.handleAppend(payload, event.EventType(), openailive.ContextAppend(command), func(ack openailive.TimelineAck) openailive.Event {
			return openailive.CommentaryAppended(ack)
		})
	case openailive.SessionUpdate:
		sess.handleUpdate(command)
	case openailive.SessionClose:
		return sess.handleClose(command)
	case openailive.ResponseItemCreate, openailive.ResponseCreate:
		// Recorded only: the protocol has no acknowledgement for either.
	default:
		sess.sendError(CodeUnknownEventType, fmt.Sprintf("unknown client event %q", event.EventType()), "type", eventID(payload))
	}
	return false
}

func (sess *session) handleAudio(command openailive.InputAudioAppend) {
	audio, err := command.Bytes()
	if err != nil {
		sess.sendError(openailive.CodeInvalidAudio, "audio must be base64", "audio", command.EventID)
		return
	}
	sess.mu.Lock()
	format := sess.format
	sess.mu.Unlock()
	if format.Type == openailive.AudioTypePCM && len(audio)%2 != 0 {
		sess.sendError(openailive.CodeInvalidAudio, "PCM16 audio must contain an even number of bytes", "audio", command.EventID)
		return
	}
	sess.server.recordAudio(audio)
	sess.mu.Lock()
	sess.inputBytes += int64(len(audio))
	sess.mu.Unlock()
}

func (sess *session) handleAppend(payload []byte, commandType string, command openailive.ContextAppend, ack func(openailive.TimelineAck) openailive.Event) {
	if !hasKey(payload, "delegation_id") {
		sess.sendError(CodeMissingRequiredParameter, "delegation_id is required; send null for session context", "delegation_id", command.EventID)
		return
	}
	if command.DelegationID != nil && !sess.knownDelegation(*command.DelegationID) {
		sess.sendError(CodeUnknownDelegation, fmt.Sprintf("unknown client delegation %q", *command.DelegationID), "delegation_id", command.EventID)
		return
	}
	start := sess.timelineMS()
	event := ack(openailive.TimelineAck{
		EventID:       sess.newID(),
		ClientEventID: command.EventID,
		StartMS:       start,
		EndMS:         start + DefaultAckSpan.Milliseconds(),
	})
	sess.ack(commandType, event)
}

func (sess *session) handleUpdate(command openailive.SessionUpdate) {
	sess.mu.Lock()
	current := sess.resource.Delegation
	patch := command.Session.Delegation
	if patch != nil && patch.Type != current.Type {
		sess.mu.Unlock()
		sess.sendError(openailive.CodeImmutableFieldUpdate, "The delegation type cannot change after session startup.", "session.delegation.type", command.EventID)
		return
	}
	if patch != nil && patch.Responses != nil && current.Responses != nil {
		merged := mergeResponses(*current.Responses, *patch.Responses)
		sess.resource.Delegation = &openailive.Delegation{Type: current.Type, Responses: &merged}
	}
	resource := sess.resource
	sess.mu.Unlock()
	sess.send(openailive.SessionUpdated{EventID: sess.newID(), ClientEventID: command.EventID, Session: resource})
}

func (sess *session) handleClose(command openailive.SessionClose) bool {
	if sess.server.dropOnClose {
		return true
	}
	sess.closeWith(openailive.CloseReasonCloseRequested, command.EventID)
	return true
}

// closeWith sends session.closed with reason and the final usage, then
// closes the connection.
func (sess *session) closeWith(reason, clientEventID string) {
	sess.mu.Lock()
	resource := sess.resource
	sess.mu.Unlock()
	sess.send(openailive.SessionClosed{
		EventID:       sess.newID(),
		ClientEventID: clientEventID,
		Reason:        reason,
		Session:       resource,
		Usage:         openailive.Usage{Seconds: float64(sess.timelineMS()) / millisPerSecond},
	})
	sess.finish()
}

func (sess *session) ack(commandType string, event openailive.Event) {
	if sess.server.withheld[commandType] {
		return
	}
	sess.send(event)
}

func (sess *session) sendError(code, message, param, clientEventID string) {
	body := openailive.Error{Type: openailive.ErrorTypeInvalidRequest, Code: code, Message: message, ClientEventID: clientEventID}
	if param != "" {
		body.Param = &param
	}
	sess.send(openailive.ErrorEvent{EventID: sess.newID(), Error: body})
}

// send encodes and writes one server event. A write to a connection that is
// already gone is not a fake failure.
func (sess *session) send(event openailive.Event) {
	if err := sess.write(event); err != nil && !sess.isDone() {
		sess.server.recordError(err)
	}
}

func (sess *session) write(event openailive.Event) error {
	frame, err := openailive.EncodeEvent(event)
	if err != nil {
		return err
	}
	if created, ok := event.(openailive.DelegationCreated); ok && created.Delegation.Target == openailive.DelegationClient {
		sess.mu.Lock()
		sess.delegations[created.Delegation.ID] = true
		sess.mu.Unlock()
	}
	sess.writeMu.Lock()
	defer sess.writeMu.Unlock()
	return sess.conn.WriteMessage(websocket.TextMessage, frame)
}

func (sess *session) writeRaw(messageType int, payload []byte) error {
	sess.writeMu.Lock()
	defer sess.writeMu.Unlock()
	return sess.conn.WriteMessage(messageType, payload)
}

func (sess *session) isDone() bool {
	select {
	case <-sess.done:
		return true
	default:
		return false
	}
}

func (sess *session) isStarted() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.started
}

func (sess *session) knownDelegation(id string) bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.delegations[id]
}

func (sess *session) count(eventType string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.counts[eventType]++
	sess.broadcastLocked()
}

// broadcastLocked wakes every step waiting for a state change.
func (sess *session) broadcastLocked() {
	close(sess.changed)
	sess.changed = make(chan struct{})
}

// waitFor blocks until ready (called with mu held) is true or the
// connection ends.
func (sess *session) waitFor(ready func() bool) error {
	for {
		sess.mu.Lock()
		if ready() {
			sess.mu.Unlock()
			return nil
		}
		changed := sess.changed
		sess.mu.Unlock()
		select {
		case <-changed:
		case <-sess.done:
			return errSessionDone
		}
	}
}

// timelineMS is the input audio received so far, in milliseconds.
func (sess *session) timelineMS() int64 {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	bytesPerSecond := int64(sess.format.Rate)
	if sess.format.Type == openailive.AudioTypePCM {
		bytesPerSecond *= 2
	}
	if bytesPerSecond == 0 {
		return 0
	}
	return sess.inputBytes * millisPerSecond / bytesPerSecond
}

func (sess *session) newID() string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.nextID++
	return fmt.Sprintf("evt_fake_%d", sess.nextID)
}

func mergeResponses(current, patch openailive.ResponsesDelegationConfig) openailive.ResponsesDelegationConfig {
	if patch.Model != "" {
		current.Model = patch.Model
	}
	if patch.Instructions != nil {
		current.Instructions = patch.Instructions
	}
	if patch.MaxOutputTokens != nil {
		current.MaxOutputTokens = patch.MaxOutputTokens
	}
	if patch.ParallelToolCalls != nil {
		current.ParallelToolCalls = patch.ParallelToolCalls
	}
	if patch.Reasoning != nil {
		current.Reasoning = patch.Reasoning
	}
	if patch.ServiceTier != "" {
		current.ServiceTier = patch.ServiceTier
	}
	if patch.Text != nil {
		current.Text = patch.Text
	}
	if patch.ToolChoice != nil {
		current.ToolChoice = patch.ToolChoice
	}
	if patch.Tools != nil {
		current.Tools = patch.Tools
	}
	return current
}

func eventID(payload []byte) string {
	var head struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(payload, &head); err != nil {
		return ""
	}
	return head.EventID
}

func hasKey(payload []byte, key string) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return false
	}
	_, ok := fields[key]
	return ok
}
