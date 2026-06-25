package quota

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestEnforcerMiddleware_PausedKey(t *testing.T) {
	m, err := NewManager(QuotaConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer m.Stop()

	apiKey := "sk-test-key-12345"
	hash := KeyHash(apiKey)

	if err := m.PauseKey(hash, "over daily limit", time.Now().Add(1*time.Hour)); err != nil {
		t.Fatalf("PauseKey failed: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userApiKey", apiKey)
		c.Next()
	})
	r.Use(m.EnforcerMiddleware())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
}

func TestEnforcerMiddleware_NotPausedKey(t *testing.T) {
	m, err := NewManager(QuotaConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer m.Stop()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userApiKey", "sk-not-paused")
		c.Next()
	})
	r.Use(m.EnforcerMiddleware())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestEnforcerMiddleware_UsesSnapshot(t *testing.T) {
	m, err := NewManager(QuotaConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer m.Stop()

	apiKey := "sk-snapshot-key"
	hash := KeyHash(apiKey)
	if err := m.PauseKey(hash, "over daily limit", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("PauseKey failed: %v", err)
	}
	if err := m.store.ResumeKey(hash); err != nil {
		t.Fatalf("direct store ResumeKey failed: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userApiKey", apiKey)
		c.Next()
	})
	r.Use(m.EnforcerMiddleware())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 from snapshot-backed enforcer, got %d", w.Code)
	}
}

func TestEnforcerMiddleware_NoKey(t *testing.T) {
	m, err := NewManager(QuotaConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer m.Stop()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(m.EnforcerMiddleware())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestEnforcerMiddleware_EmptyKey(t *testing.T) {
	m, err := NewManager(QuotaConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer m.Stop()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userApiKey", "")
		c.Next()
	})
	r.Use(m.EnforcerMiddleware())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
