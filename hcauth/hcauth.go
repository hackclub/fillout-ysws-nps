// Package hcauth is a small, dependency-free client for "Log in with Hack
// Club" — the OpenID Connect provider at https://auth.hackclub.com.
//
// It implements the authorization-code flow with PKCE and covers the four
// steps an app needs to authenticate an account:
//
//  1. Build a login URL with [Client.AuthCodeURL] and redirect the user to it.
//     Persist the returned [AuthRequest] (state, nonce, code verifier) for the
//     life of the login — e.g. in a short-lived signed cookie or server session.
//  2. On the callback, reject the request unless the returned "state" matches
//     the value you stored, then trade the "code" for tokens with
//     [Client.Exchange], passing the stored code verifier.
//  3. Fetch the user's profile with [Client.UserInfo].
//  4. (Optional) If an ID token was returned, verify it with
//     [Client.VerifyIDToken], passing the stored nonce.
//
// The package depends only on the Go standard library.
//
// # Configuration
//
// Hack Club issues a client ID and secret per app and requires you to register
// each exact callback URL. A typical wiring reads them from the environment:
//
//	clientID := os.Getenv("HC_AUTH_CLIENT_ID")
//	secret := os.Getenv("HC_AUTH_CLIENT_SECRET")
//	redirect := strings.TrimRight(os.Getenv("HC_AUTH_CALLBACK_BASE_URL"), "/") + "/callback"
//
//	client, err := hcauth.New(clientID, secret, redirect)
//	if err != nil {
//		log.Fatal(err)
//	}
//
// The default scopes are openid, email, name and profile; override them with
// [WithScopes].
package hcauth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Default endpoints for the public Hack Club authorization server. Override
// them as a set with [WithIssuer] and [WithEndpoints] (useful in tests).
const (
	DefaultIssuer      = "https://auth.hackclub.com"
	DefaultAuthURL     = "https://auth.hackclub.com/oauth/authorize"
	DefaultTokenURL    = "https://auth.hackclub.com/oauth/token"
	DefaultUserInfoURL = "https://auth.hackclub.com/oauth/userinfo"
	DefaultJWKSURL     = "https://auth.hackclub.com/oauth/discovery/keys"
)

// DefaultScopes are requested when a Client is created without [WithScopes].
// "openid" yields an ID token; "email", "name" and "profile" populate the
// corresponding user-info claims.
var DefaultScopes = []string{"openid", "email", "name", "profile"}

// Client is an OAuth2/OIDC client for a single registered Hack Club app. A
// Client is safe for concurrent use by multiple goroutines.
type Client struct {
	clientID     string
	clientSecret string
	redirectURL  string
	scopes       []string
	usePKCE      bool

	issuer      string
	authURL     string
	tokenURL    string
	userInfoURL string
	jwksURL     string

	httpClient *http.Client
	// now is overridable in tests; defaults to time.Now.
	now func() time.Time

	jwksMu    sync.Mutex
	jwksCache map[string]*rsa.PublicKey
}

// Option configures a [Client].
type Option func(*Client)

// WithScopes sets the OAuth scopes requested at authorization time, replacing
// [DefaultScopes]. Passing no scopes is ignored.
func WithScopes(scopes ...string) Option {
	return func(c *Client) {
		if len(scopes) > 0 {
			c.scopes = append([]string(nil), scopes...)
		}
	}
}

// WithHTTPClient supplies a custom *http.Client (for timeouts, proxies, or a
// test transport).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.httpClient = h
		}
	}
}

// WithIssuer overrides the expected token issuer, used when validating ID
// tokens. Pair it with [WithEndpoints] when pointing at a non-default server.
func WithIssuer(issuer string) Option {
	return func(c *Client) { c.issuer = strings.TrimRight(issuer, "/") }
}

// WithEndpoints overrides the authorization, token, user-info, and JWKS URLs.
// Empty values keep the current setting.
func WithEndpoints(authURL, tokenURL, userInfoURL, jwksURL string) Option {
	return func(c *Client) {
		if authURL != "" {
			c.authURL = authURL
		}
		if tokenURL != "" {
			c.tokenURL = tokenURL
		}
		if userInfoURL != "" {
			c.userInfoURL = userInfoURL
		}
		if jwksURL != "" {
			c.jwksURL = jwksURL
		}
	}
}

