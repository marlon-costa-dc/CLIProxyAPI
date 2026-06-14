package quota

import (
	"testing"
	"time"
)

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