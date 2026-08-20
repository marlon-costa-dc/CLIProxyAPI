package quota

import (
	"testing"
	"time"
)

func TestStore_ConfiguresSingleConnectionAndExpiryIndex(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if got := s.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("max open connections = %d, want 1", got)
	}

	var indexName string
	if err := s.db.QueryRow(`select name from sqlite_master where type = 'index' and name = 'idx_key_pauses_expires_at'`).Scan(&indexName); err != nil {
		t.Fatalf("expiry index lookup failed: %v", err)
	}
	if indexName != "idx_key_pauses_expires_at" {
		t.Fatalf("index name = %q, want idx_key_pauses_expires_at", indexName)
	}
}

func TestStore_PauseKeyAndIsPaused(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	hash := "abcd1234"
	entry := PauseEntry{
		KeyHash:   hash,
		Reason:    "over daily limit",
		PausedAt:  time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
	}

	if err := s.PauseKey(entry); err != nil {
		t.Fatalf("PauseKey failed: %v", err)
	}

	paused, got, err := s.IsPaused(hash)
	if err != nil {
		t.Fatalf("IsPaused failed: %v", err)
	}
	if !paused {
		t.Fatal("expected paused=true")
	}
	if got.Reason != "over daily limit" {
		t.Fatalf("unexpected reason: %s", got.Reason)
	}
}

func TestStore_ResumeKey(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	hash := "resume-test"
	entry := PauseEntry{
		KeyHash:   hash,
		Reason:    "test",
		PausedAt:  time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
	}

	if err := s.PauseKey(entry); err != nil {
		t.Fatalf("PauseKey failed: %v", err)
	}
	if err := s.ResumeKey(hash); err != nil {
		t.Fatalf("ResumeKey failed: %v", err)
	}

	paused, _, err := s.IsPaused(hash)
	if err != nil {
		t.Fatalf("IsPaused failed: %v", err)
	}
	if paused {
		t.Fatal("expected paused=false after resume")
	}
}

func TestStore_CleanupExpired(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	// Pause entry with expired time (1 hour ago)
	hash := "expired-test"
	entry := PauseEntry{
		KeyHash:   hash,
		Reason:    "expired",
		PausedAt:  time.Now().Add(-2 * time.Hour),
		ExpiresAt: time.Now().Add(-1 * time.Hour),
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}
	if err := s.PauseKey(entry); err != nil {
		t.Fatalf("PauseKey failed: %v", err)
	}

	n, err := s.CleanupExpired()
	if err != nil {
		t.Fatalf("CleanupExpired failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 cleanup, got %d", n)
	}

	// Confirm expired entry is gone
	paused, _, err := s.IsPaused(hash)
	if err != nil {
		t.Fatalf("IsPaused failed: %v", err)
	}
	if paused {
		t.Fatal("expected paused=false after cleanup")
	}
}

func TestStore_ListPausedOnlyNonExpired(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	// Permanent pause
	if err := s.PauseKey(PauseEntry{
		KeyHash:   "perm-1",
		Reason:    "manual",
		PausedAt:  time.Now(),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("PauseKey failed: %v", err)
	}

	// Expired entry
	if err := s.PauseKey(PauseEntry{
		KeyHash:   "exp-1",
		Reason:    "expired",
		PausedAt:  time.Now().Add(-2 * time.Hour),
		ExpiresAt: time.Now().Add(-1 * time.Hour),
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("PauseKey failed: %v", err)
	}

	// Active timed entry
	if err := s.PauseKey(PauseEntry{
		KeyHash:   "active-1",
		Reason:    "over limit",
		PausedAt:  time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("PauseKey failed: %v", err)
	}

	entries, err := s.ListPaused()
	if err != nil {
		t.Fatalf("ListPaused failed: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 paused entries (permanent + active), got %d", len(entries))
	}

	found := make(map[string]bool)
	for _, e := range entries {
		found[e.KeyHash] = true
	}
	if !found["perm-1"] || !found["active-1"] {
		t.Fatal("ListPaused missing expected entries")
	}
}

// TestStore_ExpiredKeyIsNotPaused verifies auto-resume: a key with
// an expired pause entry is treated as not paused (simulating the
// next-day/week recovery flow in spend limiting).
func TestStore_ExpiredKeyIsNotPaused(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	hash := "auto-resume-test"
	if err := s.PauseKey(PauseEntry{
		KeyHash:   hash,
		Reason:    "spend_limit_exceeded",
		PausedAt:  time.Now().Add(-2 * time.Hour),
		ExpiresAt: time.Now().Add(-1 * time.Hour), // expired 1 hour ago
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("PauseKey failed: %v", err)
	}

	// IsPaused should report false for expired entries
	paused, entry, err := s.IsPaused(hash)
	if err != nil {
		t.Fatalf("IsPaused failed: %v", err)
	}
	if paused {
		t.Fatal("expected paused=false for expired entry (auto-resume)")
	}
	if entry != nil {
		t.Fatal("expected nil entry for expired entry")
	}
}

func TestStore_ConcurrentSafe(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			hash := KeyHash(string(rune('a' + i)))
			_ = s.PauseKey(PauseEntry{
				KeyHash:   hash,
				Reason:    "concurrent",
				PausedAt:  time.Now(),
				ExpiresAt: time.Now().Add(1 * time.Hour),
				CreatedAt: time.Now(),
			})
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	entries, err := s.ListPaused()
	if err != nil {
		t.Fatalf("ListPaused failed: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected entries from concurrent writes")
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	return s
}
