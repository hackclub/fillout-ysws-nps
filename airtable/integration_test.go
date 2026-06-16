package airtable

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loadDotEnv walks up from the working directory looking for a .env file and
// loads any KEY=VALUE pairs that are not already set in the environment. This
// lets integration tests pick up credentials from the repo's .env without an
// external dependency.
func loadDotEnv() {
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for i := 0; i < 6; i++ {
		if data, err := os.ReadFile(filepath.Join(dir, ".env")); err == nil {
			applyDotEnv(string(data))
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

func applyDotEnv(content string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
}

// integrationClient returns a Client backed by real credentials and the table
// to read from, or skips the test if credentials are unavailable.
func integrationClient(t *testing.T) (*Client, string) {
	t.Helper()
	loadDotEnv()

	apiKey := os.Getenv("AIRTABLE_API_KEY")
	baseID := os.Getenv("AIRTABLE_BASE_ID")
	if apiKey == "" || baseID == "" {
		t.Skip("AIRTABLE_API_KEY/AIRTABLE_BASE_ID not set; skipping integration test")
	}

	c, err := New(apiKey, baseID, WithHTTPClient(&http.Client{Timeout: 30 * time.Second}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	table := os.Getenv("AIRTABLE_TEST_TABLE")
	if table == "" {
		table = "NPS"
	}
	return c, table
}

// TestIntegration_ListRecords reads a small page from the real base. It is
// read-only and safe to run against shared data.
func TestIntegration_ListRecords(t *testing.T) {
	c, table := integrationClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	records, err := c.ListRecords(ctx, table, &ListOptions{MaxRecords: 3, PageSize: 3})
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(records) == 0 {
		t.Skip("no records in table; nothing to assert")
	}
	if len(records) > 3 {
		t.Errorf("got %d records, want <= 3", len(records))
	}
	for i, r := range records {
		if r.ID == "" {
			t.Errorf("record %d has empty ID", i)
		}
	}
}

// TestIntegration_Pagination forces multiple pages (PageSize 1, MaxRecords 2)
// to exercise real offset following.
func TestIntegration_Pagination(t *testing.T) {
	c, table := integrationClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	records, err := c.ListRecords(ctx, table, &ListOptions{PageSize: 1, MaxRecords: 2})
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(records) < 2 {
		t.Skipf("table has fewer than 2 records (%d); cannot test pagination", len(records))
	}
	if records[0].ID == records[1].ID {
		t.Error("expected distinct records across pages")
	}
}

// TestIntegration_GetRecord lists one record, then fetches it by ID.
func TestIntegration_GetRecord(t *testing.T) {
	c, table := integrationClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	records, err := c.ListRecords(ctx, table, &ListOptions{MaxRecords: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(records) == 0 {
		t.Skip("no records in table; nothing to fetch")
	}

	want := records[0]
	got, err := c.GetRecord(ctx, table, want.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("GetRecord ID = %q, want %q", got.ID, want.ID)
	}
}

// TestIntegration_GetRecord_NotFound verifies real 404 handling.
func TestIntegration_GetRecord_NotFound(t *testing.T) {
	c, table := integrationClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.GetRecord(ctx, table, "recDoesNotExist0000")
	if err == nil {
		t.Fatal("expected error for nonexistent record")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", apiErr.StatusCode)
	}
}

// TestIntegration_WriteRoundTrip exercises create -> update -> delete against a
// real base. It is OPT-IN: it only runs when AIRTABLE_TEST_ALLOW_WRITES=1 and
// AIRTABLE_TEST_WRITE_TABLE is set, so it never mutates production data by
// accident. Set AIRTABLE_TEST_WRITE_BASE to point writes at a dedicated scratch
// base separate from the (production) AIRTABLE_BASE_ID used for read tests.
func TestIntegration_WriteRoundTrip(t *testing.T) {
	if os.Getenv("AIRTABLE_TEST_ALLOW_WRITES") != "1" {
		t.Skip("set AIRTABLE_TEST_ALLOW_WRITES=1 and AIRTABLE_TEST_WRITE_TABLE to run write tests")
	}
	loadDotEnv()

	apiKey := os.Getenv("AIRTABLE_API_KEY")
	if apiKey == "" {
		t.Skip("AIRTABLE_API_KEY not set; skipping write test")
	}
	baseID := os.Getenv("AIRTABLE_TEST_WRITE_BASE")
	if baseID == "" {
		baseID = os.Getenv("AIRTABLE_BASE_ID")
	}
	if baseID == "" {
		t.Skip("no base ID for write test")
	}
	table := os.Getenv("AIRTABLE_TEST_WRITE_TABLE")
	if table == "" {
		t.Skip("AIRTABLE_TEST_WRITE_TABLE not set; skipping write test")
	}
	field := os.Getenv("AIRTABLE_TEST_WRITE_FIELD")
	if field == "" {
		field = "Name"
	}

	c, err := New(apiKey, baseID, WithHTTPClient(&http.Client{Timeout: 30 * time.Second}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	created, err := c.CreateRecord(ctx, table, map[string]any{field: "airtable-go-test"}, false)
	if err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	// Safety net: delete the record only if the test bails out before its own
	// explicit delete below runs (so the happy path doesn't double-delete).
	var deleted bool
	t.Cleanup(func() {
		if deleted {
			return
		}
		if err := c.DeleteRecord(context.Background(), table, created.ID); err != nil {
			t.Logf("cleanup delete failed for %s: %v", created.ID, err)
		}
	})

	updated, err := c.UpdateRecord(ctx, table, created.ID, map[string]any{field: "airtable-go-test-updated"}, false)
	if err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	if updated.Fields[field] != "airtable-go-test-updated" {
		t.Errorf("updated field = %v, want airtable-go-test-updated", updated.Fields[field])
	}

	if err := c.DeleteRecord(ctx, table, created.ID); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
	deleted = true

	if _, err := c.GetRecord(ctx, table, created.ID); err == nil {
		t.Error("expected error fetching deleted record")
	}
}
