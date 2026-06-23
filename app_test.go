package main

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hackclub/fillout-ysws-nps/airtable"
	"github.com/hackclub/fillout-ysws-nps/fillout"
	"github.com/hackclub/fillout-ysws-nps/internal/auth"
	"github.com/hackclub/fillout-ysws-nps/internal/config"
	"github.com/hackclub/fillout-ysws-nps/internal/db"
	"github.com/hackclub/fillout-ysws-nps/internal/nps"
	"github.com/hackclub/fillout-ysws-nps/openai"
)

type fakeForms struct {
	meta *fillout.FormMetadata
	page *fillout.SubmissionsPage
}

func (f *fakeForms) GetForm(context.Context, string) (*fillout.FormMetadata, error) {
	return f.meta, nil
}

func (f *fakeForms) GetSubmissions(context.Context, string, *fillout.GetSubmissionsParams) (*fillout.SubmissionsPage, error) {
	return f.page, nil
}

func (f *fakeForms) AllSubmissions(context.Context, string, *fillout.GetSubmissionsParams) iter.Seq2[fillout.Submission, error] {
	return func(func(fillout.Submission, error) bool) {}
}

// fakeAirtable serves YSWS program names for the dropdown and records any
// delete calls so tests can assert on them.
type fakeAirtable struct {
	programs []string

	deleteErr    error    // returned by DeleteRecords when set
	deletedTable string   // table passed to the last DeleteRecords call
	deletedIDs   []string // IDs passed to the last DeleteRecords call
	deleteCalls  int      // number of DeleteRecords calls
}

func (f *fakeAirtable) ListRecords(_ context.Context, _ string, _ *airtable.ListOptions) ([]airtable.Record, error) {
	recs := make([]airtable.Record, 0, len(f.programs))
	for _, n := range f.programs {
		recs = append(recs, airtable.Record{Fields: map[string]any{"Name": n}})
	}
	return recs, nil
}

func (f *fakeAirtable) CreateRecords(_ context.Context, _ string, _ []map[string]any, _ bool) ([]airtable.Record, error) {
	return nil, nil
}

func (f *fakeAirtable) DeleteRecords(_ context.Context, table string, ids []string) ([]string, error) {
	f.deleteCalls++
	f.deletedTable = table
	f.deletedIDs = ids
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return ids, nil
}

