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
	return m, nil
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
				} else if n > 0 {
					log.Debugf("quota: cleaned %d expired entries", n)
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
	return EnforcerMiddleware(m.store)
}

// PauseKey creates a pause entry.
func (m *Manager) PauseKey(keyHash, reason string, expiresAt time.Time) error {
	entry := PauseEntry{
		KeyHash:   keyHash,
		Reason:    reason,
		PausedAt:  time.Now(),
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
	}
	return m.store.PauseKey(entry)
}

// ResumeKey removes a pause entry.
func (m *Manager) ResumeKey(keyHash string) error {
	return m.store.ResumeKey(keyHash)
}

// IsPaused checks whether a key is paused.
func (m *Manager) IsPaused(keyHash string) (bool, *PauseEntry, error) {
	return m.store.IsPaused(keyHash)
}

// ListPaused returns all non-expired pause entries.
func (m *Manager) ListPaused() ([]PauseEntry, error) {
	return m.store.ListPaused()
}

// Store returns the underlying store (for management API access).
func (m *Manager) Store() *Store {
	return m.store
}

// Config returns the current quota config.
func (m *Manager) Config() QuotaConfig {
	return m.config.Load().(QuotaConfig)
}