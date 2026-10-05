package store

import (
	"context"
	"time"
)

// EnsureOIDCAdmin preserves existing installations and creates only the local
// session owner in a new database. There are no local password credentials.
func (s *Store) EnsureOIDCAdmin(ctx context.Context) error {
	now := time.Now().UTC().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO admins (id, username, created_at, updated_at)
		VALUES (1, 'pocket-id', ?, ?) ON CONFLICT(id) DO NOTHING`, now, now)
	return err
}

func (s *Store) CreateIdentitySession(ctx context.Context, adminID int64, tokenHash, csrfHash []byte, expiresAt time.Time, issuer, subject, username string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions
		(token_hash, admin_id, csrf_hash, expires_at, created_at, oidc_issuer, oidc_subject, oidc_username)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, tokenHash, adminID, csrfHash, expiresAt.UTC().Unix(), time.Now().UTC().Unix(), issuer, subject, username)
	return err
}
