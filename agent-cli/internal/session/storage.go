package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	sessionDirName = "sessions"
	sessionPrefix  = "session-"
	sessionSuffix  = ".json"
	tracePrefix    = "trace-"
	traceSuffix    = ".json"
)

const (
	// DefaultSessionListLimit is the default number of sessions returned by a
	// bounded metadata query.
	DefaultSessionListLimit = 100
	// MaxSessionListLimit is the largest number of sessions returned by a
	// bounded metadata query.
	MaxSessionListLimit = 1000
)

// storedContentPart is the JSON-friendly shape for one content part (type discriminator + fields).
// Used so we can unmarshal session JSON into concrete types; messages.ContentPart is an interface.
type storedContentPart struct {
	Type      string `json:"type"`
	Bytes     []byte `json:"bytes,omitempty"`
	Text      string `json:"text,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	URL       string `json:"url,omitempty"`
	Name      string `json:"name,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	Usage     *struct {
		PromptTokens     int `json:"promptTokens"`
		CompletionTokens int `json:"completionTokens"`
		TotalTokens      int `json:"totalTokens"`
		ReasoningTokens  int `json:"reasoningTokens,omitempty"`
	} `json:"usage,omitempty"`
}

// storedMessage is the JSON structure for one message in a session file.
type storedMessage struct {
	Role         string              `json:"role"`
	ContentParts []storedContentPart `json:"contentParts,omitempty"`
	ToolCalls    []storedToolCall    `json:"toolCalls,omitempty"`
	ToolCallID   string              `json:"toolCallId,omitempty"`
	Name         string              `json:"name,omitempty"`
}

type storedToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// StoredSession is the JSON structure persisted for a session.
type StoredSession struct {
	ID       string          `json:"id"`
	Messages []storedMessage `json:"messages"`
}

// Storage loads and saves session conversation history.
type Storage struct {
	sessionsDir string
}

// NewStorage creates a Storage using the workspace directory (e.g. ~/.agent-cli).
// Sessions are stored in <workspace>/sessions/.
func NewStorage(workspaceDir string) *Storage {
	return &Storage{
		sessionsDir: filepath.Join(workspaceDir, sessionDirName),
	}
}

// WorkspaceDir returns the workspace root directory (e.g. for resolving AGENTS.md).
func (s *Storage) WorkspaceDir() string {
	return filepath.Dir(s.sessionsDir)
}

// Load loads a session by ID and returns its conversation as agent loop messages.
// Returns nil, nil if the session file does not exist.
func (s *Storage) Load(sessionID string) ([]messages.Message, error) {
	path := s.sessionPath(sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session %s: %w", sessionID, err)
	}
	var stored StoredSession
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("parse session %s: %w", sessionID, err)
	}
	msgs, err := storedToMessages(stored.Messages)
	if err != nil {
		return nil, fmt.Errorf("session %s: %w", sessionID, err)
	}
	return msgs, nil
}

