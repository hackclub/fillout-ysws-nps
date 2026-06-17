package hcauth

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestUserInfo(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/userinfo", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		// email_verified arrives as a JSON boolean, ysws_eligible as a quoted
		// string — both must decode into Bool.
		_, _ = w.Write([]byte(`{
			"sub": "user_42",
			"email": "fiona@hackclub.com",
			"email_verified": true,
			"name": "Fiona Hacker",
			"given_name": "Fiona",
			"family_name": "Hacker",
			"nickname": "fi",
			"slack_id": "U123ABC",
			"verification_status": "verified",
			"ysws_eligible": "true"
		}`))
	})
	c, _ := newMockClient(t, mux)

	info, err := c.UserInfo(context.Background(), "access-token-1")
	if err != nil {
		t.Fatalf("UserInfo: %v", err)
	}
	if gotAuth != "Bearer access-token-1" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if info.Subject != "user_42" || info.Email != "fiona@hackclub.com" || info.Name != "Fiona Hacker" {
		t.Errorf("unexpected info: %+v", info)
	}
	if !bool(info.EmailVerified) {
		t.Error("EmailVerified should be true (from JSON boolean)")
	}
	if !bool(info.YSWSEligible) {
		t.Error("YSWSEligible should be true (from JSON string \"true\")")
	}
	if info.SlackID != "U123ABC" || info.VerificationStatus != "verified" {
		t.Errorf("hack club claims not parsed: %+v", info)
	}
	if _, ok := info.Raw["slack_id"]; !ok {
		t.Error("Raw should retain all claims, including slack_id")
	}
}

func TestUserInfoRequiresToken(t *testing.T) {
	c, _ := New("id", "secret", "http://localhost:3000/callback")
	if _, err := c.UserInfo(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty access token")
	}
}

func TestUserInfoMissingSub(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/userinfo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"email":"x@example.com"}`))
	})
	c, _ := newMockClient(t, mux)
	if _, err := c.UserInfo(context.Background(), "tok"); err == nil {
		t.Fatal("expected error when sub is missing")
	}
}

func TestUserInfoUnauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	c, _ := newMockClient(t, mux)
	_, err := c.UserInfo(context.Background(), "expired")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", apiErr.StatusCode)
	}
}

func TestBoolUnmarshal(t *testing.T) {
	cases := map[string]struct {
		in      string
		want    bool
		wantErr bool
	}{
		"json true":    {`true`, true, false},
		"json false":   {`false`, false, false},
		"string true":  {`"true"`, true, false},
		"string false": {`"false"`, false, false},
		"empty string": {`""`, false, false},
		"null":         {`null`, false, false},
		"garbage":      {`"yes"`, false, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var b Bool
			err := b.UnmarshalJSON([]byte(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if bool(b) != tc.want {
				t.Errorf("got %v, want %v", bool(b), tc.want)
			}
		})
	}
}
