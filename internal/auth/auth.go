// Package auth provides "Log in with Hack Club" authentication for the app:
// OAuth handlers, signed-cookie sessions, an email whitelist, and middleware
// that gates routes behind a logged-in, allow-listed user.
package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/hackclub/fillout-ysws-nps/hcauth"
)

const (
	pendingCookie = "hc_oauth"
	sessionCookie = "hc_session"

	pendingTTL = 10 * time.Minute
	sessionTTL = 7 * 24 * time.Hour
)

// User is the authenticated principal stored in the session and request context.
type User struct {
	Email string `json:"email"`
}

type ctxKey int

const userCtxKey ctxKey = 0

// pendingAuth is the short-lived state stashed between Login and Callback.
type pendingAuth struct {
	State     string `json:"state"`
	Nonce     string `json:"nonce"`
	Verifier  string `json:"verifier"`
	ExpiresAt int64  `json:"exp"`
}

// session is the logged-in user state stored in the session cookie.
type session struct {
	Email     string `json:"email"`
	ExpiresAt int64  `json:"exp"`
}

// Authenticator wires the Hack Club OIDC client to signed-cookie sessions and an
// email allow-list.
type Authenticator struct {
	oidc    *hcauth.Client
	signer  *signer
	allowed func(string) bool
	secure  bool
	now     func() time.Time
}

// New builds an Authenticator. secret signs cookies; allowed reports whether an
// email may log in; secure marks cookies Secure (set false for plain-HTTP local
// dev).
func New(oidc *hcauth.Client, secret []byte, allowed func(string) bool, secure bool) *Authenticator {
	return &Authenticator{
		oidc:    oidc,
		signer:  newSigner(secret),
		allowed: allowed,
		secure:  secure,
		now:     time.Now,
	}
}

// Login starts the OAuth flow: it generates state/nonce/PKCE, stashes them in a
// short-lived signed cookie, and redirects to Hack Club's authorize endpoint.
func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request) {
	req, err := a.oidc.AuthCodeURL()
	if err != nil {
		http.Error(w, "could not start login", http.StatusInternalServerError)
		return
	}
	a.setCookie(w, pendingCookie, pendingAuth{
		State:     req.State,
		Nonce:     req.Nonce,
		Verifier:  req.CodeVerifier,
		ExpiresAt: a.now().Add(pendingTTL).Unix(),
	}, pendingTTL)
	http.Redirect(w, r, req.URL, http.StatusFound)
}

// Callback completes the OAuth flow: it verifies state, exchanges the code,
// fetches the profile, enforces the whitelist, and sets the session cookie.
func (a *Authenticator) Callback(w http.ResponseWriter, r *http.Request) {
	var pending pendingAuth
	if err := a.readCookie(r, pendingCookie, &pending); err != nil || pending.ExpiresAt < a.now().Unix() {
		http.Error(w, "login session expired — please try again", http.StatusBadRequest)
		return
	}
	a.clearCookie(w, pendingCookie)

	state := r.URL.Query().Get("state")
	if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(pending.State)) != 1 {
		http.Error(w, "invalid login state", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	token, err := a.oidc.Exchange(r.Context(), code, pending.Verifier)
	if err != nil {
		http.Error(w, "could not complete login (token exchange failed)", http.StatusBadGateway)
		return
	}
	info, err := a.oidc.UserInfo(r.Context(), token.AccessToken)
	if err != nil {
		http.Error(w, "could not complete login (profile fetch failed)", http.StatusBadGateway)
		return
	}

	if !a.allowed(info.Email) {
		a.renderForbidden(w, info.Email)
		return
	}

	a.setCookie(w, sessionCookie, session{
		Email:     strings.ToLower(strings.TrimSpace(info.Email)),
		ExpiresAt: a.now().Add(sessionTTL).Unix(),
	}, sessionTTL)
	http.Redirect(w, r, "/", http.StatusFound)
}

// Logout clears the session cookie.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	a.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/", http.StatusFound)
}

// RequireUser is middleware that redirects unauthenticated requests to /login
// and otherwise injects the User into the request context.
func (a *Authenticator) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.CurrentUser(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// CurrentUser returns the logged-in user from the session cookie, if any. It
// re-checks the whitelist so revoking access takes effect on the next request.
func (a *Authenticator) CurrentUser(r *http.Request) (User, bool) {
	var s session
	if err := a.readCookie(r, sessionCookie, &s); err != nil {
		return User{}, false
	}
	if s.ExpiresAt < a.now().Unix() || !a.allowed(s.Email) {
		return User{}, false
	}
	return User{Email: s.Email}, true
}

// UserFromContext returns the User injected by RequireUser.
func UserFromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userCtxKey).(User)
	return u, ok
}

func (a *Authenticator) renderForbidden(w http.ResponseWriter, email string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Access denied</title>` +
		`<body style="font-family:system-ui;max-width:32rem;margin:4rem auto;text-align:center">` +
		`<h1>Access denied</h1><p><strong>` + html.EscapeString(email) +
		`</strong> is not on the allow-list for this tool.</p>` +
		`<p>Ask an admin to add your email to the <code>Hack Club Auth Email</code> ` +
		`field in the YSWS Authors table (or to <code>ALLOWED_EMAILS</code>).</p>` +
		`<p><a href="/logout">Sign out</a></p></body>`))
}

func (a *Authenticator) setCookie(w http.ResponseWriter, name string, v any, ttl time.Duration) {
	payload, err := json.Marshal(v)
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    a.signer.sign(payload),
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  a.now().Add(ttl),
		MaxAge:   int(ttl.Seconds()),
	})
}

func (a *Authenticator) readCookie(r *http.Request, name string, dst any) error {
	c, err := r.Cookie(name)
	if err != nil {
		return err
	}
	payload, err := a.signer.unsign(c.Value)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, dst)
}

func (a *Authenticator) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
