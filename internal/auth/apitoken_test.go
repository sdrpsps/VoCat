package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"vocat/internal/oidclogin"
	"vocat/internal/store"
)

func TestTokenServiceKeepsBrowserAuthenticationDisabled(t *testing.T) {
	ctx := context.Background()
	browser := newTestService(t)
	identity := oidclogin.Identity{Issuer: testIssuer, Subject: "alice-id", Username: "alice"}
	cookie, err := browser.LoginOIDC(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewTokenService(browser.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, cookie.SessionToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("token-only service accepted browser session: %v", err)
	}
	if _, err := service.LoginOIDC(ctx, identity); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("token-only service accepted OIDC login: %v", err)
	}
	token, err := service.CreateAPIToken(ctx, "cli", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := browser.LoginOIDC(ctx, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := browser.AuthenticateAPIToken(ctx, token.Token); err != nil {
		t.Fatalf("Pocket ID login invalidated CLI token: %v", err)
	}
	if _, err := NewTokenService(nil); err == nil {
		t.Fatal("accepted nil token store")
	}
}

func TestCreateAndAuthenticateAPIToken(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)

	credentials, err := service.CreateAPIToken(ctx, "ci-runner", true, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}
	if !strings.HasPrefix(credentials.Token, APITokenPrefix) {
		t.Fatalf("token = %q, want %q prefix", credentials.Token, APITokenPrefix)
	}
	if credentials.Token == APITokenPrefix {
		t.Fatal("token has no random part")
	}

	principal, err := service.AuthenticateAPIToken(ctx, credentials.Token)
	if err != nil {
		t.Fatalf("AuthenticateAPIToken() error = %v", err)
	}
	if principal.Name != "ci-runner" || !principal.ReadOnly {
		t.Fatalf("principal = %+v, want name ci-runner read-only", principal)
	}

	// A second authentication within the touch interval must not move
	// last_used_at.
	first, err := service.ListAPITokens(ctx)
	if err != nil || len(first) != 1 {
		t.Fatalf("ListAPITokens() = %v, %v", first, err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, credentials.Token); err != nil {
		t.Fatalf("AuthenticateAPIToken() second error = %v", err)
	}
	second, err := service.ListAPITokens(ctx)
	if err != nil || len(second) != 1 {
		t.Fatalf("ListAPITokens() = %v, %v", second, err)
	}
	if !second[0].LastUsedAt.Equal(first[0].LastUsedAt) {
		t.Fatal("last_used_at moved within the touch interval")
	}

	if _, err := service.AuthenticateAPIToken(ctx, APITokenPrefix+"not-issued"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("AuthenticateAPIToken(unknown) error = %v, want ErrUnauthorized", err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, "no-prefix"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("AuthenticateAPIToken(no prefix) error = %v, want ErrUnauthorized", err)
	}
}

func TestCreateAPITokenValidation(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)

	for _, name := range []string{"", strings.Repeat("a", 65), "has space", "has/slash", "-leading-hyphen"} {
		if _, err := service.CreateAPIToken(ctx, name, false, 0); !errors.Is(err, ErrInvalidTokenName) {
			t.Fatalf("CreateAPIToken(%q) error = %v, want ErrInvalidTokenName", name, err)
		}
	}
	if _, err := service.CreateAPIToken(ctx, "usable.name-1_x", false, 0); err != nil {
		t.Fatalf("CreateAPIToken(valid) error = %v", err)
	}
	if _, err := service.CreateAPIToken(ctx, "usable.name-1_x", false, 0); !errors.Is(err, ErrDuplicateTokenName) {
		t.Fatalf("CreateAPIToken(duplicate) error = %v, want ErrDuplicateTokenName", err)
	}
}

func TestAPITokenExpiry(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service, err := NewTokenService(database)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// A token whose expiry already passed, as an expired installation would
	// have persisted it.
	credential := APITokenPrefix + "already-expired"
	digest := sha256.Sum256([]byte(credential))
	if _, err := database.CreateAPIToken(
		ctx,
		"expired",
		digest[:],
		false,
		time.Now().UTC().Add(-time.Minute),
	); err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}
	// A token with future expiry authenticates while fresh.
	fresh, err := service.CreateAPIToken(ctx, "still-valid", false, time.Hour)
	if err != nil {
		t.Fatalf("CreateAPIToken(fresh) error = %v", err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, fresh.Token); err != nil {
		t.Fatalf("AuthenticateAPIToken(fresh) error = %v", err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, credential); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("AuthenticateAPIToken(expired) error = %v, want ErrUnauthorized", err)
	}

	// The expired token must be pruned; the fresh one kept.
	tokens, err := service.ListAPITokens(ctx)
	if err != nil {
		t.Fatalf("ListAPITokens() error = %v", err)
	}
	if len(tokens) != 1 || tokens[0].Name != "still-valid" {
		t.Fatalf("ListAPITokens() = %+v, want only still-valid", tokens)
	}
	if err := service.RevokeAPIToken(ctx, "missing"); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("RevokeAPIToken(unknown) error = %v, want ErrUnknownToken", err)
	}
}

func TestRevokeAPIToken(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)

	credentials, err := service.CreateAPIToken(ctx, "revoked-later", false, 0)
	if err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}
	if err := service.RevokeAPIToken(ctx, "revoked-later"); err != nil {
		t.Fatalf("RevokeAPIToken() error = %v", err)
	}
	if _, err := service.AuthenticateAPIToken(ctx, credentials.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("AuthenticateAPIToken(revoked) error = %v, want ErrUnauthorized", err)
	}
	if err := service.RevokeAPIToken(ctx, "missing"); err == nil {
		t.Fatal("RevokeAPIToken(unknown) succeeded, want error")
	}
}
