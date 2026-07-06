// Package quota provides API Key pause/resume state management and enforcer middleware.
// It manages temporary suspension of API keys when usage limits are exceeded,
// with SQLite persistence and automatic expiration cleanup.
package quota

import "time"

// PauseEntry records the pause state of an API Key.
type PauseEntry struct {
	KeyHash   string    `json:"key_hash"`
	Reason    string    `json:"reason"`
	PausedAt  time.Time `json:"paused_at"`
	ExpiresAt time.Time `json:"expires_at"` // zero = permanent pause
	CreatedAt time.Time `json:"created_at"`
}

// IsExpired reports whether the pause entry has expired.
func (p *PauseEntry) IsExpired() bool {
	if p.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(p.ExpiresAt)
}

// QuotaConfig is the quota configuration embedded in the server config.
type QuotaConfig struct {
	Enabled   bool              `yaml:"enabled" json:"enabled"`
	DBPath    string            `yaml:"db-path,omitempty" json:"db_path"`
	Default   SpendLimit        `yaml:"default" json:"default"`
	Overrides []SpendLimitEntry `yaml:"overrides,omitempty" json:"overrides"`
}

// SpendLimit defines daily and weekly cost limits in cents.
type SpendLimit struct {
	DailyCents  int64 `yaml:"daily-cents" json:"daily_cents"`
	WeeklyCents int64 `yaml:"weekly-cents" json:"weekly_cents"`
}

// SpendLimitEntry associates a SpendLimit with a scope (global or api-key).
type SpendLimitEntry struct {
	ApplyTo     string `yaml:"apply-to" json:"apply_to"` // "global" | "api-key"
	ApplyValue  string `yaml:"apply-value" json:"apply_value"`
	DailyCents  int64  `yaml:"daily-cents" json:"daily_cents"`
	WeeklyCents int64  `yaml:"weekly-cents" json:"weekly_cents"`
}