// Save persists a session with the given messages.
func (s *Storage) Save(sessionID string, msgs []messages.Message) error {
	if err := os.MkdirAll(s.sessionsDir, 0755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}
	stored := StoredSession{
		ID:       sessionID,
		Messages: messagesToStored(msgs),
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	path := s.sessionPath(sessionID)
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		return fmt.Errorf("write session %s: %w", sessionID, &os.PathError{Op: "open", Path: path, Err: syscall.EISDIR})
	}
	tmp, err := os.CreateTemp(s.sessionsDir, sessionPrefix+".tmp-*")
	if err != nil {
		return fmt.Errorf("write session %s: %w", sessionID, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write session %s: %w", sessionID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write session %s: %w", sessionID, err)
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		return fmt.Errorf("write session %s: %w", sessionID, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("write session %s: %w", sessionID, err)
	}
	tmpName = ""
	return nil
}

// NewSessionID returns a new unique session ID. Use when starting a fresh session.
func (s *Storage) NewSessionID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// SessionInfo holds session ID and file mod time for listing.
type SessionInfo struct {
	ID      string
	ModTime time.Time
}

// SessionListOptions controls a metadata-only session query. A zero Limit
// uses DefaultSessionListLimit. Since, when non-nil, includes sessions whose
// file modification time is equal to or newer than the supplied instant.
// Filter is a literal, case-insensitive substring matched against the ID.
type SessionListOptions struct {
	Limit  int
	Since  *time.Time
	Filter string
}

// List returns all sessions with mod time, sorted by mod time descending.
func (s *Storage) List() ([]SessionInfo, error) {
	return s.listSessionMetadata(SessionListOptions{}, false)
}

// ListWithOptions returns a bounded session metadata query, sorted newest
// first. It never loads session message bodies.
func (s *Storage) ListWithOptions(options SessionListOptions) ([]SessionInfo, error) {
	if options.Limit == 0 {
		options.Limit = DefaultSessionListLimit
	}
	if options.Limit < 1 || options.Limit > MaxSessionListLimit {
		return nil, fmt.Errorf("session list limit must be between 1 and %d", MaxSessionListLimit)
	}
	return s.listSessionMetadata(options, true)
}

func (s *Storage) listSessionMetadata(options SessionListOptions, bounded bool) ([]SessionInfo, error) {
	entries, err := os.ReadDir(s.sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var infos []SessionInfo
	filter := strings.ToLower(options.Filter)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, sessionPrefix) || !strings.HasSuffix(name, sessionSuffix) {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, sessionPrefix), sessionSuffix)
		if id == "" {
			continue
		}
		path := filepath.Join(s.sessionsDir, name)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		modTime := info.ModTime()
		if options.Since != nil && modTime.Before(*options.Since) {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(id), filter) {
			continue
		}
		infos = append(infos, SessionInfo{ID: id, ModTime: modTime})
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].ModTime.Equal(infos[j].ModTime) {
			return infos[i].ID < infos[j].ID
		}
		return infos[i].ModTime.After(infos[j].ModTime)
	})
	if bounded && len(infos) > options.Limit {
		infos = infos[:options.Limit]
	}
	return infos, nil
}

// Delete removes a session file. Returns error if the file does not exist or cannot be removed.
func (s *Storage) Delete(sessionID string) error {
	path := s.sessionPath(sessionID)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("session %s not found", sessionID)
		}
		return fmt.Errorf("delete session %s: %w", sessionID, err)
	}
	return nil
}

// Latest returns the most recent session ID by modification time, or empty string if none.
func (s *Storage) Latest() (string, error) {
	infos, err := s.List()
	if err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return "", nil
	}
	return infos[0].ID, nil
}

// NewTraceID returns a new unique trace ID. Use when starting a fresh iterative loop.
func (s *Storage) NewTraceID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// SaveTrace persists a trace record to trace-<id>.json in the sessions directory.
func (s *Storage) SaveTrace(trace TraceRecord) error {
	if err := os.MkdirAll(s.sessionsDir, 0755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}
	data, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal trace: %w", err)
	}
	path := s.tracePath(trace.TraceID)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write trace %s: %w", trace.TraceID, err)
	}
	return nil
}

// LoadTrace loads a trace record by ID.
// Returns nil, nil if the trace file does not exist.
func (s *Storage) LoadTrace(traceID string) (*TraceRecord, error) {
	path := s.tracePath(traceID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read trace %s: %w", traceID, err)
	}
	var trace TraceRecord
	if err := json.Unmarshal(data, &trace); err != nil {
		return nil, fmt.Errorf("parse trace %s: %w", traceID, err)
	}
	return &trace, nil
}

// ListTraces returns all trace IDs with mod time, sorted by mod time descending.
func (s *Storage) ListTraces() ([]TraceInfo, error) {
	entries, err := os.ReadDir(s.sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list traces: %w", err)
	}
	var infos []TraceInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, tracePrefix) || !strings.HasSuffix(name, traceSuffix) {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, tracePrefix), traceSuffix)
		if id == "" {
			continue
		}
		path := filepath.Join(s.sessionsDir, name)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		infos = append(infos, TraceInfo{ID: id, ModTime: info.ModTime()})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ModTime.After(infos[j].ModTime) })
	return infos, nil
}

func (s *Storage) tracePath(traceID string) string {
	safe := strings.ReplaceAll(traceID, string(filepath.Separator), "-")
	return filepath.Join(s.sessionsDir, tracePrefix+safe+traceSuffix)
}

func (s *Storage) sessionPath(sessionID string) string {
	safe := strings.ReplaceAll(sessionID, string(filepath.Separator), "-")
	return filepath.Join(s.sessionsDir, sessionPrefix+safe+sessionSuffix)
}

