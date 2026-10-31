package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"vocat/internal/store"
)

var (
	ErrInvalidTokenName   = errors.New("invalid api token name")
	ErrDuplicateTokenName = errors.New("api token name already exists")
	ErrUnknownToken       = errors.New("unknown api token")
)

const APITokenPrefix = "vcat_"
const apiTokenTouchInterval = 5 * time.Minute

// NewTokenService manages CLI bearer credentials without configuring browser login.
// The empty OIDC issuer keeps browser session authentication disabled.
func NewTokenService(database *store.Store) (*Service, error) {
	if database == nil {
		return nil, errors.New("auth: store is required")
	}
	return &Service{store: database}, nil
}

// APITokenCredentials is returned once at creation; the raw token is never
// stored or retrievable again.
type APITokenCredentials struct {
	Token     string
	Name      string
	ReadOnly  bool
	ExpiresAt time.Time // zero means the token never expires
}

// APITokenPrincipal identifies an authenticated bearer credential.
type APITokenPrincipal struct {
	Name     string
	ReadOnly bool
}

// CreateAPIToken issues a bearer credential. ttl <= 0 creates a token that
// never expires.
func (s *Service) CreateAPIToken(
	ctx context.Context,
	name string,
	readOnly bool,
	ttl time.Duration,
) (APITokenCredentials, error) {
	if !validTokenName(name) {
		return APITokenCredentials{}, fmt.Errorf("%w: use 1-64 letters, digits, dots, underscores, or hyphens", ErrInvalidTokenName)
	}
	if _, err := s.store.APITokenByName(ctx, name); err == nil {
		return APITokenCredentials{}, fmt.Errorf("%w: %s", ErrDuplicateTokenName, name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return APITokenCredentials{}, fmt.Errorf("auth: read api tokens: %w", err)
	}
	random, err := randomToken()
	if err != nil {
		return APITokenCredentials{}, err
	}
	token := APITokenPrefix + random
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = time.Now().UTC().Add(ttl)
	}
	if _, err := s.store.CreateAPIToken(ctx, name, hashToken(token), readOnly, expiresAt); err != nil {
		return APITokenCredentials{}, fmt.Errorf("auth: create api token: %w", err)
	}
	return APITokenCredentials{
		Token:     token,
		Name:      name,
		ReadOnly:  readOnly,
		ExpiresAt: expiresAt,
	}, nil
}

// AuthenticateAPIToken validates a presented bearer credential. Opportunistic
// cleanup and usage bookkeeping never reject an otherwise valid credential:
// a transient store hiccup must not read as "invalid token" to the client.
func (s *Service) AuthenticateAPIToken(ctx context.Context, token string) (APITokenPrincipal, error) {
	if !strings.HasPrefix(token, APITokenPrefix) {
		return APITokenPrincipal{}, ErrUnauthorized
	}
	digest := hashToken(token)
	if err := s.store.DeleteExpiredAPITokens(ctx, time.Now()); err != nil {
		slog.Warn("prune expired api tokens failed", "error", err)
	}
	stored, err := s.store.APITokenByHash(ctx, digest)
	if errors.Is(err, store.ErrNotFound) {
		return APITokenPrincipal{}, ErrUnauthorized
	}
	if err != nil {
		return APITokenPrincipal{}, fmt.Errorf("auth: load api token: %w", err)
	}
	if !stored.ExpiresAt.IsZero() && !stored.ExpiresAt.After(time.Now().UTC()) {
		return APITokenPrincipal{}, ErrUnauthorized
	}
	if err := s.store.TouchAPIToken(ctx, digest, apiTokenTouchInterval); err != nil {
		slog.Warn("touch api token failed", "error", err)
	}
	return APITokenPrincipal{Name: stored.Name, ReadOnly: stored.ReadOnly}, nil
}

// ListAPITokens returns the stored token inventory, never the credentials.
func (s *Service) ListAPITokens(ctx context.Context) ([]store.APIToken, error) {
	tokens, err := s.store.APITokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: list api tokens: %w", err)
	}
	return tokens, nil
}

// RevokeAPIToken deletes one token by name.
func (s *Service) RevokeAPIToken(ctx context.Context, name string) error {
	if err := s.store.DeleteAPIToken(ctx, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrUnknownToken, name)
		}
		return fmt.Errorf("auth: revoke api token: %w", err)
	}
	return nil
}

// validTokenName applies the same character rules as device IDs; callers trim
// surrounding whitespace before calling.
func validTokenName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for index, character := range name {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			(index > 0 && (character == '.' || character == '_' || character == '-')) {
			continue
		}
		return false
	}
	return true
}
