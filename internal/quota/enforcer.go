package quota

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// PauseStore is the interface the enforcer needs from the pause store.
type PauseStore interface {
	IsPaused(keyHash string) (bool, *PauseEntry, error)
}

// EnforcerMiddleware returns a Gin middleware that rejects paused API keys with 429.
// It reads "userApiKey" from the gin context (set by AuthMiddleware).
func EnforcerMiddleware(store PauseStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		rawKey, exists := c.Get("userApiKey")
		if !exists {
			c.Next()
			return
		}
		apiKey, ok := rawKey.(string)
		if !ok || apiKey == "" {
			c.Next()
			return
		}

		hash := KeyHash(apiKey)
		paused, entry, err := store.IsPaused(hash)
		if err != nil {
			log.Errorf("quota enforcer: IsPaused error for hash %s: %v", hash[:4], err)
			c.Next()
			return
		}
		if !paused || entry == nil {
			c.Next()
			return
		}

		resp := gin.H{
			"error":   "api_key_paused",
			"message": entry.Reason,
		}
		if !entry.ExpiresAt.IsZero() {
			resp["resumes_at"] = entry.ExpiresAt.Format(http.TimeFormat)
		} else {
			resp["resumes_at"] = nil
		}

		c.AbortWithStatusJSON(http.StatusTooManyRequests, resp)
	}
}
