package hcauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// fixedClock returns a clock function pinned to t.
func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

func TestExchange(t *testing.T) {
	var gotForm url.Values
	var gotContentType string

	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"access_token": "idntk.abc",
			"token_type": "Bearer",
			"refresh_token": "idnrf.xyz",
			"id_token": "eyJ.payload.sig",
			"scope": "openid email name profile",
			"expires_in": 15778800
		}`)
	})

	c, _ := newMockClient(t, mux)
	at := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	c.now = fixedClock(at)

	tok, err := c.Exchange(context.Background(), "auth-code-1", "verifier-1")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}

	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("content-type = %q", gotContentType)
	}
	formChecks := map[string]string{
		"grant_type":    "authorization_code",
		"code":          "auth-code-1",
		"redirect_uri":  "http://localhost:3000/callback",
		"code_verifier": "verifier-1",
		"client_id":     "client-123",
		"client_secret": "secret-456",
	}
	for key, want := range formChecks {
		if got := gotForm.Get(key); got != want {
			t.Errorf("form %q = %q, want %q", key, got, want)
		}
	}

	if tok.AccessToken != "idntk.abc" || tok.RefreshToken != "idnrf.xyz" || tok.IDToken != "eyJ.payload.sig" {
		t.Errorf("unexpected token: %+v", tok)
	}
	if want := at.Add(15778800 * time.Second); !tok.Expiry.Equal(want) {
		t.Errorf("Expiry = %v, want %v", tok.Expiry, want)
	}
}

func TestExchangeOmitsVerifierWhenEmpty(t *testing.T) {
	var gotForm url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		io.WriteString(w, `{"access_token":"a","token_type":"Bearer"}`)
	})
	c, _ := newMockClient(t, mux)

	if _, err := c.Exchange(context.Background(), "code", ""); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if gotForm.Has("code_verifier") {
		t.Error("expected no code_verifier when verifier is empty")
	}
}

func TestExchangeRequiresCode(t *testing.T) {
	c, _ := New("id", "secret", "http://localhost:3000/callback")
	if _, err := c.Exchange(context.Background(), "  ", "v"); err == nil {
		t.Fatal("expected error for empty code")
	}
}

func TestExchangeOAuthError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant","error_description":"code is expired"}`)
	})
	c, _ := newMockClient(t, mux)

	_, err := c.Exchange(context.Background(), "bad", "v")
	var oauthErr *OAuthError
	if !errors.As(err, &oauthErr) {
		t.Fatalf("expected *OAuthError, got %v", err)
	}
	if oauthErr.Code != "invalid_grant" || oauthErr.StatusCode != http.StatusBadRequest {
		t.Errorf("unexpected oauth error: %+v", oauthErr)
	}
	if oauthErr.Description != "code is expired" {
		t.Errorf("description = %q", oauthErr.Description)
	}
}

func TestExchangeAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "upstream exploded")
	})
	c, _ := newMockClient(t, mux)

	_, err := c.Exchange(context.Background(), "code", "v")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d", apiErr.StatusCode)
	}
}

func TestExchangeMissingAccessToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"token_type":"Bearer"}`)
	})
	c, _ := newMockClient(t, mux)
	if _, err := c.Exchange(context.Background(), "code", "v"); err == nil {
		t.Fatal("expected error when access_token is missing")
	}
}

func TestRefresh(t *testing.T) {
	var gotForm url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		io.WriteString(w, `{"access_token":"new-access","token_type":"Bearer","refresh_token":"new-refresh","expires_in":3600}`)
	})
	c, _ := newMockClient(t, mux)
	c.now = fixedClock(time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC))

	tok, err := c.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if gotForm.Get("grant_type") != "refresh_token" || gotForm.Get("refresh_token") != "old-refresh" {
		t.Errorf("unexpected refresh form: %v", gotForm)
	}
	if tok.AccessToken != "new-access" || tok.RefreshToken != "new-refresh" {
		t.Errorf("unexpected token: %+v", tok)
	}
}

func TestRefreshRequiresToken(t *testing.T) {
	c, _ := New("id", "secret", "http://localhost:3000/callback")
	if _, err := c.Refresh(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty refresh token")
	}
}

func TestWithHTTPClient(t *testing.T) {
	rt := &countingTransport{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"access_token":"a","token_type":"Bearer"}`)
	})
	c, _ := newMockClient(t, mux, WithHTTPClient(&http.Client{Transport: rt}))

	if _, err := c.Exchange(context.Background(), "code", ""); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if rt.calls != 1 {
		t.Errorf("custom transport called %d times, want 1", rt.calls)
	}
}

// countingTransport is a RoundTripper that counts calls and delegates to the
// default transport.
type countingTransport struct{ calls int }

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls++
	return http.DefaultTransport.RoundTrip(r)
}

func TestExchangeTransportError(t *testing.T) {
	// Stand up a server, capture its URL, then close it so the round-trip fails.
	srv := httptest.NewServer(http.NewServeMux())
	closedURL := srv.URL
	srv.Close()

	c, err := New("id", "secret", "http://localhost:3000/callback",
		WithEndpoints(closedURL+"/oauth/authorize", closedURL+"/oauth/token", closedURL+"/oauth/userinfo", closedURL+"/oauth/discovery/keys"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := c.Exchange(context.Background(), "code", "v"); err == nil {
		t.Fatal("expected transport error against a closed server")
	}
}

func TestTokenExpired(t *testing.T) {
	if (&Token{}).Expired() {
		t.Error("token with zero Expiry should not be considered expired")
	}
	past := &Token{Expiry: time.Now().Add(-time.Hour)}
	if !past.Expired() {
		t.Error("token with past Expiry should be expired")
	}
	future := &Token{Expiry: time.Now().Add(time.Hour)}
	if future.Expired() {
		t.Error("token with future Expiry should not be expired")
	}
}
