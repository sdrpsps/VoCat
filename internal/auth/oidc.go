package auth

import (
	"context"
	"vocat/internal/oidclogin"
)

// LoginOIDC must only receive an identity from the verified OIDC callback.
// Pocket ID's client assignments authorize these identities as administrators.
func (s *Service) LoginOIDC(ctx context.Context, identity oidclogin.Identity) (Credentials, error) {
	if s.oidcIssuer == "" || identity.Issuer != s.oidcIssuer || identity.Subject == "" {
		return Credentials{}, ErrUnauthorized
	}
	if err := s.store.EnsureOIDCAdmin(ctx); err != nil {
		return Credentials{}, err
	}
	admin, err := s.store.CurrentAdmin(ctx)
	if err != nil {
		return Credentials{}, err
	}
	admin.Username = identity.Username
	return s.createSession(ctx, admin, identity.Issuer, identity.Subject, identity.Username)
}
