package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/hackclub/fillout-ysws-nps/hcauth"
)

func allowAll(string) bool { return true }

// stubOIDC stands up a fake Hack Club token + userinfo server and returns an
// hcauth client pointed at it. The userinfo endpoint reports the given email.
func stubOIDC(t *testing.T, email string) (*hcauth.Client, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})
	mux.HandleFunc("/oauth/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sub":   "user-sub-1",
			"email": email,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := hcauth.New("client-id", "client-secret", "http://app.test/callback",
		hcauth.WithEndpoints(srv.URL+"/oauth/authorize", srv.URL+"/oauth/token", srv.URL+"/oauth/userinfo", srv.URL+"/oauth/keys"),
	)
	if err != nil {
		t.Fatalf("hcauth.New: %v", err)
	}
	return client, srv
}

func cookieByName(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestRequireUser_RedirectsWhenAnonymous(t *testing.T) {
	a := New(nil, []byte("secret"), allowAll, false)
	handler := a.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not run for anonymous request")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/jobs", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestRequireUser_AllowsValidSession(t *testing.T) {
	a := New(nil, []byte("secret"), allowAll, false)

	// Forge a valid session cookie via setCookie.
	setRec := httptest.NewRecorder()
	a.setCookie(setRec, sessionCookie, session{Email: "zach@hackclub.com", ExpiresAt: time.Now().Add(time.Hour).Unix()}, sessionTTL)
	sessCookie := cookieByName(setRec.Result(), sessionCookie)
	if sessCookie == nil {
		t.Fatal("no session cookie set")
	}

	var sawEmail string
	handler := a.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		if !ok {
			t.Fatal("UserFromContext: not found")
		}
		sawEmail = u.Email
	}))

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.AddCookie(sessCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if sawEmail != "zach@hackclub.com" {
		t.Errorf("context email = %q", sawEmail)
	}
}

func TestCurrentUser_RejectsExpiredOrDisallowed(t *testing.T) {
	mk := func(allowed func(string) bool, exp int64) (*Authenticator, *http.Cookie) {
		a := New(nil, []byte("secret"), allowed, false)
		rec := httptest.NewRecorder()
		a.setCookie(rec, sessionCookie, session{Email: "zach@hackclub.com", ExpiresAt: exp}, sessionTTL)
		return a, cookieByName(rec.Result(), sessionCookie)
	}

	t.Run("expired", func(t *testing.T) {
		a, c := mk(allowAll, time.Now().Add(-time.Hour).Unix())
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(c)
		if _, ok := a.CurrentUser(req); ok {
			t.Error("expired session accepted")
		}
	})

	t.Run("no longer allowed", func(t *testing.T) {
		a, c := mk(func(string) bool { return false }, time.Now().Add(time.Hour).Unix())
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(c)
		if _, ok := a.CurrentUser(req); ok {
			t.Error("disallowed email accepted")
		}
	})
}

func TestCallback_StateMismatch(t *testing.T) {
	a := New(nil, []byte("secret"), allowAll, false)

	// Set a pending cookie with a known state.
	rec := httptest.NewRecorder()
	a.setCookie(rec, pendingCookie, pendingAuth{State: "expected-state", Verifier: "v", ExpiresAt: time.Now().Add(time.Minute).Unix()}, pendingTTL)
	pending := cookieByName(rec.Result(), pendingCookie)

	req := httptest.NewRequest(http.MethodGet, "/callback?state=WRONG&code=abc", nil)
	req.AddCookie(pending)
	out := httptest.NewRecorder()
	a.Callback(out, req)

	if out.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", out.Code)
	}
}

func TestCallback_HappyPathSetsSession(t *testing.T) {
	oidc, _ := stubOIDC(t, "zach@hackclub.com")
	a := New(oidc, []byte("secret"), allowAll, false)

	// Drive Login to get a matching pending cookie + state.
	loginRec := httptest.NewRecorder()
	a.Login(loginRec, httptest.NewRequest(http.MethodGet, "/login", nil))
	pending := cookieByName(loginRec.Result(), pendingCookie)
	if pending == nil {
		t.Fatal("Login set no pending cookie")
	}
	loc, err := url.Parse(loginRec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("no state in authorize redirect")
	}

	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=auth-code", nil)
	req.AddCookie(pending)
	out := httptest.NewRecorder()
	a.Callback(out, req)

	if out.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", out.Code, out.Body.String())
	}
	if loc := out.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /", loc)
	}
	if cookieByName(out.Result(), sessionCookie) == nil {
		t.Error("no session cookie set on success")
	}
}

func TestCallback_DisallowedEmailForbidden(t *testing.T) {
	oidc, _ := stubOIDC(t, "stranger@example.com")
	a := New(oidc, []byte("secret"), func(e string) bool { return e == "zach@hackclub.com" }, false)

	loginRec := httptest.NewRecorder()
	a.Login(loginRec, httptest.NewRequest(http.MethodGet, "/login", nil))
	pending := cookieByName(loginRec.Result(), pendingCookie)
	loc, _ := url.Parse(loginRec.Header().Get("Location"))
	state := loc.Query().Get("state")

	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=auth-code", nil)
	req.AddCookie(pending)
	out := httptest.NewRecorder()
	a.Callback(out, req)

	if out.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", out.Code)
	}
	if cookieByName(out.Result(), sessionCookie) != nil {
		t.Error("session cookie set for disallowed email")
	}
}
