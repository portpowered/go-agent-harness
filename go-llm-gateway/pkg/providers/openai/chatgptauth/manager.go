package chatgptauth

import (
	"context"
	"errors"
	"fmt"
)

// Manager hands out a fresh credential from a FileStore, refreshing it
// through a Client when it is about to expire. It is safe across processes:
// refresh runs under the store lock and re-reads the file first, so a token
// another process already rotated is used instead of refreshed again.
type Manager struct {
	store  *FileStore
	client *Client
}

// NewManager returns a Manager over store that refreshes through client.
func NewManager(store *FileStore, client *Client) *Manager {
	return &Manager{store: store, client: client}
}

// Credential returns a credential whose access token is valid now. A
// permanent refresh failure returns an error matching ErrReauthRequired; a
// transient one returns the stored credential while its access token is
// still unexpired, and the error otherwise.
func (m *Manager) Credential(ctx context.Context) (Credential, error) {
	now := m.client.cfg.Now()
	cred, err := m.store.Load()
	if err != nil || !cred.NeedsRefresh(now) {
		return cred, err
	}
	lock, err := m.store.Lock(ctx)
	if err != nil {
		return Credential{}, err
	}
	cred, err = m.refreshLocked(ctx)
	return cred, errors.Join(err, lock.Release())
}

func (m *Manager) refreshLocked(ctx context.Context) (Credential, error) {
	cred, err := m.store.Load()
	if err != nil {
		return Credential{}, err
	}
	now := m.client.cfg.Now()
	if !cred.NeedsRefresh(now) {
		return cred, nil
	}
	refreshed, err := m.client.Refresh(ctx, cred)
	if err != nil {
		if !errors.Is(err, ErrReauthRequired) && cred.AccessToken != "" && !cred.Expired(now) {
			return cred, nil
		}
		return Credential{}, err
	}
	if err := m.store.Save(refreshed); err != nil {
		return Credential{}, fmt.Errorf("save refreshed ChatGPT credential: %w", err)
	}
	return refreshed, nil
}
