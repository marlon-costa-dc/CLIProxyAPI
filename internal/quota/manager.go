package quota

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

const automaticPauseReason = "spend_limit_exceeded"

// Manager coordinates quota state management, middleware, and background cleanup.
type Manager struct {
	store      *Store
	config     atomic.Value // holds QuotaConfig
	paused     atomic.Value // holds map[string]PauseEntry
	downgraded atomic.Value // holds map[string]DowngradeEntry
	mu         sync.Mutex

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
	if err := m.refreshDowngradedSnapshot(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return m, nil
}

func (m *Manager) refreshPausedSnapshot() error {
	m.mu.Lock()
	defer m.mu.Unlock()
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

func (m *Manager) refreshDowngradedSnapshot() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := m.store.ListDowngraded()
	if err != nil {
		return err
	}
	m.storeDowngradedSnapshot(entries)
	return nil
}

func (m *Manager) storeDowngradedSnapshot(entries []DowngradeEntry) {
	downgraded := make(map[string]DowngradeEntry, len(entries))
	for _, entry := range entries {
		if entry.IsExpired() {
			continue
		}
		downgraded[entry.KeyHash] = entry
	}
	m.downgraded.Store(downgraded)
}

func (m *Manager) downgradedSnapshot() map[string]DowngradeEntry {
	if m == nil {
		return nil
	}
	value := m.downgraded.Load()
	if value == nil {
		return nil
	}
	downgraded, _ := value.(map[string]DowngradeEntry)
	return downgraded
}

// updatePausedSnapshotLocked updates the immutable pause snapshot while m.mu is held.
func (m *Manager) updatePausedSnapshotLocked(update func(map[string]PauseEntry)) {
	current := m.pausedSnapshot()
	next := make(map[string]PauseEntry, len(current)+1)
	for key, entry := range current {
		next[key] = entry
	}
	update(next)
	m.paused.Store(next)
}

// updateDowngradedSnapshotLocked updates the immutable downgrade snapshot while m.mu is held.
func (m *Manager) updateDowngradedSnapshotLocked(update func(map[string]DowngradeEntry)) {
	current := m.downgradedSnapshot()
	next := make(map[string]DowngradeEntry, len(current)+1)
	for key, entry := range current {
		next[key] = entry
	}
	update(next)
	m.downgraded.Store(next)
}

// Start launches the background cleanup goroutine.
func (m *Manager) Start() error {
	log.Info("quota: manager started")

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		// Expired quota states are removed within one minute.
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				pauseCount, pauseErr := m.store.CleanupExpired()
				if pauseErr != nil {
					log.Errorf("quota: cleanup pause entries error: %v", pauseErr)
				}
				downgradeCount, downgradeErr := m.store.CleanupExpiredDowngrades()
				if downgradeErr != nil {
					log.Errorf("quota: cleanup downgrade entries error: %v", downgradeErr)
				}
				if pauseErr == nil && downgradeErr == nil && (pauseCount > 0 || downgradeCount > 0) {
					if err := m.refreshPausedSnapshot(); err != nil {
						log.Errorf("quota: refresh pause snapshot error: %v", err)
					}
					if err := m.refreshDowngradedSnapshot(); err != nil {
						log.Errorf("quota: refresh downgrade snapshot error: %v", err)
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

// UpdateConfig hot-reloads the local quota configuration.
// Usage-service states are changed only by its explicit resume instructions.
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
	m.mu.Lock()
	defer m.mu.Unlock()

	if reason == automaticPauseReason {
		// Usage-service owns spend decisions, so local quota settings do not reject automatic pauses.
		if entry, ok := m.pausedSnapshot()[keyHash]; ok && !entry.IsExpired() && entry.Reason != automaticPauseReason {
			return nil
		}
	}
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
	m.updatePausedSnapshotLocked(func(paused map[string]PauseEntry) {
		if entry.IsExpired() {
			delete(paused, keyHash)
			return
		}
		paused[keyHash] = entry
	})
	return nil
}

// DowngradeKey creates or refreshes a fallback-model state for one API key.
func (m *Manager) DowngradeKey(keyHash, reason, fallbackModel string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, err := m.store.UpsertDowngrade(DowngradeEntry{
		KeyHash:       keyHash,
		Reason:        reason,
		FallbackModel: fallbackModel,
		DowngradedAt:  time.Now(),
		ExpiresAt:     expiresAt,
	})
	if err != nil {
		return err
	}
	m.updateDowngradedSnapshotLocked(func(downgraded map[string]DowngradeEntry) {
		if entry.IsExpired() {
			delete(downgraded, keyHash)
			return
		}
		downgraded[keyHash] = entry
	})
	return nil
}

// ResumeDowngradeKey removes a fallback-model state without a reason condition.
func (m *Manager) ResumeDowngradeKey(keyHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.ResumeDowngrade(keyHash); err != nil {
		return err
	}
	m.updateDowngradedSnapshotLocked(func(downgraded map[string]DowngradeEntry) {
		delete(downgraded, keyHash)
	})
	return nil
}

// ResumeDowngradeKeyIfReason removes a fallback-model state only when its reason matches.
func (m *Manager) ResumeDowngradeKeyIfReason(keyHash, expectedReason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	deleted, err := m.store.ResumeDowngradeIfReason(keyHash, expectedReason)
	if err != nil {
		return err
	}
	if deleted {
		m.updateDowngradedSnapshotLocked(func(downgraded map[string]DowngradeEntry) {
			delete(downgraded, keyHash)
		})
	}
	return nil
}

// ResumeKey removes a pause entry without a reason condition for existing management callers.
func (m *Manager) ResumeKey(keyHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.ResumeKey(keyHash); err != nil {
		return err
	}
	m.updatePausedSnapshotLocked(func(paused map[string]PauseEntry) {
		delete(paused, keyHash)
	})
	return nil
}

// ResumeKeyIfReason resumes only a matching reason, protecting manual pause replacements.
func (m *Manager) ResumeKeyIfReason(keyHash, expectedReason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	deleted, err := m.store.ResumeKeyIfReason(keyHash, expectedReason)
	if err != nil {
		return err
	}
	if deleted {
		m.updatePausedSnapshotLocked(func(paused map[string]PauseEntry) {
			delete(paused, keyHash)
		})
	}
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
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := m.store.ListPaused()
	if err != nil {
		return nil, err
	}
	m.storePausedSnapshot(entries)
	return entries, nil
}

// IsDowngraded checks whether a key has a current fallback-model state.
func (m *Manager) IsDowngraded(keyHash string) (bool, *DowngradeEntry, error) {
	entry, ok := m.downgradedSnapshot()[keyHash]
	if !ok || entry.IsExpired() {
		return false, nil, nil
	}
	return true, &entry, nil
}

// ListDowngraded returns all non-expired fallback-model states.
func (m *Manager) ListDowngraded() ([]DowngradeEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := m.store.ListDowngraded()
	if err != nil {
		return nil, err
	}
	m.storeDowngradedSnapshot(entries)
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
