package quota

import (
	"database/sql"
	"time"

	log "github.com/sirupsen/logrus"
)

// UpsertDowngrade creates or refreshes a downgrade entry while retaining its first creation time.
func (s *Store) UpsertDowngrade(entry DowngradeEntry) (DowngradeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if entry.DowngradedAt.IsZero() {
		entry.DowngradedAt = now
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}

	expiresUnix := int64(0)
	if !entry.ExpiresAt.IsZero() {
		expiresUnix = entry.ExpiresAt.Unix()
	}
	_, err := s.db.Exec(`INSERT INTO key_downgrades (key_hash, reason, fallback_model, downgraded_at, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(key_hash) DO UPDATE SET
			reason = excluded.reason,
			fallback_model = excluded.fallback_model,
			downgraded_at = excluded.downgraded_at,
			expires_at = excluded.expires_at`,
		entry.KeyHash, entry.Reason, entry.FallbackModel, entry.DowngradedAt.Unix(), expiresUnix, entry.CreatedAt.Unix())
	if err != nil {
		return DowngradeEntry{}, err
	}
	return s.getDowngradeLocked(entry.KeyHash)
}

// ResumeDowngrade removes a downgrade entry by key hash.
func (s *Store) ResumeDowngrade(keyHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM key_downgrades WHERE key_hash = ?", keyHash)
	return err
}

// ResumeDowngradeIfReason deletes only an entry with the expected reason.
func (s *Store) ResumeDowngradeIfReason(keyHash, expectedReason string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec("DELETE FROM key_downgrades WHERE key_hash = ? AND reason = ?", keyHash, expectedReason)
	if err != nil {
		return false, err
	}
	deleted, err := result.RowsAffected()
	return deleted > 0, err
}

// IsDowngraded reports whether a key has a current downgrade entry.
func (s *Store) IsDowngraded(keyHash string) (bool, *DowngradeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, err := s.getDowngradeLocked(keyHash)
	if err == sql.ErrNoRows {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if entry.IsExpired() {
		return false, nil, nil
	}
	return true, &entry, nil
}

// ListDowngraded returns all non-expired downgrade entries.
func (s *Store) ListDowngraded() ([]DowngradeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT key_hash, reason, fallback_model, downgraded_at, expires_at, created_at
		FROM key_downgrades WHERE expires_at = 0 OR expires_at > ?`, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	entries := make([]DowngradeEntry, 0)
	for rows.Next() {
		entry, err := scanDowngrade(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// CleanupExpiredDowngrades removes expired downgrade entries.
func (s *Store) CleanupExpiredDowngrades() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec("DELETE FROM key_downgrades WHERE expires_at > 0 AND expires_at <= ?", time.Now().Unix())
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count > 0 {
		log.Infof("quota: cleaned up %d expired downgrade entries", count)
	}
	return int(count), nil
}

func (s *Store) getDowngradeLocked(keyHash string) (DowngradeEntry, error) {
	row := s.db.QueryRow(`SELECT key_hash, reason, fallback_model, downgraded_at, expires_at, created_at
		FROM key_downgrades WHERE key_hash = ?`, keyHash)
	return scanDowngrade(row)
}

type downgradeScanner interface {
	Scan(dest ...any) error
}

func scanDowngrade(scanner downgradeScanner) (DowngradeEntry, error) {
	var (
		entry      DowngradeEntry
		downgraded int64
		expires    int64
		createdAt  int64
	)
	if err := scanner.Scan(&entry.KeyHash, &entry.Reason, &entry.FallbackModel, &downgraded, &expires, &createdAt); err != nil {
		return DowngradeEntry{}, err
	}
	entry.DowngradedAt = time.Unix(downgraded, 0)
	entry.CreatedAt = time.Unix(createdAt, 0)
	if expires != 0 {
		entry.ExpiresAt = time.Unix(expires, 0)
	}
	return entry, nil
}