func stubOpenAIClient(t *testing.T, content string) *openai.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		})
	}))
	t.Cleanup(srv.Close)
	c, err := openai.NewClient("k", openai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newTestServer(t *testing.T, fc formClient, mapper *nps.Mapper) *Server {
	t.Helper()
	cfg := &config.Config{AirtableBaseID: "appTest", NPSTable: "NPS"}
	a := auth.New(nil, []byte("secret"), func(string) bool { return true }, false)
	at := &fakeAirtable{programs: []string{"Boba Drops", "Sprig"}}
	s, err := NewServer(cfg, a, nil, fc, at, mapper, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s
}

func TestHealthz(t *testing.T) {
	s := newTestServer(t, &fakeForms{}, nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestAnonymousHomeShowsLogin(t *testing.T) {
	s := newTestServer(t, &fakeForms{}, nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Log in with Hack Club") {
		t.Errorf("login page missing login button:\n%s", rec.Body.String())
	}
}

func TestProtectedRouteRedirectsAnonymous(t *testing.T) {
	s := newTestServer(t, &fakeForms{}, nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/forms/new", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestPreviewRendersMappingAndSamples(t *testing.T) {
	fc := &fakeForms{
		meta: &fillout.FormMetadata{
			Name: "Boba NPS",
			Questions: []fillout.QuestionDef{
				{ID: "q_score", Name: "Recommend?", Type: fillout.QuestionOpinionScale},
				{ID: "q_extra", Name: "Anything else?", Type: fillout.QuestionLongAnswer},
			},
		},
		page: &fillout.SubmissionsPage{
			Responses: []fillout.Submission{{
				SubmissionID: "subZ",
				Questions: []fillout.QuestionAnswer{
					{ID: "q_score", Name: "Recommend?", Value: json.RawMessage(`9`)},
					{ID: "q_extra", Name: "Anything else?", Value: json.RawMessage(`"keep it up"`)},
				},
			}},
		},
	}
	content := `{"mappings":[
		{"question_id":"q_score","target":"score","reasoning":"NPS rating","confidence":"high"},
		{"question_id":"q_extra","target":"custom_fields","reasoning":"freeform","confidence":"medium"}
	]}`
	s := newTestServer(t, fc, nps.NewMapper(stubOpenAIClient(t, content)))

	form := strings.NewReader("form_input=https://forms.fillout.com/t/abc123")
	req := httptest.NewRequest(http.MethodPost, "/forms/preview", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handlePreview(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body:\n%s", rec.Code, body)
	}
	for _, want := range []string{
		"Boba NPS",            // form name
		"Recommend?",          // question name in mapping table
		"Start sync",          // CTA
		`name="csrf_token"`,   // CSRF token field embedded in the start-sync form
		nps.StampLine("subZ"), // dedup stamp present in rendered Custom Fields
		"keep it up",          // catch-all answer rendered in preview
		// mapping dropdown shows the full NPS column title, not the key "score":
		"On a scale from 1-10, how likely are you to recommend this YSWS to a friend?",
		"Boba Drops", // required YSWS program option in the searchable datalist
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preview missing %q", want)
		}
	}
}

func TestStartDuplicateReactivatesExistingStoppedJob(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set; skipping db integration test")
	}
	ctx := context.Background()
	database, err := db.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	formID := "form-reactivate-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	existing, err := database.CreateJob(ctx, &db.SyncJob{
		FilloutFormID:   formID,
		FilloutFormName: "Existing Form",
		AirtableBaseID:  "appTest",
		AirtableTable:   "NPS",
		Mapping:         json.RawMessage(`{"entries":[{"question_id":"q_score","target":"score"}]}`),
		YSWSProgram:     "Boba Drops",
		CreatedByEmail:  "original@hackclub.com",
		Status:          db.StatusStopped,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	fc := &fakeForms{meta: &fillout.FormMetadata{
		Name:      "Existing Form",
		Questions: []fillout.QuestionDef{{ID: "q_score", Name: "Recommend?", Type: fillout.QuestionOpinionScale}},
	}}
	at := &fakeAirtable{programs: []string{"Boba Drops"}}
	manager := nps.NewManager(database, fc, at, time.Hour, nil)
	t.Cleanup(manager.Shutdown)

	s, err := NewServer(&config.Config{AirtableBaseID: "appTest", NPSTable: "NPS"}, auth.New(nil, []byte("secret"), func(string) bool { return true }, false), database, fc, at, nil, manager)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	form := strings.NewReader("form_id=" + formID + "&form_name=Existing+Form&ysws_program=Boba+Drops&target_q_score=score")
	req := httptest.NewRequest(http.MethodPost, "/sync/start", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleStart(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	got, err := database.GetJob(ctx, existing.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != db.StatusActive {
		t.Errorf("status = %q, want %q", got.Status, db.StatusActive)
	}
}

func TestNewHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer(":8080", http.NewServeMux())
	if srv.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", srv.Addr)
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be set (Slowloris protection)")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout must be set")
	}
	if srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout must be set")
	}
	// WriteTimeout must exceed the OpenAI client's 60s budget so the preview
	// handler can finish writing its response.
	if srv.WriteTimeout <= 60*time.Second {
		t.Errorf("WriteTimeout = %v, want > 60s (must exceed the OpenAI call budget)", srv.WriteTimeout)
	}
}

func TestSecurityHeaders(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	t.Run("http omits HSTS", func(t *testing.T) {
		rec := httptest.NewRecorder()
		securityHeaders(next, false).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		h := rec.Header()
		if h.Get("X-Frame-Options") != "DENY" {
			t.Errorf("X-Frame-Options = %q, want DENY", h.Get("X-Frame-Options"))
		}
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want nosniff", h.Get("X-Content-Type-Options"))
		}
		if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Errorf("CSP missing frame-ancestors 'none': %q", h.Get("Content-Security-Policy"))
		}
		if h.Get("Strict-Transport-Security") != "" {
			t.Errorf("HSTS must not be set on plain HTTP, got %q", h.Get("Strict-Transport-Security"))
		}
	})

	t.Run("https sets HSTS", func(t *testing.T) {
		rec := httptest.NewRecorder()
		securityHeaders(next, true).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Header().Get("Strict-Transport-Security") == "" {
			t.Error("HSTS must be set when served over HTTPS")
		}
	})
}

func TestRoutesAppliesSecurityHeaders(t *testing.T) {
	s := newTestServer(t, &fakeForms{}, nil)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("Routes() did not apply security headers; X-Frame-Options = %q", rec.Header().Get("X-Frame-Options"))
	}
}

