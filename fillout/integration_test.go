package fillout_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hackclub/fillout-ysws-nps/fillout"
)

// TestIntegration exercises the read-only endpoints against the live Fillout
// API. It is skipped unless FILLOUT_API_KEY is set, so it never runs (or needs
// a secret) in normal `go test` invocations. It performs no writes.
func TestIntegration(t *testing.T) {
	key := os.Getenv("FILLOUT_API_KEY")
	if key == "" {
		t.Skip("set FILLOUT_API_KEY to run live integration tests")
	}

	c := fillout.NewClient(key)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	forms, err := c.ListForms(ctx)
	if err != nil {
		t.Fatalf("ListForms: %v", err)
	}
	if len(forms) == 0 {
		t.Skip("account has no forms; nothing more to verify")
	}
	t.Logf("ListForms returned %d forms", len(forms))

	form := forms[0]
	if form.FormID == "" {
		t.Fatal("first form has an empty FormID")
	}

	meta, err := c.GetForm(ctx, form.FormID)
	if err != nil {
		t.Fatalf("GetForm(%s): %v", form.FormID, err)
	}
	if meta.ID != form.FormID {
		t.Errorf("metadata ID = %q, want %q", meta.ID, form.FormID)
	}
	t.Logf("GetForm(%s) -> %d questions", form.FormID, len(meta.Questions))

	page, err := c.GetSubmissions(ctx, form.FormID, &fillout.GetSubmissionsParams{Limit: 1})
	if err != nil {
		t.Fatalf("GetSubmissions(%s): %v", form.FormID, err)
	}
	t.Logf("GetSubmissions(%s) -> total %d", form.FormID, page.TotalResponses)
}
