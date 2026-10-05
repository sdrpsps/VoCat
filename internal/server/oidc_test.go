package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"vocat/internal/auth"
	"vocat/internal/oidclogin"
)

func newOIDCApplication(t *testing.T) (testApplication, *url.URL) {
	t.Helper()
	app := newTestApplication(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var nonce, challenge string
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/authorize":
			nonce, challenge = r.URL.Query().Get("nonce"), r.URL.Query().Get("code_challenge")
			if r.URL.Query().Get("code_challenge_method") != "S256" {
				t.Error("missing S256 PKCE")
			}
			callback, _ := url.Parse(app.server.URL + "/api/auth/oidc/callback")
			callback.RawQuery = url.Values{"state": {r.URL.Query().Get("state")}, "code": {"test-code"}}.Encode()
			http.Redirect(w, r, callback.String(), http.StatusSeeOther)
		case "/jwks":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			r.ParseForm()
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if challenge == "" || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
				t.Error("invalid PKCE verifier")
			}
			signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
			payload, _ := json.Marshal(map[string]any{"iss": issuer.URL, "sub": "subject-alice", "aud": "client", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": nonce, "preferred_username": "alice"})
			signed, _ := signer.Sign(payload)
			raw, _ := signed.CompactSerialize()
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "token_type": "Bearer", "id_token": raw})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(issuer.Close)
	provider, err := oidclogin.New(context.Background(), oidclogin.Config{Issuer: issuer.URL, ClientID: "client", ClientSecret: "secret", RedirectURL: app.server.URL + "/api/auth/oidc/callback"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.New(app.handler.store, auth.Options{OIDCIssuer: issuer.URL, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	app.handler.oidc, app.handler.auth = provider, service
	u, _ := url.Parse(issuer.URL)
	return app, u
}

func TestOIDCLoginCreatesSessionAndPreservesCSRF(t *testing.T) {
	app, _ := newOIDCApplication(t)
	app.client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := app.client.Get(app.server.URL + "/api/auth/oidc/start?redirect=" + url.QueryEscape("/devices?tab=radio"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("start status = %d", response.StatusCode)
	}
	if cookies := response.Cookies(); len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("flow cookies = %v", cookies)
	}
	response, err = app.client.Get(response.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	callback := response.Header.Get("Location")
	response, err = app.client.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/devices?tab=radio" {
		t.Fatalf("callback status=%d location=%s", response.StatusCode, response.Header.Get("Location"))
	}
	response, err = app.client.Get(app.server.URL + "/api/auth/session")
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		Data struct {
			User      struct{ Username string }
			CSRFToken string `json:"csrf_token"`
		}
	}
	json.NewDecoder(response.Body).Decode(&session)
	response.Body.Close()
	if response.StatusCode != 200 || session.Data.User.Username != "alice" || session.Data.CSRFToken == "" {
		t.Fatalf("session=%+v status=%d", session, response.StatusCode)
	}
	response, err = app.client.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.Header.Get("Location") != "/login?error=oidc_failed" {
		t.Fatal("callback replay accepted")
	}
	request, _ := http.NewRequest(http.MethodPost, app.server.URL+"/api/auth/logout", nil)
	response, err = app.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("logout without CSRF accepted")
	}
	request, _ = http.NewRequest(http.MethodPost, app.server.URL+"/api/auth/logout", nil)
	request.Header.Set(csrfHeaderName, session.Data.CSRFToken)
	response, err = app.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("logout status=%d", response.StatusCode)
	}
	response, err = app.client.Get(app.server.URL + "/api/auth/session")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("logged-out session still authorized")
	}
}

func TestOIDCCallbackRejectsMissingBindingAndExpiredState(t *testing.T) {
	app, _ := newOIDCApplication(t)
	for _, mode := range []string{"missing_cookie", "wrong_cookie", "expired", "wrong_state", "provider_denied"} {
		t.Run(mode, func(t *testing.T) {
			flow := oidcFlow{Binding: "binding", ExpiresAt: time.Now().Add(time.Minute)}
			if mode == "expired" {
				flow.ExpiresAt = time.Now().Add(-time.Minute)
			}
			app.handler.oidcFlows.flows = map[string]oidcFlow{"state": flow}
			request := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?state=state&code=code", nil)
			if mode != "missing_cookie" {
				binding := "binding"
				if mode == "wrong_cookie" {
					binding = "wrong"
				}
				request.AddCookie(&http.Cookie{Name: oidcCookieName, Value: binding})
			}
			if mode == "wrong_state" {
				request.URL.RawQuery = "state=wrong&code=code"
			}
			if mode == "provider_denied" {
				request.URL.RawQuery = "state=state&error=access_denied"
			}
			recorder := httptest.NewRecorder()
			app.handler.handleOIDCCallback(recorder, request)
			if recorder.Header().Get("Location") != "/login?error=oidc_failed" {
				t.Fatal("invalid callback accepted")
			}
			for _, cookie := range recorder.Result().Cookies() {
				if cookie.Name == sessionCookieName && cookie.Value != "" {
					t.Fatal("invalid callback issued session")
				}
			}
		})
	}
}

func TestOIDCReturnToIsSameOrigin(t *testing.T) {
	for _, raw := range []string{"https://evil.example", "//evil.example", "/\\evil.example", "/%5cevil.example", "/%2f%2fevil.example", "/login", "/api/auth/oidc/start", "javascript:alert(1)", "/\r\nevil"} {
		if got := safeReturnTo(raw); got != "/" {
			t.Errorf("unsafe redirect %q accepted as %q", raw, got)
		}
	}
	if got := safeReturnTo("/devices?tab=radio#details"); got != "/devices?tab=radio#details" {
		t.Errorf("local deep link lost: %s", got)
	}
}

func TestPasswordChangeIsRemoved(t *testing.T) {
	app := newTestApplication(t)
	app.login(t).Body.Close()
	response, err := app.client.Get(app.server.URL + "/api/settings/password")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("removed password endpoint status=%d", response.StatusCode)
	}
}
