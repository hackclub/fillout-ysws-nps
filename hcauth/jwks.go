package hcauth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
)

// jwk is a single JSON Web Key. Only the RSA fields used for RS256 signature
// verification are modeled.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwkSet struct {
	Keys []jwk `json:"keys"`
}

// publicKey returns the RSA public key for kid, fetching (and caching) the JWKS
// the first time and re-fetching once if the key is not already cached — which
// covers key rotation.
func (c *Client) publicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.jwksMu.Lock()
	key, ok := c.jwksCache[kid]
	c.jwksMu.Unlock()
	if ok {
		return key, nil
	}

	if err := c.refreshJWKS(ctx); err != nil {
		return nil, err
	}

	c.jwksMu.Lock()
	key, ok = c.jwksCache[kid]
	c.jwksMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("hcauth: no signing key found for kid %q", kid)
	}
	return key, nil
}

// refreshJWKS fetches the key set and replaces the cache with the RSA keys it
// contains.
func (c *Client) refreshJWKS(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("hcauth: building jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("hcauth: fetching jwks: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("hcauth: reading jwks: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp.StatusCode, body)
	}

	var set jwkSet
	if err := json.Unmarshal(body, &set); err != nil {
		return fmt.Errorf("hcauth: decoding jwks: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := k.rsaPublicKey()
		if err != nil {
			return err
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return fmt.Errorf("hcauth: jwks contained no usable RSA keys")
	}

	c.jwksMu.Lock()
	c.jwksCache = keys
	c.jwksMu.Unlock()
	return nil
}

// rsaPublicKey builds an *rsa.PublicKey from the base64url-encoded modulus (n)
// and exponent (e) of a JWK.
func (k jwk) rsaPublicKey() (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("hcauth: decoding jwk modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("hcauth: decoding jwk exponent: %w", err)
	}
	if len(nBytes) == 0 || len(eBytes) == 0 {
		return nil, fmt.Errorf("hcauth: jwk %q has empty modulus or exponent", k.Kid)
	}
	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: int(new(big.Int).SetBytes(eBytes).Int64()),
	}, nil
}
