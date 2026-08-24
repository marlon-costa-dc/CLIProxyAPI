package quota

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestStore_DowngradeUpsertPreservesCreatedAt(t *testing.T) {
	s := newTestStore(t)
	defer func() { _ = s.Close() }()

	createdAt := time.Now().Add(-time.Hour).Truncate(time.Second)
	firstDowngradedAt := time.Now().Add(-30 * time.Minute).Truncate(time.Second)
	firstExpiry := time.Now().Add(time.Hour).Truncate(time.Second)
	first, err := s.UpsertDowngrade(DowngradeEntry{
		KeyHash:       "abcdef12",
		Reason:        "spend_limit_exceeded",
		FallbackModel: "gpt-5.6-luna",
		DowngradedAt:  firstDowngradedAt,
		ExpiresAt:     firstExpiry,
		CreatedAt:     createdAt,
	})
	if err != nil {
		t.Fatalf("UpsertDowngrade first: %v", err)
	}

	secondDowngradedAt := firstDowngradedAt.Add(time.Minute)
	secondExpiry := firstExpiry.Add(time.Hour)
	second, err := s.UpsertDowngrade(DowngradeEntry{
		KeyHash:       "abcdef12",
		Reason:        "refreshed_reason",
		FallbackModel: "gpt-5.6-terra",
		DowngradedAt:  secondDowngradedAt,
		ExpiresAt:     secondExpiry,
		CreatedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertDowngrade refresh: %v", err)
	}
	if first.CreatedAt.Unix() != createdAt.Unix() || second.CreatedAt.Unix() != createdAt.Unix() {
		t.Fatalf("created_at changed: first=%v second=%v want=%v", first.CreatedAt, second.CreatedAt, createdAt)
	}
	if second.DowngradedAt.Unix() != secondDowngradedAt.Unix() || second.ExpiresAt.Unix() != secondExpiry.Unix() {
		t.Fatalf("refresh timestamps = downgraded=%v expiry=%v", second.DowngradedAt, second.ExpiresAt)
	}
	if second.FallbackModel != "gpt-5.6-terra" || second.Reason != "refreshed_reason" {
		t.Fatalf("refresh fields = %+v", second)
	}

	var pauseColumns int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('key_pauses') WHERE name = 'fallback_model'`).Scan(&pauseColumns); err != nil {
		t.Fatalf("inspect key_pauses schema: %v", err)
	}
	if pauseColumns != 0 {
		t.Fatal("key_pauses schema unexpectedly contains fallback_model")
	}
}

func TestManager_DowngradePersistsAndReasonSafeResume(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "quota.db")
	m, err := NewManager(QuotaConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.DowngradeKey("abcdef12", "manual_downgrade", "gpt-5.6-luna", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("DowngradeKey: %v", err)
	}
	m.Stop()

	m, err = NewManager(QuotaConfig{DBPath: dbPath})
	if err != nil {
		t.Fatalf("NewManager after restart: %v", err)
	}
	defer m.Stop()

	downgraded, entry, err := m.IsDowngraded("abcdef12")
	if err != nil || !downgraded || entry == nil || entry.FallbackModel != "gpt-5.6-luna" {
		t.Fatalf("persisted downgrade = downgraded=%v entry=%+v err=%v", downgraded, entry, err)
	}
	if err := m.ResumeDowngradeKeyIfReason("abcdef12", "spend_limit_exceeded"); err != nil {
		t.Fatalf("conditional resume mismatch: %v", err)
	}
	if downgraded, entry, err = m.IsDowngraded("abcdef12"); err != nil || !downgraded || entry == nil || entry.Reason != "manual_downgrade" {
		t.Fatalf("mismatched reason removed state: downgraded=%v entry=%+v err=%v", downgraded, entry, err)
	}
	if err := m.ResumeDowngradeKeyIfReason("abcdef12", "manual_downgrade"); err != nil {
		t.Fatalf("conditional resume match: %v", err)
	}
	if downgraded, _, err = m.IsDowngraded("abcdef12"); err != nil || downgraded {
		t.Fatalf("matched reason did not remove state: downgraded=%v err=%v", downgraded, err)
	}
}

func TestManager_ExpiredDowngradeIsNotReturnedOrEnforced(t *testing.T) {
	m, err := NewManager(QuotaConfig{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Stop()

	if err := m.DowngradeKey("abcdef12", "spend_limit_exceeded", "gpt-5.6-luna", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("DowngradeKey: %v", err)
	}
	if downgraded, entry, err := m.IsDowngraded("abcdef12"); err != nil || downgraded || entry != nil {
		t.Fatalf("expired downgrade = downgraded=%v entry=%+v err=%v", downgraded, entry, err)
	}
	entries, err := m.ListDowngraded()
	if err != nil {
		t.Fatalf("ListDowngraded: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expired entries = %+v, want none", entries)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userApiKey", "raw-api-key")
		c.Next()
	})
	router.Use(m.EnforcerMiddleware())
	router.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/test", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("downgrade state affected pause enforcer: status=%d", recorder.Code)
	}
}