func storedToMessages(stored []storedMessage) ([]messages.Message, error) {
	out := make([]messages.Message, 0, len(stored))
	for _, m := range stored {
		parts, err := storedContentPartsToParts(m.ContentParts)
		if err != nil {
			return nil, err
		}
		toolCalls := make([]messages.ToolCall, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			toolCalls = append(toolCalls, messages.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		out = append(out, messages.Message{
			Role:         messages.Role(m.Role),
			ContentParts: parts,
			ToolCalls:    toolCalls,
			ToolCallID:   m.ToolCallID,
			Name:         m.Name,
		})
	}
	return out, nil
}

func storedContentPartsToParts(stored []storedContentPart) ([]messages.ContentPart, error) {
	out := make([]messages.ContentPart, 0, len(stored))
	for _, p := range stored {
		switch p.Type {
		case "text":
			out = append(out, messages.TextPart{Text: p.Text})
		case "image":
			out = append(out, messages.ImagePart{MediaType: p.MediaType, URL: p.URL})
		case "audio":
			out = append(out, messages.AudioPart{MediaType: p.MediaType, URL: p.URL})
		case "video":
			out = append(out, messages.VideoPart{MediaType: p.MediaType, URL: p.URL})
		case "file":
			out = append(out, messages.FilePart{MediaType: p.MediaType, URL: p.URL, Name: p.Name})
		case "reasoning":
			out = append(out, messages.ReasoningPart{Reasoning: p.Reasoning})
		case "usage_info":
			if p.Usage == nil {
				out = append(out, messages.UsageInfoPart{})
			} else {
				out = append(out, messages.UsageInfoPart{
					Usage: messages.TokenUsage{
						PromptTokens:     p.Usage.PromptTokens,
						CompletionTokens: p.Usage.CompletionTokens,
						TotalTokens:      p.Usage.TotalTokens,
						ReasoningTokens:  p.Usage.ReasoningTokens,
					},
				})
			}
		default:
			return nil, fmt.Errorf("unknown content part type %q", p.Type)
		}
	}
	return out, nil
}

func messagesToStored(msgs []messages.Message) []storedMessage {
	out := make([]storedMessage, 0, len(msgs))
	for _, m := range msgs {
		parts := make([]storedContentPart, 0, len(m.ContentParts))
		for _, p := range m.ContentParts {
			parts = append(parts, contentPartToStored(p))
		}
		toolCalls := make([]storedToolCall, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			toolCalls = append(toolCalls, storedToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		out = append(out, storedMessage{
			Role:         string(m.Role),
			ContentParts: parts,
			ToolCalls:    toolCalls,
			ToolCallID:   m.ToolCallID,
			Name:         m.Name,
		})
	}
	return out
}

func contentPartToStored(p messages.ContentPart) storedContentPart {
	switch cp := p.(type) {
	case messages.TextPart:
		return storedContentPart{Type: "text", Text: cp.Text}
	case messages.ImagePart:
		return storedContentPart{Type: "image", MediaType: cp.MediaType, Bytes: cp.Bytes, URL: cp.URL}
	case messages.AudioPart:
		return storedContentPart{Type: "audio", MediaType: cp.MediaType, Bytes: cp.Bytes, URL: cp.URL}
	case messages.VideoPart:
		return storedContentPart{Type: "video", MediaType: cp.MediaType, Bytes: cp.Bytes, URL: cp.URL}
	case messages.FilePart:
		return storedContentPart{Type: "file", MediaType: cp.MediaType, Bytes: cp.Bytes, URL: cp.URL, Name: cp.Name}
	case messages.ReasoningPart:
		return storedContentPart{Type: "reasoning", Reasoning: cp.Reasoning}
	case messages.UsageInfoPart:
		u := cp.Usage
		var usage *struct {
			PromptTokens     int `json:"promptTokens"`
			CompletionTokens int `json:"completionTokens"`
			TotalTokens      int `json:"totalTokens"`
			ReasoningTokens  int `json:"reasoningTokens,omitempty"`
		}
		if u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0 || u.ReasoningTokens != 0 {
			usage = &struct {
				PromptTokens     int `json:"promptTokens"`
				CompletionTokens int `json:"completionTokens"`
				TotalTokens      int `json:"totalTokens"`
				ReasoningTokens  int `json:"reasoningTokens,omitempty"`
			}{u.PromptTokens, u.CompletionTokens, u.TotalTokens, u.ReasoningTokens}
		}
		return storedContentPart{Type: "usage_info", Usage: usage}
	default:
		// Unknown type (e.g. ControlPlanePart); persist as text placeholder so round-trip doesn't lose the slot
		return storedContentPart{Type: "text", Text: ""}
	}
}
