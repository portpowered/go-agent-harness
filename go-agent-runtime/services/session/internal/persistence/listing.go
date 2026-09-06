package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

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
	for _, entry := range entries {
		if info, ok := s.sessionMetadata(entry, options.Since, filter); ok {
			infos = append(infos, info)
		}
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

func (s *Storage) sessionMetadata(entry os.DirEntry, since *time.Time, filter string) (SessionInfo, bool) {
	name := entry.Name()
	if entry.IsDir() || !strings.HasPrefix(name, sessionPrefix) || !strings.HasSuffix(name, sessionSuffix) {
		return SessionInfo{}, false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, sessionPrefix), sessionSuffix)
	if id == "" || (filter != "" && !strings.Contains(strings.ToLower(id), filter)) {
		return SessionInfo{}, false
	}
	info, err := os.Stat(filepath.Join(s.sessionsDir, name))
	if err != nil || (since != nil && info.ModTime().Before(*since)) {
		return SessionInfo{}, false
	}
	return SessionInfo{ID: id, ModTime: info.ModTime()}, true
}
