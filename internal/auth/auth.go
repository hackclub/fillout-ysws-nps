// Package auth provides "Log in with Hack Club" authentication for the app:
// OAuth handlers, signed-cookie sessions, an email whitelist, and middleware
// that gates routes behind a logged-in, allow-listed user.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"html"
	"net/http"
	"strconv"
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
	CSRFSeed  string `json:"csrf,omitempty"`
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

	// The email is the sole identity and authorization key, so it must be one
	// the provider confirms the user controls. An unverified email could be set
	// to any address; trusting it would let a user log in as any allow-listed
	// person. Per OIDC, treat the email claim as authoritative only when
	// email_verified is true.
	if !info.EmailVerified {
		a.renderUnverified(w, info.Email)
		return
	}

	if !a.allowed(info.Email) {
		a.renderForbidden(w, info.Email)
		return
	}
	csrfSeed, err := randomToken()
	if err != nil {
		http.Error(w, "could not complete login (session setup failed)", http.StatusInternalServerError)
		return
	}

	a.setCookie(w, sessionCookie, session{
		Email:     strings.ToLower(strings.TrimSpace(info.Email)),
		ExpiresAt: a.now().Add(sessionTTL).Unix(),
		CSRFSeed:  csrfSeed,
	}, sessionTTL)
	http.Redirect(w, r, "/", http.StatusFound)
}

// Logout clears the session cookie.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	a.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/", http.StatusFound)
}

// RequireUser is middleware that redirects unauthenticated requests to /login
// and otherwise injects the User into the request context. On state-changing
// (non-safe) methods it also enforces a CSRF token bound to the session.
func (a *Authenticator) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := a.currentSession(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		user := User{Email: sess.Email}
		// Defense-in-depth CSRF check on mutating requests; SameSite=Lax cookies
		// are the first line. The token is derived from the signing key and bound
		// to the session, so a cross-site form cannot supply a valid one.
		if !safeMethod(r.Method) && !a.validCSRF(r, sess) {
			http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// safeMethod reports whether m is a read-only HTTP method that needs no CSRF
// check.
func safeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// CSRFToken returns the CSRF token bound to the current session, or "" when
// there is no valid session. Embed it as a hidden "csrf_token" field in every
// state-changing form; RequireUser rejects unsafe-method requests whose token
// is missing or does not match.
func (a *Authenticator) CSRFToken(r *http.Request) string {
	sess, ok := a.currentSession(r)
	if !ok {
		return ""
	}
	return a.csrfToken(sess)
}

// csrfToken derives a stable, unforgeable token for a session from the cookie
// signing key. It is deterministic for a given session (so any form rendered
// during the session stays valid) yet unknowable to a cross-site attacker, who
// lacks the secret — exactly the property CSRF protection needs.
func (a *Authenticator) csrfToken(sess session) string {
	email := strings.ToLower(strings.TrimSpace(sess.Email))
	return encodeSegment(a.signer.mac([]byte("csrf:" + email + ":" + strconv.FormatInt(sess.ExpiresAt, 10) + ":" + sess.CSRFSeed)))
}

// validCSRF reports whether the request body carries the CSRF token matching
// sess. The token is read only from the POST body, never the query string.
func (a *Authenticator) validCSRF(r *http.Request, sess session) bool {
	got := r.PostFormValue("csrf_token")
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(a.csrfToken(sess))) == 1
}

func (a *Authenticator) currentSession(r *http.Request) (session, bool) {
	var s session
	if err := a.readCookie(r, sessionCookie, &s); err != nil {
		return session{}, false
	}
	if s.ExpiresAt < a.now().Unix() || !a.allowed(s.Email) {
		return session{}, false
	}
	return s, true
}

// CurrentUser returns the logged-in user from the session cookie, if any. It
// re-checks the whitelist so revoking access takes effect on the next request.
func (a *Authenticator) CurrentUser(r *http.Request) (User, bool) {
	s, ok := a.currentSession(r)
	if !ok {
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
	a.denyLogin(w, email,
		`is not on the allow-list for this tool.</p>`+
			`<p>Ask an admin to add your email to the <code>Hack Club Auth Email</code> `+
			`field in the YSWS Authors table (or to <code>ALLOWED_EMAILS</code>).`)
}

// renderUnverified denies a login whose email the provider has not verified.
func (a *Authenticator) renderUnverified(w http.ResponseWriter, email string) {
	a.denyLogin(w, email,
		`has not been verified with Hack Club, so it can't be used to sign in.</p>`+
			`<p>Verify your email address with Hack Club, then try again.`)
}

// denyLogin writes the shared 403 "access denied" page. reasonHTML is trusted
// markup describing why access was denied; only email is attacker-influenced,
// and it is HTML-escaped.
func (a *Authenticator) denyLogin(w http.ResponseWriter, email, reasonHTML string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Access denied</title>` +
		`<body style="font-family:system-ui;max-width:32rem;margin:4rem auto;text-align:center">` +
		`<h1>Access denied</h1><p><strong>` + html.EscapeString(email) + `</strong> ` + reasonHTML +
		`</p><p><a href="/logout">Sign out</a></p></body>`))
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return encodeSegment(b), nil
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