// WithPKCE enables or disables PKCE (enabled by default). PKCE adds protection
// against authorization-code interception and is recommended; disable it only
// if the server rejects the extra parameters.
func WithPKCE(enabled bool) Option {
	return func(c *Client) { c.usePKCE = enabled }
}

// New returns a Client for the app identified by clientID/clientSecret.
// redirectURL must be the full callback URL registered with Hack Club (for
// example "http://localhost:3000/callback"). All three arguments are required.
func New(clientID, clientSecret, redirectURL string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(clientID) == "" {
		return nil, fmt.Errorf("hcauth: client id is required")
	}
	if strings.TrimSpace(clientSecret) == "" {
		return nil, fmt.Errorf("hcauth: client secret is required")
	}
	if strings.TrimSpace(redirectURL) == "" {
		return nil, fmt.Errorf("hcauth: redirect url is required")
	}

	c := &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURL:  redirectURL,
		scopes:       append([]string(nil), DefaultScopes...),
		usePKCE:      true,
		issuer:       DefaultIssuer,
		authURL:      DefaultAuthURL,
		tokenURL:     DefaultTokenURL,
		userInfoURL:  DefaultUserInfoURL,
		jwksURL:      DefaultJWKSURL,
		now:          time.Now,
		jwksCache:    make(map[string]*rsa.PublicKey),
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return c, nil
}

// Scopes returns a copy of the scopes the client requests.
func (c *Client) Scopes() []string {
	return append([]string(nil), c.scopes...)
}

// AuthRequest is the result of starting a login. Redirect the user to URL, and
// store the other fields until the callback so they can be checked there.
type AuthRequest struct {
	// URL is the authorization endpoint the user must be sent to.
	URL string
	// State is an opaque anti-CSRF token. On the callback, require that the
	// "state" query parameter equals this value before proceeding.
	State string
	// Nonce binds the eventual ID token to this login. Pass it to
	// [Client.VerifyIDToken].
	Nonce string
	// CodeVerifier is the PKCE secret. Pass it to [Client.Exchange]. Empty when
	// PKCE is disabled.
	CodeVerifier string
}

// AuthCodeURL begins a login: it generates fresh state, nonce and (unless PKCE
// is disabled) a PKCE verifier, and builds the authorization URL embedding the
// configured scopes and redirect URL.
func (c *Client) AuthCodeURL() (AuthRequest, error) {
	state, err := randomToken()
	if err != nil {
		return AuthRequest{}, fmt.Errorf("hcauth: generating state: %w", err)
	}
	nonce, err := randomToken()
	if err != nil {
		return AuthRequest{}, fmt.Errorf("hcauth: generating nonce: %w", err)
	}

	q := url.Values{}
	q.Set("client_id", c.clientID)
	q.Set("redirect_uri", c.redirectURL)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(c.scopes, " "))
	q.Set("state", state)
	q.Set("nonce", nonce)

	var verifier string
	if c.usePKCE {
		verifier, err = randomToken()
		if err != nil {
			return AuthRequest{}, fmt.Errorf("hcauth: generating code verifier: %w", err)
		}
		q.Set("code_challenge", pkceChallenge(verifier))
		q.Set("code_challenge_method", "S256")
	}

	u, err := url.Parse(c.authURL)
	if err != nil {
		return AuthRequest{}, fmt.Errorf("hcauth: invalid authorize url: %w", err)
	}
	u.RawQuery = q.Encode()

	return AuthRequest{
		URL:          u.String(),
		State:        state,
		Nonce:        nonce,
		CodeVerifier: verifier,
	}, nil
}

// randomToken returns 32 bytes of cryptographic randomness encoded as
// URL-safe base64 without padding (43 characters). The encoding is a valid
// PKCE code verifier per RFC 7636.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkceChallenge derives the S256 PKCE code challenge from a verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
