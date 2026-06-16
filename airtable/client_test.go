package airtable

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// newTestServer spins up an httptest server running handler and returns a
// Client pointed at it.
func newTestServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New("test-key", "appTEST", WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

func decodeBody(t *testing.T, r *http.Request, out any) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("unmarshal request body %q: %v", body, err)
	}
}

func TestNew_Validation(t *testing.T) {
	if _, err := New("", "appX"); err == nil {
		t.Error("expected error for empty apiKey, got nil")
	}
	if _, err := New("key", ""); err == nil {
		t.Error("expected error for empty baseID, got nil")
	}
	if _, err := New("key", "appX"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestListRecords_AuthAndPagination(t *testing.T) {
	var calls int
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer test-key")
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got := r.URL.Path; got != "/appTEST/NPS" {
			t.Errorf("path = %q, want /appTEST/NPS", got)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("offset") {
		case "":
			io.WriteString(w, `{"records":[{"id":"rec1","fields":{"n":1}}],"offset":"page2"}`)
		case "page2":
			io.WriteString(w, `{"records":[{"id":"rec2","fields":{"n":2}}]}`)
		default:
			t.Errorf("unexpected offset %q", r.URL.Query().Get("offset"))
		}
	})

	got, err := c.ListRecords(context.Background(), "NPS", nil)
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if calls != 2 {
		t.Errorf("server calls = %d, want 2 (pagination)", calls)
	}
	if len(got) != 2 || got[0].ID != "rec1" || got[1].ID != "rec2" {
		t.Errorf("records = %+v, want rec1, rec2", got)
	}
}

func TestListRecords_MaxRecordsCap(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Always advertise another page so the cap, not the offset, stops us.
		io.WriteString(w, `{"records":[{"id":"a","fields":{}},{"id":"b","fields":{}}],"offset":"more"}`)
	})

	got, err := c.ListRecords(context.Background(), "T", &ListOptions{MaxRecords: 1})
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("len = %d, want 1 (MaxRecords cap)", len(got))
	}
}

func TestListRecords_QueryOptions(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		checks := map[string]string{
			"view":               "Grid",
			"filterByFormula":    "{n}>1",
			"maxRecords":         "5",
			"pageSize":           "2",
			"sort[0][field]":     "Name",
			"sort[0][direction]": "desc",
			"sort[1][field]":     "Age",
		}
		for k, want := range checks {
			if got := q.Get(k); got != want {
				t.Errorf("query %q = %q, want %q", k, got, want)
			}
		}
		if got := q["fields[]"]; !reflect.DeepEqual(got, []string{"Name", "Age"}) {
			t.Errorf("fields[] = %v, want [Name Age]", got)
		}
		// sort[1] has no explicit direction.
		if _, ok := q["sort[1][direction]"]; ok {
			t.Error("sort[1][direction] should be absent")
		}
		io.WriteString(w, `{"records":[]}`)
	})

	_, err := c.ListRecords(context.Background(), "T", &ListOptions{
		View:            "Grid",
		Fields:          []string{"Name", "Age"},
		FilterByFormula: "{n}>1",
		MaxRecords:      5,
		PageSize:        2,
		Sort: []SortSpec{
			{Field: "Name", Direction: "desc"},
			{Field: "Age"},
		},
	})
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
}

func TestGetRecord_PathEscaping(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Table name with a space must round-trip through path escaping.
		if got := r.URL.Path; got != "/appTEST/Approved Projects/rec123" {
			t.Errorf("decoded path = %q, want /appTEST/Approved Projects/rec123", got)
		}
		if got := r.URL.EscapedPath(); got != "/appTEST/Approved%20Projects/rec123" {
			t.Errorf("escaped path = %q, want spaces as %%20", got)
		}
		io.WriteString(w, `{"id":"rec123","createdTime":"2026-01-23T17:51:22.000Z","fields":{"Name":"x"}}`)
	})

	rec, err := c.GetRecord(context.Background(), "Approved Projects", "rec123")
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if rec.ID != "rec123" || rec.Fields["Name"] != "x" {
		t.Errorf("record = %+v", rec)
	}
	if rec.CreatedTime.IsZero() {
		t.Error("CreatedTime not parsed")
	}
}

func TestCreateRecords_BatchingAndBody(t *testing.T) {
	var batches [][]writeRecord
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var req writeRequest
		decodeBody(t, r, &req)
		if !req.Typecast {
			t.Error("typecast not forwarded")
		}
		batches = append(batches, req.Records)

		resp := listResponse{}
		for i := range req.Records {
			resp.Records = append(resp.Records, Record{ID: "rec", Fields: req.Records[i].Fields})
		}
		json.NewEncoder(w).Encode(resp)
	})

	fields := make([]map[string]any, 12)
	for i := range fields {
		fields[i] = map[string]any{"i": i}
	}

	created, err := c.CreateRecords(context.Background(), "T", fields, true)
	if err != nil {
		t.Fatalf("CreateRecords: %v", err)
	}
	if len(created) != 12 {
		t.Errorf("created = %d, want 12", len(created))
	}
	if len(batches) != 2 || len(batches[0]) != 10 || len(batches[1]) != 2 {
		t.Errorf("batch sizes = %v, want [10 2]", batchLens(batches))
	}
	// Create payloads must not carry an id field.
	if batches[0][0].ID != "" {
		t.Errorf("create payload included id %q", batches[0][0].ID)
	}
}

