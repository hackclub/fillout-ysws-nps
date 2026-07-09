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

func withUser(req *http.Request, email string) *http.Request {
	return req.WithContext(auth.ContextWithUser(req.Context(), auth.User{Email: email}))
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

func TestPreviewSamplesReflectsEditedMapping(t *testing.T) {
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
					{ID: "q_extra", Name: "Anything else?", Value: json.RawMessage(`"add more boba"`)},
				},
			}},
		},
	}
	// No mapper needed: the samples endpoint rebuilds the mapping from the
	// submitted form values, not the AI.
	s := newTestServer(t, fc, nil)

	// The reviewer re-targets the free-text question from the AI's default
	// (custom_fields) onto the "How can we improve?" feedback column.
	form := strings.NewReader("form_id=abc123&ysws_program=Boba+Drops&target_q_score=score&target_q_extra=improve")
	req := httptest.NewRequest(http.MethodPost, "/forms/preview/samples", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handlePreviewSamples(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body:\n%s", rec.Code, body)
	}
	for _, want := range []string{
		"First 1 submissions as NPS records", // fragment heading reflects sample count
		"How can we improve?",                // the edited target's NPS column now appears
		"add more boba",                      // the answer rendered under it
		nps.StampLine("subZ"),                // dedup stamp still present in Custom Fields
	} {
		if !strings.Contains(body, want) {
			t.Errorf("samples fragment missing %q\nbody:\n%s", want, body)
		}
	}
	// It must be just the fragment, not the whole page.
	for _, unwanted := range []string{"Start sync", "<html", "Field mapping"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("samples fragment unexpectedly contains page chrome %q", unwanted)
		}
	}
}

