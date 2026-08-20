package quota

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

// Store persists pause entries to SQLite.
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// NewStore opens or creates the SQLite database at dbPath.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open quota db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := s.Init(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init quota db: %w", err)
	}
	return s, nil
}

// Init creates the key_pauses table if it does not exist.
func (s *Store) Init() error {
	query := `CREATE TABLE IF NOT EXISTS key_pauses (
		key_hash   TEXT PRIMARY KEY,
		reason     TEXT NOT NULL DEFAULT '',
		paused_at  INTEGER NOT NULL,
		expires_at INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	)`
	if _, err := s.db.Exec(query); err != nil {
		return err
	}
	_, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_key_pauses_expires_at ON key_pauses(expires_at)`)
	return err
}

// PauseKey inserts or updates a pause entry.
func (s *Store) PauseKey(entry PauseEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `INSERT OR REPLACE INTO key_pauses (key_hash, reason, paused_at, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?)`
	pausedUnix := entry.PausedAt.Unix()
	expiresUnix := int64(0)
	if !entry.ExpiresAt.IsZero() {
		expiresUnix = entry.ExpiresAt.Unix()
	}
	createdUnix := entry.CreatedAt.Unix()
	if createdUnix == 0 {
		createdUnix = time.Now().Unix()
	}

	_, err := s.db.Exec(query, entry.KeyHash, entry.Reason, pausedUnix, expiresUnix, createdUnix)
	return err
}

// ResumeKey removes a pause entry by key hash.
func (s *Store) ResumeKey(keyHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM key_pauses WHERE key_hash = ?", keyHash)
	return err
}

// ResumeKeyIfReason 仅在当前暂停原因匹配时删除，避免自动恢复覆盖并发写入的手动暂停。
func (s *Store) ResumeKeyIfReason(keyHash, expectedReason string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec("DELETE FROM key_pauses WHERE key_hash = ? AND reason = ?", keyHash, expectedReason)
	if err != nil {
		return false, err
	}
	deleted, err := result.RowsAffected()
	return deleted > 0, err
}

// IsPaused checks whether a key is currently paused.
// Returns the pause entry if paused.
func (s *Store) IsPaused(keyHash string) (bool, *PauseEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRow("SELECT key_hash, reason, paused_at, expires_at, created_at FROM key_pauses WHERE key_hash = ?", keyHash)
	var (
		keyHashOut string
		reason     string
		pausedAt   int64
		expiresAt  int64
		createdAt  int64
	)
	err := row.Scan(&keyHashOut, &reason, &pausedAt, &expiresAt, &createdAt)
	if err == sql.ErrNoRows {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}

	entry := &PauseEntry{
		KeyHash:   keyHashOut,
		Reason:    reason,
		PausedAt:  time.Unix(pausedAt, 0),
		CreatedAt: time.Unix(createdAt, 0),
	}
	if expiresAt != 0 {
		entry.ExpiresAt = time.Unix(expiresAt, 0)
	}

	if entry.IsExpired() {
		return false, nil, nil
	}
	return true, entry, nil
}

// ListPaused returns all non-expired pause entries.
func (s *Store) ListPaused() ([]PauseEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	rows, err := s.db.Query("SELECT key_hash, reason, paused_at, expires_at, created_at FROM key_pauses WHERE expires_at = 0 OR expires_at > ?", now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []PauseEntry
	for rows.Next() {
		var (
			keyHash   string
			reason    string
			pausedAt  int64
			expiresAt int64
			createdAt int64
		)
		if err := rows.Scan(&keyHash, &reason, &pausedAt, &expiresAt, &createdAt); err != nil {
			return nil, err
		}
		entry := PauseEntry{
			KeyHash:   keyHash,
			Reason:    reason,
			PausedAt:  time.Unix(pausedAt, 0),
			CreatedAt: time.Unix(createdAt, 0),
		}
		if expiresAt != 0 {
			entry.ExpiresAt = time.Unix(expiresAt, 0)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// CleanupExpired removes expired pause entries.
// Returns the number of removed entries.
func (s *Store) CleanupExpired() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	res, err := s.db.Exec("DELETE FROM key_pauses WHERE expires_at > 0 AND expires_at <= ?", now)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Infof("quota: cleaned up %d expired pause entries", n)
	}
	return int(n), nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// KeyHash computes the abbreviated SHA-256 hash used as the pause key identifier.
func KeyHash(apiKey string) string {
	h := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(h[:4]) // first 8 hex chars = 4 bytes
}
