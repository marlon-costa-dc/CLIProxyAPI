package quota

import (
	"path/filepath"
	"testing"
	"time"
)

func TestManager_PauseResume(t *testing.T) {
	m, err := NewManager(QuotaConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer m.Stop()

	hash := "test-hash-1"
	if err := m.PauseKey(hash, "over limit", time.Now().Add(1*time.Hour)); err != nil {
		t.Fatalf("PauseKey failed: %v", err)
	}

	paused, entry, err := m.IsPaused(hash)
	if err != nil {
		t.Fatalf("IsPaused failed: %v", err)
	}
	if !paused {
		t.Fatal("expected paused=true")
	}
	if entry.Reason != "over limit" {
		t.Fatalf("unexpected reason: %s", entry.Reason)
	}

	if err := m.ResumeKey(hash); err != nil {
		t.Fatalf("ResumeKey failed: %v", err)
	}

	paused, _, err = m.IsPaused(hash)
	if err != nil {
		t.Fatalf("IsPaused failed: %v", err)
	}
	if paused {
		t.Fatal("expected paused=false after resume")
	}
}

func TestManager_ListPaused(t *testing.T) {
	m, err := NewManager(QuotaConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer m.Stop()

	_ = m.PauseKey("k1", "reason 1", time.Now().Add(1*time.Hour))
	_ = m.PauseKey("k2", "reason 2", time.Time{}) // permanent

	entries, err := m.ListPaused()
	if err != nil {
		t.Fatalf("ListPaused failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

func TestManager_IsPausedUsesSnapshot(t *testing.T) {
	m, err := NewManager(QuotaConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer m.Stop()

	hash := "snapshot"
	if err := m.PauseKey(hash, "over limit", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("PauseKey failed: %v", err)
	}
	if err := m.store.ResumeKey(hash); err != nil {
		t.Fatalf("direct store ResumeKey failed: %v", err)
	}

	paused, entry, err := m.IsPaused(hash)
	if err != nil {
		t.Fatalf("IsPaused failed: %v", err)
	}
	if !paused || entry == nil {
		t.Fatal("expected snapshot to remain paused after direct store mutation")
	}

	if err := m.refreshPausedSnapshot(); err != nil {
		t.Fatalf("refreshPausedSnapshot failed: %v", err)
	}
	paused, _, err = m.IsPaused(hash)
	if err != nil {
		t.Fatalf("IsPaused after refresh failed: %v", err)
	}
	if paused {
		t.Fatal("expected snapshot refresh to pick up resumed key")
	}
}

func TestManager_LoadsPauseSnapshot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "quota.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	hash := "persisted"
	if err := store.PauseKey(PauseEntry{
		KeyHash:   hash,
		Reason:    "manual pause",
		PausedAt:  time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
		CreatedAt: time.Now(),
	}); err != nil {
		_ = store.Close()
		t.Fatalf("PauseKey failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	m, err := NewManager(QuotaConfig{Enabled: true, DBPath: dbPath})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer m.Stop()

	paused, entry, err := m.IsPaused(hash)
	if err != nil {
		t.Fatalf("IsPaused failed: %v", err)
	}
	if !paused || entry == nil || entry.Reason != "manual pause" {
		t.Fatalf("expected persisted pause in snapshot, paused=%v entry=%v", paused, entry)
	}
}

func TestManager_ExpiredPauseIsNotInSnapshot(t *testing.T) {
	m, err := NewManager(QuotaConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer m.Stop()

	hash := "expired"
	if err := m.PauseKey(hash, "expired pause", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("PauseKey failed: %v", err)
	}

	paused, entry, err := m.IsPaused(hash)
	if err != nil {
		t.Fatalf("IsPaused failed: %v", err)
	}
	if paused || entry != nil {
		t.Fatalf("expected expired pause to be ignored, paused=%v entry=%v", paused, entry)
	}
}

func TestManager_ExpiredPauseReplacesExistingSnapshotEntry(t *testing.T) {
	m, err := NewManager(QuotaConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer m.Stop()

	hash := "expired-replace"
	if err := m.PauseKey(hash, "active pause", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("PauseKey active failed: %v", err)
	}
	if err := m.PauseKey(hash, "expired pause", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("PauseKey expired failed: %v", err)
	}

	paused, entry, err := m.IsPaused(hash)
	if err != nil {
		t.Fatalf("IsPaused failed: %v", err)
	}
	if paused || entry != nil {
		t.Fatalf("expected expired replacement to clear snapshot, paused=%v entry=%v", paused, entry)
	}
}

func TestManager_UpdateConfig(t *testing.T) {
	m, err := NewManager(QuotaConfig{Enabled: false})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer m.Stop()

	m.UpdateConfig(QuotaConfig{Enabled: true, Default: SpendLimit{DailyCents: 10000}})
	cfg := m.Config()
	if !cfg.Enabled {
		t.Fatal("expected enabled=true after update")
	}
	if cfg.Default.DailyCents != 10000 {
		t.Fatalf("unexpected daily_cents: %d", cfg.Default.DailyCents)
	}
}

func TestManager_StartStop(t *testing.T) {
	m, err := NewManager(QuotaConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if err := m.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	m.Stop()
}
