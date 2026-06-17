package hcauth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newMockClient returns a Client whose endpoints all point at a test server
// backed by mux, with the issuer set to the server URL.
func newMockClient(t *testing.T, mux *http.ServeMux, opts ...Option) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	base := []Option{
		WithIssuer(srv.URL),
		WithEndpoints(
			srv.URL+"/oauth/authorize",
			srv.URL+"/oauth/token",
			srv.URL+"/oauth/userinfo",
			srv.URL+"/oauth/discovery/keys",
		),
	}
	c, err := New("client-123", "secret-456", "http://localhost:3000/callback", append(base, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

func TestNewValidatesRequiredArgs(t *testing.T) {
	cases := map[string][3]string{
		"missing client id":     {"", "secret", "http://localhost:3000/callback"},
		"missing client secret": {"id", "", "http://localhost:3000/callback"},
		"missing redirect url":  {"id", "secret", "  "},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(args[0], args[1], args[2]); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}

	if _, err := New("id", "secret", "http://localhost:3000/callback"); err != nil {
		t.Fatalf("unexpected error for valid args: %v", err)
	}
}

func TestNewDefaults(t *testing.T) {
	c, err := New("id", "secret", "http://localhost:3000/callback")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.Scopes(); strings.Join(got, " ") != strings.Join(DefaultScopes, " ") {
		t.Errorf("scopes = %v, want %v", got, DefaultScopes)
	}
	if c.authURL != DefaultAuthURL || c.tokenURL != DefaultTokenURL || c.userInfoURL != DefaultUserInfoURL || c.jwksURL != DefaultJWKSURL {
		t.Error("default endpoints were not applied")
	}
	if c.issuer != DefaultIssuer {
		t.Errorf("issuer = %q, want %q", c.issuer, DefaultIssuer)
	}
}

func TestScopesReturnsCopy(t *testing.T) {
	c, _ := New("id", "secret", "http://localhost:3000/callback", WithScopes("openid", "email"))
	got := c.Scopes()
	got[0] = "mutated"
	if c.Scopes()[0] != "openid" {
		t.Error("Scopes() exposed internal slice; mutation leaked back into the client")
	}
}

func TestAuthCodeURL(t *testing.T) {
	c, _ := New("client-123", "secret", "http://localhost:3000/callback", WithScopes("openid", "email", "name", "profile"))

	ar, err := c.AuthCodeURL()
	if err != nil {
		t.Fatalf("AuthCodeURL: %v", err)
	}
	if ar.State == "" || ar.Nonce == "" || ar.CodeVerifier == "" {
		t.Fatalf("expected state/nonce/verifier to be set, got %+v", ar)
	}

	u, err := url.Parse(ar.URL)
	if err != nil {
		t.Fatalf("parsing auth url: %v", err)
	}
	if u.Scheme != "https" || u.Host != "auth.hackclub.com" || u.Path != "/oauth/authorize" {
		t.Errorf("unexpected base url: %s", ar.URL)
	}

	q := u.Query()
	checks := map[string]string{
		"client_id":             "client-123",
		"redirect_uri":          "http://localhost:3000/callback",
		"response_type":         "code",
		"scope":                 "openid email name profile",
		"state":                 ar.State,
		"nonce":                 ar.Nonce,
		"code_challenge_method": "S256",
	}
	for key, want := range checks {
		if got := q.Get(key); got != want {
			t.Errorf("query %q = %q, want %q", key, got, want)
		}
	}
	if got := q.Get("code_challenge"); got != pkceChallenge(ar.CodeVerifier) {
		t.Errorf("code_challenge = %q, want S256(verifier) = %q", got, pkceChallenge(ar.CodeVerifier))
	}
}

func TestAuthCodeURLIsRandomPerCall(t *testing.T) {
	c, _ := New("id", "secret", "http://localhost:3000/callback")
	a, err := c.AuthCodeURL()
	if err != nil {
		t.Fatalf("AuthCodeURL: %v", err)
	}
	b, err := c.AuthCodeURL()
	if err != nil {
		t.Fatalf("AuthCodeURL: %v", err)
	}
	if a.State == b.State || a.Nonce == b.Nonce || a.CodeVerifier == b.CodeVerifier {
		t.Error("expected fresh randomness on each AuthCodeURL call")
	}
}

func TestAuthCodeURLWithoutPKCE(t *testing.T) {
	c, _ := New("id", "secret", "http://localhost:3000/callback", WithPKCE(false))
	ar, err := c.AuthCodeURL()
	if err != nil {
		t.Fatalf("AuthCodeURL: %v", err)
	}
	if ar.CodeVerifier != "" {
		t.Errorf("expected empty CodeVerifier with PKCE disabled, got %q", ar.CodeVerifier)
	}
	u, _ := url.Parse(ar.URL)
	if u.Query().Has("code_challenge") {
		t.Error("expected no code_challenge with PKCE disabled")
	}
}
