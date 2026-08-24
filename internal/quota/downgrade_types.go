package quota

import "time"

// DowngradeEntry records the fallback model selected for an API Key.
type DowngradeEntry struct {
	KeyHash       string    `json:"key_hash"`
	Reason        string    `json:"reason"`
	FallbackModel string    `json:"fallback_model"`
	DowngradedAt  time.Time `json:"downgraded_at"`
	ExpiresAt     time.Time `json:"expires_at"` // zero = no expiry
	CreatedAt     time.Time `json:"created_at"`
}

// IsExpired reports whether the downgrade entry has expired.
func (d *DowngradeEntry) IsExpired() bool {
	if d == nil || d.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(d.ExpiresAt)
}
