package hcauth

import (
	"net/http"
	"strings"
	"testing"
)

func TestOAuthErrorMessage(t *testing.T) {
	err := &OAuthError{StatusCode: http.StatusBadRequest, Code: "invalid_grant", Description: "code is expired"}
	msg := err.Error()
	for _, want := range []string{"400", "invalid_grant", "code is expired"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q missing %q", msg, want)
		}
	}

	// Description is optional.
	bare := (&OAuthError{StatusCode: 401, Code: "invalid_token"}).Error()
	if !strings.Contains(bare, "invalid_token") || strings.Contains(bare, ": :") {
		t.Errorf("unexpected bare message: %q", bare)
	}
}

func TestAPIErrorMessage(t *testing.T) {
	msg := (&APIError{StatusCode: 500, Body: "boom"}).Error()
	if !strings.Contains(msg, "500") || !strings.Contains(msg, "boom") {
		t.Errorf("unexpected message: %q", msg)
	}
}
