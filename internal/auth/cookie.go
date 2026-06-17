package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// ErrBadSignature is returned when a cookie value fails HMAC verification.
var ErrBadSignature = errors.New("auth: bad cookie signature")

// signer signs and verifies opaque payloads with HMAC-SHA256. The wire format
// is base64url(payload) + "." + base64url(mac).
type signer struct {
	secret []byte
}

func newSigner(secret []byte) *signer {
	return &signer{secret: secret}
}

func (s *signer) sign(payload []byte) string {
	return encodeSegment(payload) + "." + encodeSegment(s.mac(payload))
}

func (s *signer) unsign(token string) ([]byte, error) {
	dot := strings.LastIndexByte(token, '.')
	if dot <= 0 {
		return nil, ErrBadSignature
	}
	payload, err := decodeSegment(token[:dot])
	if err != nil {
		return nil, ErrBadSignature
	}
	sig, err := decodeSegment(token[dot+1:])
	if err != nil {
		return nil, ErrBadSignature
	}
	// hmac.Equal is constant time, defeating timing attacks on the signature.
	if !hmac.Equal(sig, s.mac(payload)) {
		return nil, ErrBadSignature
	}
	return payload, nil
}

func (s *signer) mac(payload []byte) []byte {
	h := hmac.New(sha256.New, s.secret)
	h.Write(payload)
	return h.Sum(nil)
}

func encodeSegment(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeSegment(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
