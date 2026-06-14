package quota

import (
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