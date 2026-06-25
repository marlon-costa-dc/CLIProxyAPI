package quota

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// Manager coordinates pause state management, middleware, and background cleanup.
type Manager struct {
	store  *Store
	config atomic.Value // holds QuotaConfig
	paused atomic.Value // holds map[string]PauseEntry
	mu     sync.Mutex

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewManager creates a new quota manager.
func NewManager(cfg QuotaConfig) (*Manager, error) {
	dbPath := cfg.DBPath
	if dbPath == "" {
		dbPath = ":memory:"
	}

	store, err := NewStore(dbPath)
	if err != nil {
		return nil, err
	}

	m := &Manager{
		store:  store,
		stopCh: make(chan struct{}),
	}
	m.config.Store(cfg)
	if err := m.refreshPausedSnapshot(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return m, nil
}

func (m *Manager) refreshPausedSnapshot() error {
	entries, err := m.store.ListPaused()
	if err != nil {
		return err
	}
	m.storePausedSnapshot(entries)
	return nil
}

func (m *Manager) storePausedSnapshot(entries []PauseEntry) {
	paused := make(map[string]PauseEntry, len(entries))
	for _, entry := range entries {
		if entry.IsExpired() {
			continue
		}
		paused[entry.KeyHash] = entry
	}
	m.paused.Store(paused)
}

func (m *Manager) pausedSnapshot() map[string]PauseEntry {
	if m == nil {
		return nil
	}
	value := m.paused.Load()
	if value == nil {
		return nil
	}
	paused, _ := value.(map[string]PauseEntry)
	return paused
}

func (m *Manager) updatePausedSnapshot(update func(map[string]PauseEntry)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.pausedSnapshot()
	next := make(map[string]PauseEntry, len(current)+1)
	for key, entry := range current {
		next[key] = entry
	}
	update(next)
	m.paused.Store(next)
}

// Start launches the background cleanup goroutine.
func (m *Manager) Start() error {
	log.Info("quota: manager started")

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if n, err := m.store.CleanupExpired(); err != nil {
					log.Errorf("quota: cleanup error: %v", err)
				} else {
					if n > 0 {
						log.Debugf("quota: cleaned %d expired entries", n)
					}
					if err := m.refreshPausedSnapshot(); err != nil {
						log.Errorf("quota: refresh pause snapshot error: %v", err)
					}
				}
			case <-m.stopCh:
				return
			}
		}
	}()
	return nil
}

// Stop shuts down the background goroutine and closes the store.
func (m *Manager) Stop() {
	close(m.stopCh)
	m.wg.Wait()
	if m.store != nil {
		m.store.Close()
	}
}

// UpdateConfig hot-reloads the quota configuration.
func (m *Manager) UpdateConfig(cfg QuotaConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.Store(cfg)
}

// EnforcerMiddleware returns the Gin middleware that blocks paused keys.
func (m *Manager) EnforcerMiddleware() gin.HandlerFunc {
	return EnforcerMiddleware(m)
}

// PauseKey creates a pause entry.
func (m *Manager) PauseKey(keyHash, reason string, expiresAt time.Time) error {
	now := time.Now()
	entry := PauseEntry{
		KeyHash:   keyHash,
		Reason:    reason,
		PausedAt:  now,
		ExpiresAt: expiresAt,
		CreatedAt: now,
	}
	if err := m.store.PauseKey(entry); err != nil {
		return err
	}
	m.updatePausedSnapshot(func(paused map[string]PauseEntry) {
		if entry.IsExpired() {
			delete(paused, keyHash)
			return
		}
		paused[keyHash] = entry
	})
	return nil
}

// ResumeKey removes a pause entry.
func (m *Manager) ResumeKey(keyHash string) error {
	if err := m.store.ResumeKey(keyHash); err != nil {
		return err
	}
	m.updatePausedSnapshot(func(paused map[string]PauseEntry) {
		delete(paused, keyHash)
	})
	return nil
}

// IsPaused checks whether a key is paused.
func (m *Manager) IsPaused(keyHash string) (bool, *PauseEntry, error) {
	entry, ok := m.pausedSnapshot()[keyHash]
	if !ok || entry.IsExpired() {
		return false, nil, nil
	}
	return true, &entry, nil
}

// ListPaused returns all non-expired pause entries.
func (m *Manager) ListPaused() ([]PauseEntry, error) {
	entries, err := m.store.ListPaused()
	if err != nil {
		return nil, err
	}
	m.storePausedSnapshot(entries)
	return entries, nil
}

// Store returns the underlying store (for management API access).
func (m *Manager) Store() *Store {
	return m.store
}

// Config returns the current quota config.
func (m *Manager) Config() QuotaConfig {
	return m.config.Load().(QuotaConfig)
}
