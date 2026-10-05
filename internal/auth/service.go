package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"vocat/internal/store"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrInvalidCSRF  = errors.New("invalid csrf token")
)

type Options struct {
	OIDCIssuer string
	SessionTTL time.Duration
}

type Service struct {
	oidcIssuer string
	store      *store.Store
	sessionTTL time.Duration
}

type Principal struct {
	ID       int64  `json:"-"`
	Username string `json:"username"`
}

type Credentials struct {
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
	Principal    Principal
}

type AuthenticatedSession struct {
	Principal Principal
	ExpiresAt time.Time
	tokenHash []byte
	csrfHash  []byte
}

func New(database *store.Store, options Options) (*Service, error) {
	if database == nil {
		return nil, errors.New("auth: store is required")
	}
	if options.OIDCIssuer == "" {
		return nil, errors.New("auth: OIDC issuer is required")
	}
	if options.SessionTTL <= 0 {
		return nil, errors.New("auth: session TTL must be positive")
	}
	return &Service{
		store:      database,
		oidcIssuer: options.OIDCIssuer,
		sessionTTL: options.SessionTTL,
	}, nil
}

func (s *Service) createSession(ctx context.Context, admin store.Admin, issuer, subject, username string) (Credentials, error) {
	if err := s.store.DeleteExpiredSessions(ctx, time.Now()); err != nil {
		return Credentials{}, err
	}
	sessionToken, err := randomToken()
	if err != nil {
		return Credentials{}, err
	}
	csrfToken, err := randomToken()
	if err != nil {
		return Credentials{}, err
	}
	expiresAt := time.Now().UTC().Add(s.sessionTTL)
	if err := s.store.CreateIdentitySession(
		ctx,
		admin.ID,
		hashToken(sessionToken),
		hashToken(csrfToken),
		expiresAt, issuer, subject, username,
	); err != nil {
		return Credentials{}, err
	}
	return Credentials{
		SessionToken: sessionToken,
		CSRFToken:    csrfToken,
		ExpiresAt:    expiresAt,
		Principal: Principal{
			ID:       admin.ID,
			Username: admin.Username,
		},
	}, nil
}

func (s *Service) Authenticate(ctx context.Context, sessionToken string) (AuthenticatedSession, error) {
	if sessionToken == "" {
		return AuthenticatedSession{}, ErrUnauthorized
	}
	tokenHash := hashToken(sessionToken)
	session, err := s.store.SessionByTokenHash(ctx, tokenHash)
	if errors.Is(err, store.ErrNotFound) {
		return AuthenticatedSession{}, ErrUnauthorized
	}
	if err != nil {
		return AuthenticatedSession{}, fmt.Errorf("auth: load session: %w", err)
	}
	if s.oidcIssuer == "" || session.OIDCIssuer != s.oidcIssuer || session.OIDCSubject == "" {
		return AuthenticatedSession{}, ErrUnauthorized
	}
	if session.OIDCSubject != "" {
		session.Admin.Username = session.OIDCUsername
	}
	if !session.ExpiresAt.After(time.Now().UTC()) {
		_ = s.store.DeleteSession(ctx, tokenHash)
		return AuthenticatedSession{}, ErrUnauthorized
	}
	return AuthenticatedSession{
		Principal: Principal{
			ID:       session.Admin.ID,
			Username: session.Admin.Username,
		},
		ExpiresAt: session.ExpiresAt,
		tokenHash: tokenHash,
		csrfHash:  session.CSRFHash,
	}, nil
}

// RotateCSRF replaces the session-bound CSRF value and returns the new raw
// token. Only its SHA-256 digest is persisted.
func (s *Service) RotateCSRF(ctx context.Context, sessionToken string) (AuthenticatedSession, string, error) {
	return s.CSRFToken(ctx, sessionToken, "")
}

// CSRFToken reuses a valid CSRF cookie or rotates it when the cookie is absent
// or stale. Reuse prevents one browser tab from invalidating another tab's
// session-bound token.
func (s *Service) CSRFToken(
	ctx context.Context,
	sessionToken string,
	existingToken string,
) (AuthenticatedSession, string, error) {
	session, err := s.Authenticate(ctx, sessionToken)
	if err != nil {
		return AuthenticatedSession{}, "", err
	}
	if existingToken != "" {
		existingHash := hashToken(existingToken)
		if subtle.ConstantTimeCompare(existingHash, session.csrfHash) == 1 {
			return session, existingToken, nil
		}
	}
	csrfToken, err := randomToken()
	if err != nil {
		return AuthenticatedSession{}, "", err
	}
	csrfHash := hashToken(csrfToken)
	if err := s.store.UpdateSessionCSRF(ctx, session.tokenHash, csrfHash); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return AuthenticatedSession{}, "", ErrUnauthorized
		}
		return AuthenticatedSession{}, "", err
	}
	session.csrfHash = csrfHash
	return session, csrfToken, nil
}

func (s *Service) ValidateCSRF(
	ctx context.Context,
	sessionToken string,
	csrfToken string,
) (AuthenticatedSession, error) {
	if csrfToken == "" {
		return AuthenticatedSession{}, ErrInvalidCSRF
	}
	session, err := s.Authenticate(ctx, sessionToken)
	if err != nil {
		return AuthenticatedSession{}, err
	}
	providedHash := hashToken(csrfToken)
	if subtle.ConstantTimeCompare(providedHash, session.csrfHash) != 1 {
		return AuthenticatedSession{}, ErrInvalidCSRF
	}
	return session, nil
}

func (s *Service) Logout(ctx context.Context, sessionToken string) error {
	if sessionToken == "" {
		return nil
	}
	if err := s.store.DeleteSession(ctx, hashToken(sessionToken)); err != nil {
		return err
	}
	return nil
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("auth: generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func hashToken(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}
