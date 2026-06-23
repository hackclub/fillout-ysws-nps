package auth

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hackclub/fillout-ysws-nps/airtable"
	"github.com/hackclub/fillout-ysws-nps/internal/dotenv"
)

// TestIntegration_AuthorEmailsFetch reads the real YSWS Authors table to confirm
// the table name, field name, and filterByFormula the allow-list uses are valid
// against the live base. It is read-only and skips when credentials are absent.
//
// Set AUTH_TEST_EXPECT_EMAIL to additionally assert a known author can log in.
func TestIntegration_AuthorEmailsFetch(t *testing.T) {
	_ = dotenv.Load("../../.env") // best-effort; worktree root holds .env

	apiKey := os.Getenv("AIRTABLE_API_KEY")
	baseID := os.Getenv("AIRTABLE_BASE_ID")
	if apiKey == "" || baseID == "" {
		t.Skip("AIRTABLE_API_KEY/AIRTABLE_BASE_ID not set; skipping integration test")
	}

	client, err := airtable.New(apiKey, baseID)
	if err != nil {
		t.Fatalf("airtable.New: %v", err)
	}
	l := NewAllowlist(nil, client)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	set, err := l.fetch(ctx)
	if err != nil {
		t.Fatalf("fetch %q.%q: %v", AuthorsTable, AuthorEmailField, err)
	}
	t.Logf("fetched %d author email(s) from %q", len(set), AuthorsTable)

	for email := range set {
		if email != normalizeEmail(email) {
			t.Errorf("email %q is not normalized", email)
		}
		if !strings.Contains(email, "@") {
			t.Errorf("email %q does not look like an address", email)
		}
	}

	if want := normalizeEmail(os.Getenv("AUTH_TEST_EXPECT_EMAIL")); want != "" {
		if !l.Allowed(want) {
			t.Errorf("Allowed(%q) = false; expected this author to be permitted", want)
		}
	}
}
