package hcauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Token holds the credentials returned by the token endpoint.
//
// Hack Club access tokens are long-lived (about six months) and refresh tokens
// rotate: every successful [Client.Refresh] returns a new refresh token and
// invalidates the previous one, so callers must persist the latest value.
type Token struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	// IDToken is the raw OIDC ID token (a JWT), present when the "openid" scope
	// was granted. Verify it with [Client.VerifyIDToken].
	IDToken string `json:"id_token"`
	Scope   string `json:"scope"`
	// ExpiresIn is the access token lifetime in seconds as reported by the
	// server.
	ExpiresIn int64 `json:"expires_in"`
	// Expiry is the absolute time the access token expires, computed from
	// ExpiresIn when the token was issued. Zero if the server omitted
	// expires_in.
	Expiry time.Time `json:"-"`
}

// Expired reports whether the access token has passed (or is within 10s of) its
// expiry. It always returns false when the expiry is unknown.
func (t *Token) Expired() bool {
	if t == nil || t.Expiry.IsZero() {
		return false
	}
	return time.Now().Add(10 * time.Second).After(t.Expiry)
}

// Exchange trades an authorization code from the callback for tokens. Pass the
// CodeVerifier from the [AuthRequest] that began the login; pass "" if PKCE was
// disabled.
func (c *Client) Exchange(ctx context.Context, code, codeVerifier string) (*Token, error) {
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("hcauth: authorization code is required")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.redirectURL)
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}
	return c.tokenRequest(ctx, form)
}

// Refresh obtains a fresh access token using a refresh token. The returned
// Token carries a new refresh token that replaces the one passed in.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("hcauth: refresh token is required")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	return c.tokenRequest(ctx, form)
}

// tokenRequest posts a form to the token endpoint using client_secret_post
// authentication (client credentials in the body, as documented by Hack Club)
// and decodes the JSON token response.
func (c *Client) tokenRequest(ctx context.Context, form url.Values) (*Token, error) {
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("hcauth: building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hcauth: sending token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("hcauth: reading token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError(resp.StatusCode, body)
	}

	var tok Token
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("hcauth: decoding token response: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("hcauth: token response missing access_token")
	}
	if tok.ExpiresIn > 0 {
		tok.Expiry = c.now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	return &tok, nil
}