func batchLens(b [][]writeRecord) []int {
	out := make([]int, len(b))
	for i := range b {
		out[i] = len(b[i])
	}
	return out
}

func TestUpdateRecords_PatchVsReplace(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wantMethod string
		call       func(c *Client, recs []Record) ([]Record, error)
	}{
		{"patch", http.MethodPatch, func(c *Client, recs []Record) ([]Record, error) {
			return c.UpdateRecords(context.Background(), "T", recs, false)
		}},
		{"replace", http.MethodPut, func(c *Client, recs []Record) ([]Record, error) {
			return c.ReplaceRecords(context.Background(), "T", recs, false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.wantMethod {
					t.Errorf("method = %s, want %s", r.Method, tc.wantMethod)
				}
				var req writeRequest
				decodeBody(t, r, &req)
				if len(req.Records) != 1 || req.Records[0].ID != "rec1" {
					t.Errorf("payload = %+v, want one record with id rec1", req.Records)
				}
				io.WriteString(w, `{"records":[{"id":"rec1","fields":{"Name":"y"}}]}`)
			})

			got, err := tc.call(c, []Record{{ID: "rec1", Fields: map[string]any{"Name": "y"}}})
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if len(got) != 1 || got[0].Fields["Name"] != "y" {
				t.Errorf("got = %+v", got)
			}
		})
	}
}

func TestUpdateRecords_MissingID(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called when an ID is missing")
	})
	_, err := c.UpdateRecords(context.Background(), "T", []Record{{Fields: map[string]any{"a": 1}}}, false)
	if err == nil {
		t.Fatal("expected error for record without ID")
	}
}

func TestDeleteRecords_BatchingAndQuery(t *testing.T) {
	var seenIDs []string
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		ids := r.URL.Query()["records[]"]
		seenIDs = append(seenIDs, ids...)

		var resp deleteResponse
		for _, id := range ids {
			resp.Records = append(resp.Records, struct {
				ID      string `json:"id"`
				Deleted bool   `json:"deleted"`
			}{ID: id, Deleted: true})
		}
		json.NewEncoder(w).Encode(resp)
	})

	ids := make([]string, 11)
	for i := range ids {
		ids[i] = "rec" + string(rune('a'+i))
	}

	deleted, err := c.DeleteRecords(context.Background(), "T", ids)
	if err != nil {
		t.Fatalf("DeleteRecords: %v", err)
	}
	if !reflect.DeepEqual(deleted, ids) {
		t.Errorf("deleted = %v, want %v", deleted, ids)
	}
	if len(seenIDs) != 11 {
		t.Errorf("server saw %d ids, want 11", len(seenIDs))
	}
}

func TestDeleteRecord_Single(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/appTEST/T/rec9" {
			t.Errorf("path = %q, want /appTEST/T/rec9", r.URL.Path)
		}
		io.WriteString(w, `{"id":"rec9","deleted":true}`)
	})
	if err := c.DeleteRecord(context.Background(), "T", "rec9"); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
}

func TestDeleteRecord_NotConfirmed(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"rec9","deleted":false}`)
	})
	if err := c.DeleteRecord(context.Background(), "T", "rec9"); err == nil {
		t.Fatal("expected error when deleted=false")
	}
}

func TestAPIError_ObjectForm(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"type":"INVALID_PERMISSIONS","message":"nope"}}`)
	})

	_, err := c.GetRecord(context.Background(), "T", "rec1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != 403 || apiErr.Type != "INVALID_PERMISSIONS" || apiErr.Message != "nope" {
		t.Errorf("apiErr = %+v", apiErr)
	}
}

func TestAPIError_StringForm(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"NOT_FOUND"}`)
	})

	_, err := c.GetRecord(context.Background(), "T", "missing")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != 404 || apiErr.Type != "NOT_FOUND" {
		t.Errorf("apiErr = %+v", apiErr)
	}
}

func TestContextCancellation(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"records":[]}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ListRecords(ctx, "T", nil); err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestChunk(t *testing.T) {
	got := chunk([]int{1, 2, 3, 4, 5}, 2)
	want := [][]int{{1, 2}, {3, 4}, {5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("chunk = %v, want %v", got, want)
	}
	if got := chunk([]int{}, 3); got != nil {
		t.Errorf("chunk(empty) = %v, want nil", got)
	}
}
