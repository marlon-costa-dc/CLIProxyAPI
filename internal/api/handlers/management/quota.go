package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
)

// Quota exceeded toggles
func (h *Handler) GetSwitchProject(c *gin.Context) {
	c.JSON(200, gin.H{"switch-project": h.cfg.QuotaExceeded.SwitchProject})
}
func (h *Handler) PutSwitchProject(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.QuotaExceeded.SwitchProject = v })
}

func (h *Handler) GetSwitchPreviewModel(c *gin.Context) {
	c.JSON(200, gin.H{"switch-preview-model": h.cfg.QuotaExceeded.SwitchPreviewModel})
}
func (h *Handler) PutSwitchPreviewModel(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.QuotaExceeded.SwitchPreviewModel = v })
}

// PostPauseKey pauses an API key.
// Body: {"key_hash": "...", "reason": "...", "expires_in_seconds": 3600}
func (h *Handler) PostPauseKey(c *gin.Context) {
	h.mu.Lock()
	qm := h.quotaManager
	h.mu.Unlock()

	if qm == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "quota is disabled"})
		return
	}

	var body struct {
		KeyHash          string `json:"key_hash"`
		Reason           string `json:"reason"`
		ExpiresInSeconds int64  `json:"expires_in_seconds"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if body.KeyHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key_hash is required"})
		return
	}

	var expiresAt time.Time
	if body.ExpiresInSeconds > 0 {
		expiresAt = time.Now().Add(time.Duration(body.ExpiresInSeconds) * time.Second)
	}

	if err := qm.PauseKey(body.KeyHash, body.Reason, expiresAt); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// PostResumeKey resumes a paused API key.
// Body: {"key_hash": "..."}
func (h *Handler) PostResumeKey(c *gin.Context) {
	h.mu.Lock()
	qm := h.quotaManager
	h.mu.Unlock()

	if qm == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "quota is disabled"})
		return
	}

	var body struct {
		KeyHash string `json:"key_hash"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if body.KeyHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key_hash is required"})
		return
	}

	if err := qm.ResumeKey(body.KeyHash); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// GetPausedKeys returns all non-expired pause entries.
func (h *Handler) GetPausedKeys(c *gin.Context) {
	h.mu.Lock()
	qm := h.quotaManager
	h.mu.Unlock()

	if qm == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "entries": []quota.PauseEntry{}})
		return
	}

	entries, err := qm.ListPaused()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []quota.PauseEntry{}
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries})
}

// GetQuotaConfig returns the current quota configuration.
func (h *Handler) GetQuotaConfig(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "db_path": "", "default": gin.H{}, "overrides": []gin.H{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":   h.cfg.Quota.Enabled,
		"db_path":   h.cfg.Quota.DBPath,
		"default":   gin.H{"daily_cents": h.cfg.Quota.Default.DailyCents, "weekly_cents": h.cfg.Quota.Default.WeeklyCents},
		"overrides": quotaOverridesToJSON(h.cfg.Quota.Overrides),
	})
}

func quotaOverridesToJSON(entries []quota.SpendLimitEntry) []gin.H {
	out := make([]gin.H, len(entries))
	for i, e := range entries {
		out[i] = gin.H{
			"apply_to":     e.ApplyTo,
			"apply_value":  e.ApplyValue,
			"daily_cents":  e.DailyCents,
			"weekly_cents": e.WeeklyCents,
		}
	}
	return out
}

// PutQuotaConfig updates the quota configuration and persists to disk.
func (h *Handler) PutQuotaConfig(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var body struct {
		Enabled   *bool                 `json:"enabled"`
		DBPath    *string               `json:"db_path"`
		Default   *spendLimitBody       `json:"default"`
		Overrides []spendLimitEntryBody `json:"overrides"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	if body.Enabled != nil {
		h.cfg.Quota.Enabled = *body.Enabled
	}
	if body.DBPath != nil {
		h.cfg.Quota.DBPath = *body.DBPath
	}
	if body.Default != nil {
		if body.Default.DailyCents != nil {
			h.cfg.Quota.Default.DailyCents = *body.Default.DailyCents
		}
		if body.Default.WeeklyCents != nil {
			h.cfg.Quota.Default.WeeklyCents = *body.Default.WeeklyCents
		}
	}
	if body.Overrides != nil {
		h.cfg.Quota.Overrides = make([]quota.SpendLimitEntry, len(body.Overrides))
		for i, o := range body.Overrides {
			h.cfg.Quota.Overrides[i] = quota.SpendLimitEntry{
				ApplyTo:     o.ApplyTo,
				ApplyValue:  o.ApplyValue,
				DailyCents:  o.DailyCents,
				WeeklyCents: o.WeeklyCents,
			}
		}
	}

	h.persistLocked(c)
}

type spendLimitBody struct {
	DailyCents  *int64 `json:"daily_cents"`
	WeeklyCents *int64 `json:"weekly_cents"`
}

type spendLimitEntryBody struct {
	ApplyTo     string `json:"apply_to"`
	ApplyValue  string `json:"apply_value"`
	DailyCents  int64  `json:"daily_cents"`
	WeeklyCents int64  `json:"weekly_cents"`
}
