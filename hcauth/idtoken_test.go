package hcauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"sync"
	"testing"
	"time"
)

const testKID = "test-key-1"

var (
	keyOnce   sync.Once
	cachedKey *rsa.PrivateKey
)

// testKey returns a process-wide 2048-bit RSA key (generated once to keep the
// suite fast).
func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		cachedKey = k
	})
	return cachedKey
}

func jwksJSON(t *testing.T, kid string, pub *rsa.PublicKey) string {
	t.Helper()
	set := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"kid": kid,
			"alg": "RS256",
			"use": "sig",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	}
	b, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshaling jwks: %v", err)
	}
	return string(b)
}

// signJWT builds and RS256-signs a JWT with the given kid and claims.
func signJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	seg := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshaling segment: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
	signingInput := seg(header) + "." + seg(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("signing jwt: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// newIDTokenClient returns a client whose JWKS endpoint serves jwks, with the
// clock pinned to now.
func newIDTokenClient(t *testing.T, jwks string, now time.Time) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/discovery/keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jwks))
	})
	c, _ := newMockClient(t, mux)
	c.now = fixedClock(now)
	return c
}

// validClaims returns a baseline set of valid claims for the given client and
// clock that individual tests can mutate.
func validClaims(c *Client, now time.Time) map[string]any {
	return map[string]any{
		"iss":   c.issuer,
		"sub":   "user_42",
		"aud":   c.clientID,
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Add(-time.Minute).Unix(),
		"nonce": "nonce-123",
		"email": "fiona@hackclub.com",
		"name":  "Fiona Hacker",
	}
}

func TestVerifyIDToken(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	c := newIDTokenClient(t, jwksJSON(t, testKID, &key.PublicKey), now)

	raw := signJWT(t, key, testKID, validClaims(c, now))
	tok, err := c.VerifyIDToken(context.Background(), raw, "nonce-123")
	if err != nil {
		t.Fatalf("VerifyIDToken: %v", err)
	}
	if tok.Subject != "user_42" || tok.Email != "fiona@hackclub.com" || tok.Name != "Fiona Hacker" {
		t.Errorf("unexpected claims: %+v", tok)
	}
	if tok.Nonce != "nonce-123" {
		t.Errorf("nonce = %q", tok.Nonce)
	}
	if len(tok.Audience) != 1 || tok.Audience[0] != c.clientID {
		t.Errorf("audience = %v", tok.Audience)
	}
	if _, ok := tok.Claims["email"]; !ok {
		t.Error("Claims map should include email")
	}
}

func TestVerifyIDTokenAudienceArray(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	c := newIDTokenClient(t, jwksJSON(t, testKID, &key.PublicKey), now)

	claims := validClaims(c, now)
	claims["aud"] = []string{"someone-else", c.clientID}
	raw := signJWT(t, key, testKID, claims)

	if _, err := c.VerifyIDToken(context.Background(), raw, ""); err != nil {
		t.Fatalf("VerifyIDToken with audience array: %v", err)
	}
}

func TestVerifyIDTokenRejectsBadSignature(t *testing.T) {
	served := testKey(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	c := newIDTokenClient(t, jwksJSON(t, testKID, &served.PublicKey), now)

	// Sign with a different key than the one published in the JWKS.
	attacker, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating attacker key: %v", err)
	}
	raw := signJWT(t, attacker, testKID, validClaims(c, now))

	if _, err := c.VerifyIDToken(context.Background(), raw, ""); err == nil {
		t.Fatal("expected signature verification to fail")
	}
}

func TestVerifyIDTokenRejectsBadClaims(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	cases := map[string]func(map[string]any){
		"wrong issuer":   func(m map[string]any) { m["iss"] = "https://evil.example.com" },
		"wrong audience": func(m map[string]any) { m["aud"] = "another-client" },
		"expired":        func(m map[string]any) { m["exp"] = now.Add(-time.Hour).Unix() },
		"missing exp":    func(m map[string]any) { delete(m, "exp") },
		"future iat":     func(m map[string]any) { m["iat"] = now.Add(time.Hour).Unix() },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := newIDTokenClient(t, jwksJSON(t, testKID, &key.PublicKey), now)
			claims := validClaims(c, now)
			mutate(claims)
			raw := signJWT(t, key, testKID, claims)
			if _, err := c.VerifyIDToken(context.Background(), raw, ""); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestVerifyIDTokenNonceMismatch(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	c := newIDTokenClient(t, jwksJSON(t, testKID, &key.PublicKey), now)

	raw := signJWT(t, key, testKID, validClaims(c, now))
	if _, err := c.VerifyIDToken(context.Background(), raw, "different-nonce"); err == nil {
		t.Fatal("expected nonce mismatch error")
	}
}

func TestVerifyIDTokenUnknownKID(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	// JWKS publishes a different kid than the token is signed with.
	c := newIDTokenClient(t, jwksJSON(t, "some-other-kid", &key.PublicKey), now)

	raw := signJWT(t, key, testKID, validClaims(c, now))
	if _, err := c.VerifyIDToken(context.Background(), raw, ""); err == nil {
		t.Fatal("expected error for unknown kid")
	}
}

func TestVerifyIDTokenRejectsNonRS256(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	c := newIDTokenClient(t, jwksJSON(t, testKID, &key.PublicKey), now)

	// Hand-craft a token with alg "none".
	seg := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	raw := seg(map[string]any{"alg": "none", "typ": "JWT", "kid": testKID}) + "." + seg(validClaims(c, now)) + "."
	if _, err := c.VerifyIDToken(context.Background(), raw, ""); err == nil {
		t.Fatal("expected error for alg none")
	}
}

func TestVerifyIDTokenMalformed(t *testing.T) {
	c := newIDTokenClient(t, "{}", time.Now())
	if _, err := c.VerifyIDToken(context.Background(), "not-a-jwt", ""); err == nil {
		t.Fatal("expected error for malformed token")
	}
}

func TestVerifyIDTokenJWKSFetchError(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/discovery/keys", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c, _ := newMockClient(t, mux)
	c.now = fixedClock(now)

	raw := signJWT(t, key, testKID, validClaims(c, now))
	if _, err := c.VerifyIDToken(context.Background(), raw, ""); err == nil {
		t.Fatal("expected error when JWKS endpoint fails")
	}
}

func TestVerifyIDTokenMalformedJWKSKey(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/discovery/keys", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[{"kty":"RSA","kid":"test-key-1","n":"!!not-base64!!","e":"AQAB"}]}`))
	})
	c, _ := newMockClient(t, mux)
	c.now = fixedClock(now)

	raw := signJWT(t, key, testKID, validClaims(c, now))
	if _, err := c.VerifyIDToken(context.Background(), raw, ""); err == nil {
		t.Fatal("expected error for malformed JWKS key material")
	}
}

func TestVerifyIDTokenCachesKeys(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	var fetches int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/discovery/keys", func(w http.ResponseWriter, r *http.Request) {
		fetches++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jwksJSON(t, testKID, &key.PublicKey)))
	})
	c, _ := newMockClient(t, mux)
	c.now = fixedClock(now)

	for i := 0; i < 3; i++ {
		raw := signJWT(t, key, testKID, validClaims(c, now))
		if _, err := c.VerifyIDToken(context.Background(), raw, ""); err != nil {
			t.Fatalf("VerifyIDToken #%d: %v", i, err)
		}
	}
	if fetches != 1 {
		t.Errorf("JWKS fetched %d times, want 1 (keys should be cached)", fetches)
	}
}
