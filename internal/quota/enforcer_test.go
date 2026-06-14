package quota

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestEnforcerMiddleware_PausedKey(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	apiKey := "sk-test-key-12345"
	hash := KeyHash(apiKey)

	if err := s.PauseKey(PauseEntry{
		KeyHash:   hash,
		Reason:    "over daily limit",
		PausedAt:  time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("PauseKey failed: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userApiKey", apiKey)
		c.Next()
	})
	r.Use(EnforcerMiddleware(s))
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
	s := newTestStore(t)
	defer s.Close()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userApiKey", "sk-not-paused")
		c.Next()
	})
	r.Use(EnforcerMiddleware(s))
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

func TestEnforcerMiddleware_NoKey(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(EnforcerMiddleware(s))
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
	s := newTestStore(t)
	defer s.Close()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userApiKey", "")
		c.Next()
	})
	r.Use(EnforcerMiddleware(s))
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