package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hackclub/fillout-ysws-nps/hcauth"
)

func allowAll(string) bool { return true }

// stubOIDC stands up a fake Hack Club token + userinfo server and returns an
// hcauth client pointed at it. The userinfo endpoint reports the given email
// and email_verified flag.
func stubOIDC(t *testing.T, email string, emailVerified bool) (*hcauth.Client, *httptest.Server) {
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
			"sub":            "user-sub-1",
			"email":          email,
			"email_verified": emailVerified,
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
	oidc, _ := stubOIDC(t, "zach@hackclub.com", true)
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
	sessCookie := cookieByName(out.Result(), sessionCookie)
	if sessCookie == nil {
		t.Error("no session cookie set on success")
	}
	var sess session
	reqWithSession := httptest.NewRequest(http.MethodGet, "/", nil)
	reqWithSession.AddCookie(sessCookie)
	if err := a.readCookie(reqWithSession, sessionCookie, &sess); err != nil {
		t.Fatalf("read session cookie: %v", err)
	}
	if sess.CSRFSeed == "" {
		t.Error("session cookie has empty CSRF seed")
	}
}

func TestCallback_DisallowedEmailForbidden(t *testing.T) {
	oidc, _ := stubOIDC(t, "stranger@example.com", true)
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

func TestCallback_UnverifiedEmailForbidden(t *testing.T) {
	// allowAll would otherwise admit this email; the unverified flag must win.
	oidc, _ := stubOIDC(t, "zach@hackclub.com", false)
	a := New(oidc, []byte("secret"), allowAll, false)

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
		t.Fatalf("status = %d, want 403 (unverified email must be rejected)", out.Code)
	}
	if cookieByName(out.Result(), sessionCookie) != nil {
		t.Error("session cookie set for unverified email")
	}
}

func testSession(email, seed string) session {
	return session{Email: email, ExpiresAt: time.Now().Add(time.Hour).Unix(), CSRFSeed: seed}
}

func cookieForSession(t *testing.T, a *Authenticator, sess session) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	a.setCookie(rec, sessionCookie, sess, sessionTTL)
	c := cookieByName(rec.Result(), sessionCookie)
	if c == nil {
		t.Fatal("no session cookie forged")
	}
	return c
}

func TestRequireUser_EnforcesCSRFOnPOST(t *testing.T) {
	a := New(nil, []byte("secret"), allowAll, false)
	const email = "zach@hackclub.com"
	sess := testSession(email, "csrf-seed-1")
	sessCookie := cookieForSession(t, a, sess)

	var ran bool
	handler := a.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ran = true }))

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/sync/stop", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(sessCookie)
		rec := httptest.NewRecorder()
		ran = false
		handler.ServeHTTP(rec, req)
		return rec
	}

	t.Run("missing token rejected", func(t *testing.T) {
		rec := post("job_id=1")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		if ran {
			t.Error("next handler ran despite missing CSRF token")
		}
	})

	t.Run("wrong token rejected", func(t *testing.T) {
		rec := post("csrf_token=not-the-token&job_id=1")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		if ran {
			t.Error("next handler ran despite wrong CSRF token")
		}
	})

	t.Run("valid token accepted", func(t *testing.T) {
		rec := post("csrf_token=" + url.QueryEscape(a.csrfToken(sess)) + "&job_id=1")
		if rec.Code == http.StatusForbidden {
			t.Fatalf("valid CSRF token rejected (status %d)", rec.Code)
		}
		if !ran {
			t.Error("next handler did not run with a valid CSRF token")
		}
	})

	t.Run("query-string token rejected", func(t *testing.T) {
		// The token must come from the request body, not the URL query.
		req := httptest.NewRequest(http.MethodPost, "/sync/stop?csrf_token="+url.QueryEscape(a.csrfToken(sess)), strings.NewReader("job_id=1"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(sessCookie)
		rec := httptest.NewRecorder()
		ran = false
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (query-string token must not be accepted)", rec.Code)
		}
	})
}

func TestRequireUser_BindsCSRFToSession(t *testing.T) {
	a := New(nil, []byte("secret"), allowAll, false)
	const email = "zach@hackclub.com"
	sessA := testSession(email, "csrf-seed-a")
	sessB := testSession(email, "csrf-seed-b")
	cookieB := cookieForSession(t, a, sessB)

	if a.csrfToken(sessA) == a.csrfToken(sessB) {
		t.Fatal("CSRF token did not change between sessions for the same user")
	}

	var ran bool
	handler := a.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ran = true }))
	req := httptest.NewRequest(http.MethodPost, "/sync/stop", strings.NewReader("csrf_token="+url.QueryEscape(a.csrfToken(sessA))+"&job_id=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookieB)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for token from different session", rec.Code)
	}
	if ran {
		t.Error("next handler ran with a CSRF token from a different session")
	}
}

func TestCSRFToken_EmptyWithoutSession(t *testing.T) {
	a := New(nil, []byte("secret"), allowAll, false)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if tok := a.CSRFToken(req); tok != "" {
		t.Errorf("CSRFToken without session = %q, want empty", tok)
	}
}
