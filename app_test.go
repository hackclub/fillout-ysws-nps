package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleHome(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	newRouter().ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want %d", res.StatusCode, http.StatusOK)
	}

	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("got Content-Type %q, want text/html", ct)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Fillout YSWS NPS") {
		t.Errorf("home page body missing title; got:\n%s", body)
	}
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Errorf("home page body is not a full HTML document; got:\n%s", body)
	}
}

func TestHandleHomeUnknownPathReturns404(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	rec := httptest.NewRecorder()

	newRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("got status %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	newRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "ok" {
		t.Errorf("got body %q, want %q", got, "ok")
	}
}

func TestPortDefault(t *testing.T) {
	t.Setenv("PORT", "")
	if got := port(); got != "8080" {
		t.Errorf("got default port %q, want %q", got, "8080")
	}
}

func TestPortFromEnv(t *testing.T) {
	t.Setenv("PORT", "9999")
	if got := port(); got != "9999" {
		t.Errorf("got port %q, want %q", got, "9999")
	}
}