func TestPreviewSamplesRequiresFormID(t *testing.T) {
	s := newTestServer(t, &fakeForms{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/forms/preview/samples", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handlePreviewSamples(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
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
	req = withUser(req, "original@hackclub.com")
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

func TestStartUsesRefetchedFilloutFormName(t *testing.T) {
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

	formID := "form-start-name-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	fc := &fakeForms{meta: &fillout.FormMetadata{
		Name:      "Real Fillout Name",
		Questions: []fillout.QuestionDef{{ID: "q_score", Name: "Recommend?", Type: fillout.QuestionOpinionScale}},
	}}
	at := &fakeAirtable{programs: []string{"Boba Drops"}}
	manager := nps.NewManager(database, fc, at, time.Hour, nil)
	t.Cleanup(manager.Shutdown)
	s, err := NewServer(&config.Config{AirtableBaseID: "appTest", NPSTable: "NPS"}, auth.New(nil, []byte("secret"), func(string) bool { return true }, false), database, fc, at, nil, manager)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	form := strings.NewReader("form_id=" + formID + "&form_name=Tampered+Name&ysws_program=Boba+Drops&target_q_score=score")
	req := httptest.NewRequest(http.MethodPost, "/sync/start", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withUser(req, "creator@example.com")
	rec := httptest.NewRecorder()
	s.handleStart(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	jobs, err := database.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	for _, job := range jobs {
		if job.FilloutFormID == formID {
			if job.FilloutFormName != "Real Fillout Name" {
				t.Fatalf("FilloutFormName = %q, want Real Fillout Name", job.FilloutFormName)
			}
			return
		}
	}
	t.Fatalf("created job for form %s not found", formID)
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
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"frame-ancestors 'none'", "script-src 'self'", "style-src 'self'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("CSP missing %q: %q", want, csp)
			}
		}
		if strings.Contains(csp, "unsafe-inline") {
			t.Errorf("CSP must not allow unsafe-inline: %q", csp)
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

func TestStaticAssets(t *testing.T) {
	s := newTestServer(t, &fakeForms{}, nil)
	for _, tc := range []struct {
		path        string
		contentType string
		marker      string
	}{
		{path: "/static/app.css", contentType: "text/css; charset=utf-8", marker: ":root"},
		{path: "/static/app.js", contentType: "text/javascript; charset=utf-8", marker: "preview-samples"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body:\n%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != tc.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, tc.contentType)
			}
			if !strings.Contains(rec.Body.String(), tc.marker) {
				t.Fatalf("body missing marker %q", tc.marker)
			}
		})
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

func createAuthTestJob(t *testing.T, database *db.DB, owner, status string) *db.SyncJob {
	t.Helper()
	job, err := database.CreateJob(context.Background(), &db.SyncJob{
		FilloutFormID:   "form-auth-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		FilloutFormName: "Auth Test",
		AirtableBaseID:  "appTest",
		AirtableTable:   "NPS",
		Mapping:         json.RawMessage(`{"entries":[{"question_id":"q_score","target":"score"}]}`),
		CreatedByEmail:  owner,
		Status:          status,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return job
}

func TestStopDoesNotCancelPollerWhenStatusUpdateFails(t *testing.T) {
	s, database, _ := dbBackedServer(t)
	ctx := context.Background()

	formID := "form-stop-fail-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	job, err := database.CreateJob(ctx, &db.SyncJob{
		FilloutFormID:   formID,
		FilloutFormName: "Stop Failure",
		AirtableBaseID:  "appTest",
		AirtableTable:   "NPS",
		Mapping:         json.RawMessage(`{"entries":[{"question_id":"q_score","target":"score"}]}`),
		CreatedByEmail:  "z@hackclub.com",
		Status:          db.StatusActive,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	s.manager.StartJob(job)
	t.Cleanup(func() { s.manager.StopJob(job.ID) })
	if !s.manager.IsRunning(job.ID) {
		t.Fatal("poller did not start")
	}
	if err := database.DeleteJob(ctx, job.ID); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}

	form := strings.NewReader("job_id=" + strconv.FormatInt(job.ID, 10))
	req := httptest.NewRequest(http.MethodPost, "/sync/stop", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withUser(req, "z@hackclub.com")
	rec := httptest.NewRecorder()
	s.handleStop(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	if !s.manager.IsRunning(job.ID) {
		t.Fatal("poller was stopped even though the durable status update failed")
	}
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
	req = withUser(req, "z@hackclub.com")
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
	req = withUser(req, "z@hackclub.com")
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

func TestAdminCanRemoveAnotherUsersJob(t *testing.T) {
	s, database, _ := dbBackedServer(t)
	s.cfg.AdminEmails = []string{"admin@example.com"}
	ctx := context.Background()
	job := createAuthTestJob(t, database, "owner@example.com", db.StatusActive)

	form := strings.NewReader("job_id=" + strconv.FormatInt(job.ID, 10))
	req := httptest.NewRequest(http.MethodPost, "/sync/remove", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withUser(req, "admin@example.com")
	rec := httptest.NewRecorder()
	s.handleRemove(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	if _, err := database.GetJob(ctx, job.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetJob after admin remove: err = %v, want ErrNotFound", err)
	}
}

func TestNonOwnerCannotRemoveAnotherUsersJob(t *testing.T) {
	s, database, at := dbBackedServer(t)
	ctx := context.Background()
	job := createAuthTestJob(t, database, "owner@example.com", db.StatusActive)
	if _, err := database.RecordSubmission(ctx, db.SubmissionRecord{
		SyncJobID: job.ID, FilloutFormID: job.FilloutFormID, FilloutSubmissionID: "s1", AirtableRecordID: "recA", Outcome: db.OutcomeCreated,
	}); err != nil {
		t.Fatalf("RecordSubmission: %v", err)
	}

	form := strings.NewReader("job_id=" + strconv.FormatInt(job.ID, 10) + "&delete_records=1")
	req := httptest.NewRequest(http.MethodPost, "/sync/remove", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withUser(req, "other@example.com")
	rec := httptest.NewRecorder()
	s.handleRemove(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	got, err := database.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob after forbidden remove: %v", err)
	}
	if got.Status != db.StatusActive {
		t.Fatalf("status after forbidden remove = %q, want %q", got.Status, db.StatusActive)
	}
	if at.deleteCalls != 0 {
		t.Fatalf("DeleteRecords calls = %d, want 0", at.deleteCalls)
	}
}

func TestNonOwnerCannotStopOrResumeAnotherUsersJob(t *testing.T) {
	s, database, _ := dbBackedServer(t)
	ctx := context.Background()

	active := createAuthTestJob(t, database, "owner@example.com", db.StatusActive)
	stopForm := strings.NewReader("job_id=" + strconv.FormatInt(active.ID, 10))
	stopReq := httptest.NewRequest(http.MethodPost, "/sync/stop", stopForm)
	stopReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	stopReq = withUser(stopReq, "other@example.com")
	stopRec := httptest.NewRecorder()
	s.handleStop(stopRec, stopReq)
	if stopRec.Code != http.StatusForbidden {
		t.Fatalf("stop status = %d; body:\n%s", stopRec.Code, stopRec.Body.String())
	}
	got, err := database.GetJob(ctx, active.ID)
	if err != nil {
		t.Fatalf("GetJob active: %v", err)
	}
	if got.Status != db.StatusActive {
		t.Fatalf("status after forbidden stop = %q, want %q", got.Status, db.StatusActive)
	}

	stopped := createAuthTestJob(t, database, "owner@example.com", db.StatusStopped)
	resumeForm := strings.NewReader("job_id=" + strconv.FormatInt(stopped.ID, 10))
	resumeReq := httptest.NewRequest(http.MethodPost, "/sync/resume", resumeForm)
	resumeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resumeReq = withUser(resumeReq, "other@example.com")
	resumeRec := httptest.NewRecorder()
	s.handleResume(resumeRec, resumeReq)
	if resumeRec.Code != http.StatusForbidden {
		t.Fatalf("resume status = %d; body:\n%s", resumeRec.Code, resumeRec.Body.String())
	}
	got, err = database.GetJob(ctx, stopped.ID)
	if err != nil {
		t.Fatalf("GetJob stopped: %v", err)
	}
	if got.Status != db.StatusStopped {
		t.Fatalf("status after forbidden resume = %q, want %q", got.Status, db.StatusStopped)
	}
	if s.manager.IsRunning(stopped.ID) {
		t.Fatal("forbidden resume started the poller")
	}
}

func TestNonAdminCannotViewAuditLog(t *testing.T) {
	s := newTestServer(t, &fakeForms{}, nil)
	s.cfg.AdminEmails = []string{"admin@example.com"}
	req := httptest.NewRequest(http.MethodGet, "/audit", nil)
	req = withUser(req, "other@example.com")
	rec := httptest.NewRecorder()
	s.handleAudit(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; body:\n%s", rec.Code, rec.Body.String())
	}
}
