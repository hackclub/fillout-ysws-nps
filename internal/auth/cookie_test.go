package auth

import (
	"errors"
	"testing"
)

func TestSignerRoundTrip(t *testing.T) {
	s := newSigner([]byte("secret-key"))
	payload := []byte(`{"email":"zach@hackclub.com"}`)

	token := s.sign(payload)
	got, err := s.unsign(token)
	if err != nil {
		t.Fatalf("unsign: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("round-trip = %q, want %q", got, payload)
	}
}

func TestSignerRejectsTamperedPayload(t *testing.T) {
	s := newSigner([]byte("secret-key"))
	token := s.sign([]byte("hello"))

	// Flip a character in the payload segment.
	tampered := "X" + token[1:]
	if _, err := s.unsign(tampered); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature for tampered payload, got %v", err)
	}
}

func TestSignerRejectsWrongSecret(t *testing.T) {
	token := newSigner([]byte("secret-a")).sign([]byte("hello"))
	if _, err := newSigner([]byte("secret-b")).unsign(token); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature for wrong secret, got %v", err)
	}
}

func TestSignerRejectsMalformed(t *testing.T) {
	s := newSigner([]byte("secret"))
	for _, bad := range []string{"", "nodot", ".", "a.b.c"} {
		if _, err := s.unsign(bad); err == nil {
			t.Errorf("unsign(%q) = nil error, want error", bad)
		}
	}
}
