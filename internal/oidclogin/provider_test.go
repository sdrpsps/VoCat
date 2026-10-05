package oidclogin

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
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

func TestAuthorizationAndIDTokenVerification(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "wrong_nonce", "wrong_audience", "wrong_issuer", "expired", "wrong_signature", "missing_subject", "missing_id_token"} {
		t.Run(mode, func(t *testing.T) {
			var issuer *httptest.Server
			issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
				case "/jwks":
					json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
				case "/token":
					r.ParseForm()
					if r.Form.Get("code") != "test-code" || r.Form.Get("code_verifier") != strings.Repeat("v", 43) || r.Form.Get("redirect_uri") != "http://127.0.0.1/api/auth/oidc/callback" {
						t.Error("code, PKCE verifier or callback not passed to token endpoint")
					}
					clientID, secret, _ := r.BasicAuth()
					if clientID != "vocat-client" || secret != "test-secret" {
						t.Error("client authentication missing")
					}
					claims := map[string]any{"iss": issuer.URL, "sub": "user-123", "aud": "vocat-client", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": "test-nonce", "preferred_username": "alice"}
					signingKey := key
					switch mode {
					case "wrong_nonce":
						claims["nonce"] = "wrong"
					case "wrong_audience":
						claims["aud"] = "other-client"
					case "wrong_issuer":
						claims["iss"] = "https://other.example"
					case "expired":
						claims["exp"] = time.Now().Add(-time.Hour).Unix()
					case "missing_subject":
						delete(claims, "sub")
					case "wrong_signature":
						signingKey, _ = rsa.GenerateKey(rand.Reader, 2048)
					}
					signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signingKey}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
					if err != nil {
						t.Error(err)
						return
					}
					payload, _ := json.Marshal(claims)
					signed, _ := signer.Sign(payload)
					raw, _ := signed.CompactSerialize()
					response := map[string]any{"access_token": "test-access", "token_type": "Bearer", "id_token": raw}
					if mode == "missing_id_token" {
						delete(response, "id_token")
					}
					json.NewEncoder(w).Encode(response)
				default:
					http.NotFound(w, r)
				}
			}))
			defer issuer.Close()
			p, err := New(context.Background(), Config{Issuer: issuer.URL, ClientID: "vocat-client", ClientSecret: "test-secret", RedirectURL: "http://127.0.0.1/api/auth/oidc/callback"})
			if err != nil {
				t.Fatal(err)
			}
			verifier := strings.Repeat("v", 43)
			u, _ := url.Parse(p.AuthorizationURL("test-state", "test-nonce", verifier))
			challenge := sha256.Sum256([]byte(verifier))
			q := u.Query()
			if q.Get("state") != "test-state" || q.Get("nonce") != "test-nonce" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) || q.Get("response_type") != "code" {
				t.Fatalf("unsafe authorization request: %v", q)
			}
			identity, err := p.Exchange(context.Background(), "test-code", "test-nonce", verifier)
			if mode != "valid" {
				if err == nil {
					t.Fatalf("accepted %s token", mode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if identity.Subject != "user-123" || identity.Username != "alice" || identity.Issuer != issuer.URL {
				t.Fatalf("identity = %+v", identity)
			}
		})
	}
}

func TestConfigurationRequiresSecureExactCallback(t *testing.T) {
	c := Config{Issuer: "https://auth.bytespark.app", ClientID: "client", RedirectURL: "https://vo.0o.tn/api/auth/oidc/callback"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "http://vo.0o.tn/api/auth/oidc/callback", "https://vo.0o.tn/other", "https://user:pass@vo.0o.tn/api/auth/oidc/callback", "https://vo.0o.tn/api/auth/oidc/callback?next=evil"} {
		c.RedirectURL = value
		if err := c.Validate(); err == nil {
			t.Errorf("accepted redirect %q", value)
		}
	}
}
