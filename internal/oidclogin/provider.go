// Package oidclogin verifies Pocket ID's OpenID Connect authorization-code flow.
package oidclogin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type Config struct {
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RedirectURL  string `json:"redirect_url"`
}

func (c Config) Configured() bool {
	return c.Issuer != "" || c.ClientID != "" || c.ClientSecret != "" || c.RedirectURL != ""
}

func (c Config) Validate() error {
	if !c.Configured() {
		return nil
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return errors.New("OIDC client_id is required")
	}
	for name, value := range map[string]string{"issuer": c.Issuer, "redirect_url": c.RedirectURL} {
		u, err := url.Parse(value)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("OIDC " + name + " must be an absolute URL without credentials, query or fragment")
		}
		if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
			return errors.New("OIDC " + name + " requires HTTPS (HTTP is allowed only on loopback for development)")
		}
		if name == "redirect_url" && u.Path != "/api/auth/oidc/callback" {
			return errors.New("OIDC redirect_url must use /api/auth/oidc/callback")
		}
	}
	return nil
}

type Identity struct {
	Issuer   string
	Subject  string
	Username string
}

type Provider struct {
	config   Config
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	client   *http.Client
}

func New(ctx context.Context, c Config) (*Provider, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !c.Configured() {
		return nil, errors.New("Pocket ID OIDC configuration is required")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	ctx = oidc.ClientContext(ctx, client)
	p, err := oidc.NewProvider(ctx, c.Issuer)
	if err != nil {
		return nil, err
	}
	return &Provider{
		config: c,
		oauth: oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURL,
			Endpoint: p.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile"}},
		verifier: p.Verifier(&oidc.Config{ClientID: c.ClientID}),
		client:   client,
	}, nil
}

func (p *Provider) SecureCookies() bool { return strings.HasPrefix(p.config.RedirectURL, "https://") }

func (p *Provider) AuthorizationURL(state, nonce, verifier string) string {
	return p.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
}

func (p *Provider) Exchange(ctx context.Context, code, nonce, verifier string) (Identity, error) {
	ctx = oidc.ClientContext(ctx, p.client)
	token, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, errors.New("OIDC token exchange failed")
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return Identity{}, errors.New("OIDC response has no ID token")
	}
	id, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, errors.New("OIDC ID token verification failed")
	}
	if id.Nonce != nonce || nonce == "" || id.Subject == "" {
		return Identity{}, errors.New("OIDC nonce or subject is invalid")
	}
	var claims struct {
		Username string `json:"preferred_username"`
		Name     string `json:"name"`
	}
	if err := id.Claims(&claims); err != nil {
		return Identity{}, errors.New("OIDC profile claims are invalid")
	}
	username := strings.TrimSpace(claims.Username)
	if username == "" {
		username = strings.TrimSpace(claims.Name)
	}
	if username == "" {
		username = id.Subject
	}
	return Identity{Issuer: id.Issuer, Subject: id.Subject, Username: username}, nil
}
