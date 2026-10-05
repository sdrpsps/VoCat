package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const oidcCookieName = "vocat_oidc"
const oidcFlowTTL = 10 * time.Minute

type oidcFlow struct {
	Binding, Nonce, Verifier, ReturnTo string
	ExpiresAt                          time.Time
}

type oidcFlows struct {
	mu    sync.Mutex
	flows map[string]oidcFlow
}

func safeReturnTo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.HasPrefix(u.Path, "//") || strings.ContainsAny(u.Path, "\\\r\n") || strings.ContainsAny(raw, "\\\r\n") || strings.HasPrefix(u.Path, "/api/") || u.Path == "/login" {
		return "/"
	}
	return u.String()
}

func flowToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"provider": "pocket-id", "enabled": s.oidc != nil}})
}

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if s.oidc == nil {
		writeError(w, http.StatusServiceUnavailable, "oidc_unconfigured", "Pocket ID is not configured")
		return
	}
	state, err := flowToken()
	if err != nil {
		writeError(w, 500, "internal_error", "cannot start login")
		return
	}
	binding, err := flowToken()
	if err != nil {
		writeError(w, 500, "internal_error", "cannot start login")
		return
	}
	nonce, err := flowToken()
	if err != nil {
		writeError(w, 500, "internal_error", "cannot start login")
		return
	}
	verifier, err := flowToken()
	if err != nil {
		writeError(w, 500, "internal_error", "cannot start login")
		return
	}
	s.oidcFlows.mu.Lock()
	if s.oidcFlows.flows == nil {
		s.oidcFlows.flows = make(map[string]oidcFlow)
	}
	for key, flow := range s.oidcFlows.flows {
		if !flow.ExpiresAt.After(time.Now()) {
			delete(s.oidcFlows.flows, key)
		}
	}
	if len(s.oidcFlows.flows) >= 1024 {
		s.oidcFlows.mu.Unlock()
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "please try again later")
		return
	}
	s.oidcFlows.flows[state] = oidcFlow{Binding: binding, Nonce: nonce, Verifier: verifier, ReturnTo: safeReturnTo(r.URL.Query().Get("redirect")), ExpiresAt: time.Now().Add(oidcFlowTTL)}
	s.oidcFlows.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: oidcCookieName, Value: binding, Path: "/api/auth/oidc", MaxAge: int(oidcFlowTTL.Seconds()), HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, s.oidc.AuthorizationURL(state, nonce, verifier), http.StatusSeeOther)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	fail := func() { http.Redirect(w, r, "/login?error=oidc_failed", http.StatusSeeOther) }
	if s.oidc == nil {
		fail()
		return
	}
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(oidcCookieName)
	if err != nil || cookie.Value == "" || state == "" {
		fail()
		return
	}
	s.oidcFlows.mu.Lock()
	flow, ok := s.oidcFlows.flows[state]
	valid := ok && flow.ExpiresAt.After(time.Now()) && subtle.ConstantTimeCompare([]byte(flow.Binding), []byte(cookie.Value)) == 1
	if valid {
		delete(s.oidcFlows.flows, state)
	}
	s.oidcFlows.mu.Unlock()
	if !valid {
		fail()
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcCookieName, Path: "/api/auth/oidc", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode})
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		fail()
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	identity, err := s.oidc.Exchange(ctx, r.URL.Query().Get("code"), flow.Nonce, flow.Verifier)
	if err != nil {
		s.auditAuth(r, "", "failure")
		fail()
		return
	}
	credentials, err := s.auth.LoginOIDC(ctx, identity)
	if err != nil {
		s.auditAuth(r, identity.Username, "failure")
		fail()
		return
	}
	// Replace any pre-existing session instead of leaving a stale token usable.
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_ = s.auth.Logout(ctx, cookie.Value)
	}
	s.setAuthCookies(w, credentials.SessionToken, credentials.CSRFToken, credentials.ExpiresAt)
	s.auditAuth(r, identity.Username, "success")
	http.Redirect(w, r, flow.ReturnTo, http.StatusSeeOther)
}