func TestParseFormID(t *testing.T) {
	cases := map[string]string{
		"abc123":                              "abc123",
		"  abc123  ":                          "abc123",
		"pYAa1Upz9uus":                        "pYAa1Upz9uus",
		"https://forms.fillout.com/t/abc123":  "abc123",
		"https://forms.fillout.com/t/abc123/": "abc123",
		"https://build.fillout.com/editor/pYAa1Upz9uus/edit":            "pYAa1Upz9uus",
		"https://build.fillout.com/editor/pYAa1Upz9uus":                 "pYAa1Upz9uus",
		"https://build.fillout.com/editor/pYAa1Upz9uus/edit?tab=design": "pYAa1Upz9uus",
		"https://forms.fillout.com/abc123":                              "abc123",
		"":                                                              "",
	}
	for in, want := range cases {
		if got := parseFormID(in); got != want {
			t.Errorf("parseFormID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPortDefault(t *testing.T) {
	t.Setenv("PORT", "")
	if got := port(); got != "8080" {
		t.Errorf("default port = %q, want 8080", got)
	}
}

func TestPortFromEnv(t *testing.T) {
	t.Setenv("PORT", "9999")
	if got := port(); got != "9999" {
		t.Errorf("port = %q, want 9999", got)
	}
}

// dbBackedServer builds a Server wired to a real Postgres (skipping when
// DATABASE_URL is unset) plus a manager and a fakeAirtable, for handler tests
// that touch the store.
func dbBackedServer(t *testing.T) (*Server, *db.DB, *fakeAirtable) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set; skipping db integration test")
	}
	ctx := context.Background()
	database, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	fc := &fakeForms{}
	at := &fakeAirtable{}
	manager := nps.NewManager(database, fc, at, time.Hour, nil)
	t.Cleanup(manager.Shutdown)
	cfg := &config.Config{AirtableBaseID: "appTest", NPSTable: "NPS"}
	s, err := NewServer(cfg, auth.New(nil, []byte("secret"), func(string) bool { return true }, false), database, fc, at, nil, manager)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s, database, at
}

func TestRemoveJobDeletesOnlyItsOwnCreatedRecords(t *testing.T) {
	s, database, at := dbBackedServer(t)
	ctx := context.Background()

	formID := "form-remove-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	job, err := database.CreateJob(ctx, &db.SyncJob{
		FilloutFormID: formID, FilloutFormName: "Removable", AirtableBaseID: "appTest",
		AirtableTable: "NPS", CreatedByEmail: "z@hackclub.com", Status: db.StatusActive,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := database.RecordSubmissions(ctx, []db.SubmissionRecord{
		{SyncJobID: job.ID, FilloutFormID: formID, FilloutSubmissionID: "s1", AirtableRecordID: "recA", Outcome: db.OutcomeCreated},
		{SyncJobID: job.ID, FilloutFormID: formID, FilloutSubmissionID: "s2", AirtableRecordID: "recB", Outcome: db.OutcomeCreated},
		// Skipped (someone else's record) — must NOT be deleted.
		{SyncJobID: job.ID, FilloutFormID: formID, FilloutSubmissionID: "s3", AirtableRecordID: "recX", Outcome: db.OutcomeSkippedExisting},
	}); err != nil {
		t.Fatalf("RecordSubmissions: %v", err)
	}

	form := strings.NewReader("job_id=" + strconv.FormatInt(job.ID, 10) + "&delete_records=1")
	req := httptest.NewRequest(http.MethodPost, "/sync/remove", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleRemove(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	if _, err := database.GetJob(ctx, job.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetJob after remove: err = %v, want ErrNotFound", err)
	}
	if at.deleteCalls != 1 {
		t.Fatalf("DeleteRecords calls = %d, want 1", at.deleteCalls)
	}
	if at.deletedTable != "NPS" {
		t.Errorf("deleted table = %q, want NPS", at.deletedTable)
	}
	if len(at.deletedIDs) != 2 || at.deletedIDs[0] != "recA" || at.deletedIDs[1] != "recB" {
		t.Errorf("deleted IDs = %v, want [recA recB] (skipped recX must be excluded)", at.deletedIDs)
	}
}

func TestRemoveJobKeepsRecordsByDefault(t *testing.T) {
	s, database, at := dbBackedServer(t)
	ctx := context.Background()

	formID := "form-keep-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	job, err := database.CreateJob(ctx, &db.SyncJob{
		FilloutFormID: formID, FilloutFormName: "Keep", AirtableBaseID: "appTest",
		AirtableTable: "NPS", CreatedByEmail: "z@hackclub.com", Status: db.StatusActive,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := database.RecordSubmission(ctx, db.SubmissionRecord{
		SyncJobID: job.ID, FilloutFormID: formID, FilloutSubmissionID: "s1", AirtableRecordID: "recA", Outcome: db.OutcomeCreated,
	}); err != nil {
		t.Fatalf("RecordSubmission: %v", err)
	}

	// No delete_records field -> records are kept.
	form := strings.NewReader("job_id=" + strconv.FormatInt(job.ID, 10))
	req := httptest.NewRequest(http.MethodPost, "/sync/remove", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleRemove(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	if _, err := database.GetJob(ctx, job.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetJob after remove: err = %v, want ErrNotFound", err)
	}
	if at.deleteCalls != 0 {
		t.Errorf("DeleteRecords calls = %d, want 0 (records kept)", at.deleteCalls)
	}
}
