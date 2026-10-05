package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
	"vocat/internal/oidclogin"
	"vocat/internal/store"
)

const testIssuer = "https://test.pocket-id.example"

func newTestService(t *testing.T) *Service {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "vocat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	service, err := New(database, Options{SessionTTL: time.Hour, OIDCIssuer: testIssuer})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestOIDCAuthenticateCSRFAndLogout(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	identity := oidclogin.Identity{Issuer: testIssuer, Subject: "alice-id", Username: "alice"}
	credentials, err := service.LoginOIDC(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Authenticate(ctx, credentials.SessionToken)
	if err != nil || session.Principal.Username != "alice" {
		t.Fatalf("session=%+v err=%v", session, err)
	}
	if _, err := service.ValidateCSRF(ctx, credentials.SessionToken, "wrong"); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("CSRF error=%v", err)
	}
	if _, err := service.ValidateCSRF(ctx, credentials.SessionToken, credentials.CSRFToken); err != nil {
		t.Fatal(err)
	}
	_, token, err := service.CSRFToken(ctx, credentials.SessionToken, credentials.CSRFToken)
	if err != nil || token != credentials.CSRFToken {
		t.Fatal("valid CSRF token rotated")
	}
	if err := service.Logout(ctx, credentials.SessionToken); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, credentials.SessionToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("logged-out error=%v", err)
	}
}

func TestOIDCUsersKeepDistinctSessionIdentities(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	for _, name := range []string{"alice", "bob"} {
		credentials, err := service.LoginOIDC(ctx, oidclogin.Identity{Issuer: testIssuer, Subject: name + "-id", Username: name})
		if err != nil {
			t.Fatal(err)
		}
		session, err := service.Authenticate(ctx, credentials.SessionToken)
		if err != nil || session.Principal.Username != name {
			t.Fatalf("identity lost: %+v %v", session, err)
		}
		stored, err := service.store.SessionByTokenHash(ctx, hashToken(credentials.SessionToken))
		if err != nil || stored.OIDCSubject != name+"-id" || stored.OIDCIssuer != testIssuer {
			t.Fatal("stable OIDC identity not persisted")
		}
	}
}

func TestOIDCRejectsOtherIssuersAndMissingSubjects(t *testing.T) {
	service := newTestService(t)
	for _, identity := range []oidclogin.Identity{{Issuer: "https://evil.example", Subject: "alice"}, {Issuer: testIssuer}} {
		if _, err := service.LoginOIDC(context.Background(), identity); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("accepted identity %+v: %v", identity, err)
		}
	}
}

func TestLegacyAndOtherIssuerSessionsAreRejected(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.EnsureOIDCAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.store.CreateSession(ctx, 1, hashToken("legacy"), hashToken("csrf"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, "legacy"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("legacy session accepted")
	}
	credentials, err := service.LoginOIDC(ctx, oidclogin.Identity{Issuer: testIssuer, Subject: "alice", Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	service.oidcIssuer = "https://other.example"
	if _, err := service.Authenticate(ctx, credentials.SessionToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("other issuer session accepted")
	}
}
