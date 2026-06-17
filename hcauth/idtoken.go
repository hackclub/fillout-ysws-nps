package hcauth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// clockSkew is the tolerance applied to time-based ID token claims.
const clockSkew = 2 * time.Minute

// IDToken holds the validated claims of an OIDC ID token.
type IDToken struct {
	Issuer   string
	Subject  string
	Audience []string
	Expiry   time.Time
	IssuedAt time.Time
	Nonce    string
	Email    string
	Name     string

	// Claims holds every claim in the token payload for access to fields not
	// modeled above.
	Claims map[string]json.RawMessage
}

// VerifyIDToken parses rawIDToken, verifies its RS256 signature against the
// provider's JWKS, and checks the issuer, audience and expiry. When
// expectedNonce is non-empty the token's nonce must match it; pass the Nonce
// from the [AuthRequest] that began the login.
//
// On success it returns the validated claims. Only RS256 is accepted.
func (c *Client) VerifyIDToken(ctx context.Context, rawIDToken, expectedNonce string) (*IDToken, error) {
	parts := strings.Split(rawIDToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("hcauth: id token is not a JWT (expected 3 parts, got %d)", len(parts))
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return nil, fmt.Errorf("hcauth: decoding id token header: %w", err)
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("hcauth: unsupported id token alg %q (want RS256)", header.Alg)
	}

	key, err := c.publicKey(ctx, header.Kid)
	if err != nil {
		return nil, err
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("hcauth: decoding id token signature: %w", err)
	}
	signingInput := parts[0] + "." + parts[1]
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return nil, fmt.Errorf("hcauth: id token signature verification failed: %w", err)
	}

	var claims struct {
		Issuer   string      `json:"iss"`
		Subject  string      `json:"sub"`
		Audience audience    `json:"aud"`
		Expiry   numericDate `json:"exp"`
		IssuedAt numericDate `json:"iat"`
		Nonce    string      `json:"nonce"`
		Email    string      `json:"email"`
		Name     string      `json:"name"`
	}
	if err := decodeSegment(parts[1], &claims); err != nil {
		return nil, fmt.Errorf("hcauth: decoding id token claims: %w", err)
	}

	if claims.Issuer != c.issuer {
		return nil, fmt.Errorf("hcauth: id token issuer %q does not match %q", claims.Issuer, c.issuer)
	}
	if !containsString(claims.Audience, c.clientID) {
		return nil, fmt.Errorf("hcauth: id token audience %v does not include client id", []string(claims.Audience))
	}
	now := c.now()
	if claims.Expiry.IsZero() {
		return nil, fmt.Errorf("hcauth: id token is missing exp")
	}
	if now.After(claims.Expiry.Add(clockSkew)) {
		return nil, fmt.Errorf("hcauth: id token expired at %s", claims.Expiry.Format(time.RFC3339))
	}
	if !claims.IssuedAt.IsZero() && claims.IssuedAt.After(now.Add(clockSkew)) {
		return nil, fmt.Errorf("hcauth: id token issued in the future (%s)", claims.IssuedAt.Format(time.RFC3339))
	}
	if expectedNonce != "" && claims.Nonce != expectedNonce {
		return nil, fmt.Errorf("hcauth: id token nonce mismatch")
	}

	rawClaims := map[string]json.RawMessage{}
	if err := decodeSegment(parts[1], &rawClaims); err != nil {
		return nil, fmt.Errorf("hcauth: decoding id token claim map: %w", err)
	}

	return &IDToken{
		Issuer:   claims.Issuer,
		Subject:  claims.Subject,
		Audience: claims.Audience,
		Expiry:   claims.Expiry.Time(),
		IssuedAt: claims.IssuedAt.Time(),
		Nonce:    claims.Nonce,
		Email:    claims.Email,
		Name:     claims.Name,
		Claims:   rawClaims,
	}, nil
}

// decodeSegment base64url-decodes a JWT segment and unmarshals the JSON into v.
func decodeSegment(segment string, v any) error {
	data, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// audience decodes the JWT "aud" claim, which may be a single string or an
// array of strings.
type audience []string

func (a *audience) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*a = []string{single}
		return nil
	}
	var multiple []string
	if err := json.Unmarshal(data, &multiple); err != nil {
		return fmt.Errorf("hcauth: aud claim is neither string nor array: %w", err)
	}
	*a = multiple
	return nil
}

// numericDate decodes a JWT NumericDate (seconds since the Unix epoch).
type numericDate struct{ t time.Time }

func (d *numericDate) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var seconds float64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return fmt.Errorf("hcauth: invalid numeric date: %w", err)
	}
	d.t = time.Unix(int64(seconds), 0).UTC()
	return nil
}

func (d numericDate) IsZero() bool                  { return d.t.IsZero() }
func (d numericDate) Time() time.Time               { return d.t }
func (d numericDate) Add(x time.Duration) time.Time { return d.t.Add(x) }
func (d numericDate) After(t time.Time) bool        { return d.t.After(t) }
func (d numericDate) Format(layout string) string   { return d.t.Format(layout) }
