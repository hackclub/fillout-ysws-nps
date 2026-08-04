package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hackclub/fillout-ysws-nps/fillout"
	"github.com/hackclub/fillout-ysws-nps/internal/auth"
	"github.com/hackclub/fillout-ysws-nps/internal/config"
	"github.com/hackclub/fillout-ysws-nps/internal/db"
	"github.com/hackclub/fillout-ysws-nps/internal/httplog"
	"github.com/hackclub/fillout-ysws-nps/internal/nps"
)

//go:embed templates/*.html static/*
var assetsFS embed.FS

// formClient is the subset of the Fillout client the web layer needs.
type formClient interface {
	GetForm(ctx context.Context, formID string) (*fillout.FormMetadata, error)
	GetSubmissions(ctx context.Context, formID string, params *fillout.GetSubmissionsParams) (*fillout.SubmissionsPage, error)
}

// airtableClient is the subset of *airtable.Client the web layer uses: what the
// sync needs (nps.AirtableAPI: list + create) plus record deletion, used when a
// sync is removed and the user opts to delete the records it created.
type airtableClient interface {
	nps.AirtableAPI
	DeleteRecords(ctx context.Context, table string, recordIDs []string) ([]string, error)
}

// Server holds the application's HTTP dependencies.
type Server struct {
	cfg      *config.Config
	auth     *auth.Authenticator
	store    *db.DB
	fillout  formClient
	airtable airtableClient
	mapper   *nps.Mapper
	manager  *nps.Manager
	tmpl     *template.Template
}

// NewServer constructs a Server and parses templates.
func NewServer(cfg *config.Config, a *auth.Authenticator, store *db.DB, fc formClient, at airtableClient, mapper *nps.Mapper, manager *nps.Manager) (*Server, error) {
	tmpl, err := template.ParseFS(assetsFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}
	return &Server{cfg: cfg, auth: a, store: store, fillout: fc, airtable: at, mapper: mapper, manager: manager, tmpl: tmpl}, nil
}

// Routes wires up the HTTP handlers.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /login", s.auth.Login)
	mux.HandleFunc("GET /callback", s.auth.Callback)
	mux.HandleFunc("GET /logout", s.auth.Logout)
	mux.HandleFunc("GET /static/app.css", serveStaticAsset("static/app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /static/app.js", serveStaticAsset("static/app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /{$}", s.handleHome)

	mux.Handle("GET /forms/new", s.auth.RequireUser(http.HandlerFunc(s.handleNewForm)))
	mux.Handle("POST /forms/preview", s.auth.RequireUser(http.HandlerFunc(s.handlePreview)))
	mux.Handle("POST /forms/preview/samples", s.auth.RequireUser(http.HandlerFunc(s.handlePreviewSamples)))
	mux.Handle("POST /sync/start", s.auth.RequireUser(http.HandlerFunc(s.handleStart)))
	mux.Handle("POST /sync/stop", s.auth.RequireUser(http.HandlerFunc(s.handleStop)))
	mux.Handle("POST /sync/resume", s.auth.RequireUser(http.HandlerFunc(s.handleResume)))
	mux.Handle("GET /sync/remove", s.auth.RequireUser(http.HandlerFunc(s.handleRemoveConfirm)))
	mux.Handle("POST /sync/remove", s.auth.RequireUser(http.HandlerFunc(s.handleRemove)))
	mux.Handle("GET /audit", s.auth.RequireUser(http.HandlerFunc(s.handleAudit)))
	// httplog is outermost so every response — including ones written by the
	// middleware below it — produces exactly one log line.
	return httplog.Middleware(securityHeaders(mux, strings.HasPrefix(s.cfg.HCAuthCallbackBase, "https://")))
}

// securityHeaders wraps h with conservative, app-wide security response headers.
// HSTS is sent only when the app is served over HTTPS (https), so it never
// pins plain-HTTP local dev. The CSP allows same-origin assets plus the Hack
// Club asset host for images/fonts, and forbids framing.
func securityHeaders(h http.Handler, https bool) http.Handler {
	const csp = "default-src 'self'; " +
		"img-src 'self' https://assets.hackclub.com data:; " +
		"font-src https://assets.hackclub.com; " +
		"style-src 'self'; " +
		"script-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		head := w.Header()
		head.Set("X-Content-Type-Options", "nosniff")
		head.Set("X-Frame-Options", "DENY")
		head.Set("Content-Security-Policy", csp)
		head.Set("Referrer-Policy", "same-origin")
		if https {
			head.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		h.ServeHTTP(w, r)
	})
}

func serveStaticAsset(path, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := assetsFS.ReadFile(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(b)
	}
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// --- view models ---

type baseView struct {
	Title     string
	UserEmail string
	IsAdmin   bool
	// CSRF is the per-session CSRF token embedded in every state-changing form.
	CSRF string
}

// duplicateView backs the page shown when someone connects a form that already
// has a sync. It carries the existing sync itself, not just prose about it, so
// the user can see who set it up, whether it is running, and how much it has
// synced — everything needed to decide what to do next.
type duplicateView struct {
	baseView
	Job jobRow
	// SetUpOn is the existing sync's creation date, already formatted.
	SetUpOn string
	// Restarted reports that the existing sync had been stopped and this request
	// started it again (only possible for someone who may manage it).
	Restarted bool
}

type newFormView struct {
	baseView
	Error     string
	FormInput string
}

type dashboardView struct {
	baseView
	Flash string
	Jobs  []jobRow
}

type jobRow struct {
	ID        int64
	FormName  string
	FormID    string
	BaseID    string
	Table     string
	YSWS      string
	Tags      string
	CreatedBy string
	Status    string
	Running   bool
	Created   int
	Skipped   int
	Errored   int
	// LastPolled is the no-JS fallback text (UTC). LastPolledISO is the same
	// instant as an RFC3339 UTC value the browser localizes to the viewer's zone.
	LastPolled    string
	LastPolledISO string
	LastError     string
	CanManage     bool
}

type previewView struct {
	baseView
	Error       string
	FormID      string
	FormName    string
	BaseID      string
	Table       string
	YSWSProgram string
	Tags        string
	TagExamples []tagExample
	Programs    []string
	Targets     []nps.TargetOption
	Mappings    []nps.MappingEntry
	Samples     []sampleView
}

type tagExample struct {
	Program string
	Tags    string
}

// samplesView carries just the sample-records preview, for the partial re-render
// served to the mapping screen when a target or template changes.
type samplesView struct {
	Samples []sampleView
}

type sampleView struct {
	SubmissionID   string
	SubmissionTime string
	Fields         []fieldKV
}

type fieldKV struct {
	Name  string
	Value template.HTML
	// Rich marks a field rendered as Airtable-style formatted rich text (the
	// feedback columns and the Custom Fields catch-all) rather than plain text.
	Rich bool
}

// --- handlers ---

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	user, ok := s.auth.CurrentUser(r)
	if !ok {
		s.render(w, "page_login", baseView{Title: "Fillout → NPS sync"})
		return
	}

	jobs, err := s.store.ListJobs(r.Context())
	if err != nil {
		s.serverError(w, r, "loading jobs", err)
		return
	}
	jobIDs := make([]int64, 0, len(jobs))
	for _, j := range jobs {
		jobIDs = append(jobIDs, j.ID)
	}
	statsByJob, err := s.store.StatsForJobs(r.Context(), jobIDs)
	if err != nil {
		s.serverError(w, r, "loading job stats", err)
		return
	}

	rows := make([]jobRow, 0, len(jobs))
	for _, j := range jobs {
		rows = append(rows, s.jobRowFor(user, j, statsByJob[j.ID]))
	}

	s.render(w, "page_dashboard", dashboardView{
		baseView: baseView{Title: "Dashboard", UserEmail: user.Email, IsAdmin: s.cfg.IsAdmin(user.Email), CSRF: s.auth.CSRFToken(r)},
		Flash:    r.URL.Query().Get("flash"),
		Jobs:     rows,
	})
}

func (s *Server) handleNewForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "page_new_form", newFormView{baseView: s.base(r, "New form")})
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	formID := parseFormID(r.FormValue("form_input"))
	if formID == "" {
		s.render(w, "page_new_form", newFormView{
			baseView: s.base(r, "New form"),
			Error:    "Please paste a Fillout form link or ID.",
		})
		return
	}

	meta, err := s.fillout.GetForm(r.Context(), formID)
	if err != nil {
		s.render(w, "page_new_form", newFormView{
			baseView:  s.base(r, "New form"),
			Error:     fmt.Sprintf("Couldn't load that form (%s): %v", formID, err),
			FormInput: r.FormValue("form_input"),
		})
		return
	}

	mapping, err := s.mapper.Map(r.Context(), meta.Questions)
	if err != nil {
		s.render(w, "page_new_form", newFormView{
			baseView:  s.base(r, "New form"),
			Error:     fmt.Sprintf("AI mapping failed: %v", err),
			FormInput: r.FormValue("form_input"),
		})
		return
	}

	s.renderPreview(w, r, formID, meta.Name, mapping, "", "", "")
}

// handlePreviewSamples re-renders only the sample-records preview for the
// current (possibly edited) mapping. The mapping screen posts the form here
// whenever a target or template changes, so the "First N submissions as NPS
// records" preview stays in sync without a full page reload. It reuses the same
// transform the real sync uses, so the preview can't drift from what gets
// written. The mapping is rebuilt from the form's authoritative question list
// (re-fetched) plus the submitted target_/template_ values.
func (s *Server) handlePreviewSamples(w http.ResponseWriter, r *http.Request) {
	formID := strings.TrimSpace(r.FormValue("form_id"))
	if formID == "" {
		http.Error(w, "missing form id", http.StatusBadRequest)
		return
	}

	meta, err := s.fillout.GetForm(r.Context(), formID)
	if err != nil {
		http.Error(w, fmt.Sprintf("couldn't reload form %s: %v", formID, err), http.StatusBadGateway)
		return
	}

	mapping := nps.BuildMapping(meta.Questions,
		func(qid string) string { return r.FormValue("target_" + qid) },
		func(qid string) string { return r.FormValue("template_" + qid) },
	)
	samples := s.buildSamples(r.Context(), formID, mapping, strings.TrimSpace(r.FormValue("ysws_program")), r.FormValue("tags"))
	s.render(w, "preview_samples", samplesView{Samples: samples})
}

// buildSamples fetches the first few submissions and transforms them into NPS
// records under the given mapping, YSWS link, and tags, for the preview screen.
// It mirrors the sync's own transform so the preview matches what gets written.
func (s *Server) buildSamples(ctx context.Context, formID string, mapping nps.Mapping, ysws, tags string) []sampleView {
	opts := nps.RecordOptions{YSWSProgram: ysws, Tags: nps.NormalizeTags(tags)}
	var samples []sampleView
	if page, err := s.fillout.GetSubmissions(ctx, formID, &fillout.GetSubmissionsParams{Limit: 5, Sort: fillout.SortDesc}); err == nil {
		for _, sub := range page.Responses {
			samples = append(samples, sampleView{
				SubmissionID:   sub.SubmissionID,
				SubmissionTime: sub.SubmissionTime.Format("2006-01-02 15:04"),
				Fields:         orderFields(nps.BuildFields(sub, mapping, opts)),
			})
		}
	}
	return samples
}

// renderPreview renders the mapping/preview screen. It fetches the program
// picker options and a few sample transformed records (reflecting the chosen
// YSWS link and tags). It is shared by the initial preview and the re-render
// shown when starting a sync fails validation.
func (s *Server) renderPreview(w http.ResponseWriter, r *http.Request, formID, formName string, mapping nps.Mapping, ysws, tags, errMsg string) {
	programs, err := nps.ListProgramNames(r.Context(), s.airtable)
	if err != nil {
		s.serverError(w, r, "loading YSWS programs", err)
		return
	}

	var examples []tagExample
	if ex, err := nps.SampleTagsByProgram(r.Context(), s.airtable, s.cfg.NPSTable, 6, 5); err == nil {
		for _, e := range ex {
			examples = append(examples, tagExample{Program: e.Program, Tags: strings.Join(e.Tags, ", ")})
		}
	}

	samples := s.buildSamples(r.Context(), formID, mapping, ysws, tags)

	s.render(w, "page_preview", previewView{
		baseView:    s.base(r, "Preview mapping"),
		Error:       errMsg,
		FormID:      formID,
		FormName:    formName,
		BaseID:      s.cfg.AirtableBaseID,
		Table:       s.cfg.NPSTable,
		YSWSProgram: ysws,
		Tags:        tags,
		TagExamples: examples,
		Programs:    programs,
		Targets:     nps.TargetOptions(),
		Mappings:    mapping.Entries,
		Samples:     samples,
	})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	formID := strings.TrimSpace(r.FormValue("form_id"))
	if formID == "" {
		http.Error(w, "missing form id", http.StatusBadRequest)
		return
	}

	// Re-fetch the form so the stored mapping is authoritative, then apply the
	// reviewer's (possibly edited) target choices.
	meta, err := s.fillout.GetForm(r.Context(), formID)
	if err != nil {
		s.render(w, "page_new_form", newFormView{
			baseView: s.base(r, "New form"),
			Error:    fmt.Sprintf("Couldn't reload form %s: %v", formID, err),
		})
		return
	}
	formName := strings.TrimSpace(meta.Name)
	tagsInput := r.FormValue("tags")
	mapping := nps.BuildMapping(meta.Questions,
		func(qid string) string { return r.FormValue("target_" + qid) },
		func(qid string) string { return r.FormValue("template_" + qid) },
	)

	// The YSWS program link is required and must be a real program.
	yswsInput := strings.TrimSpace(r.FormValue("ysws_program"))
	programs, err := nps.ListProgramNames(r.Context(), s.airtable)
	if err != nil {
		s.serverError(w, r, "loading YSWS programs", err)
		return
	}
	canonicalYSWS, ok := nps.CanonicalProgram(programs, yswsInput)
	if !ok {
		msg := "Please choose a YSWS program from the list to link these records to."
		if yswsInput != "" {
			msg = fmt.Sprintf("%q isn't a known YSWS program — pick one from the list.", yswsInput)
		}
		s.renderPreview(w, r, formID, formName, mapping, yswsInput, tagsInput, msg)
		return
	}

	mappingJSON, err := json.Marshal(mapping)
	if err != nil {
		s.serverError(w, r, "encoding mapping", err)
		return
	}

	tags := strings.Join(nps.NormalizeTags(tagsInput), ",")
	job := &db.SyncJob{
		FilloutFormID:   formID,
		FilloutFormName: formName,
		AirtableBaseID:  s.cfg.AirtableBaseID,
		AirtableTable:   s.cfg.NPSTable,
		Mapping:         mappingJSON,
		YSWSProgram:     canonicalYSWS,
		Tags:            tags,
		CreatedByEmail:  user.Email,
		Status:          db.StatusActive,
	}

	created, err := s.store.CreateJob(r.Context(), job)
	if errors.Is(err, db.ErrJobExists) {
		// This form is already set up. Explain that rather than duplicating it —
		// setting up a form is something every allow-listed user may attempt, so
		// hitting an existing sync is a normal outcome, not an access error.
		//
		// Only a manager (the owner or an admin) gets the existing sync ensured
		// running: a duplicate attempt must never let someone else flip another
		// user's deliberately stopped sync back on.
		formLabel := orDefault(created.FilloutFormName, formID)
		detail := fmt.Sprintf("Attempted to set up a duplicate sync for %q; existing job kept.", formLabel)
		restarted := false
		if s.canManageJob(user, created) {
			restarted = created.Status != db.StatusActive
			if err := s.store.SetJobStatus(r.Context(), created.ID, db.StatusActive); err != nil {
				s.serverError(w, r, "reactivating existing sync job", err)
				return
			}
			created.Status = db.StatusActive
			s.manager.StartJob(created)
		} else {
			detail += " Actor cannot manage that sync, so its status was left unchanged."
		}

		stats, err := s.store.StatsForJobs(r.Context(), []int64{created.ID})
		if err != nil {
			s.serverError(w, r, "loading job stats", err)
			return
		}

		httplog.Note(r, "form %s is already synced by job %d (set up by %s); no duplicate created",
			formID, created.ID, orDefault(created.CreatedByEmail, "an unknown user"))
		s.audit(r, db.AuditEntry{ActorEmail: user.Email, Action: db.ActionSyncDuplicateBlocked, SyncJobID: &created.ID, FormID: formID,
			Details: detail})
		s.render(w, "page_duplicate", duplicateView{
			baseView:  s.base(r, "Already syncing"),
			Job:       s.jobRowFor(user, created, stats[created.ID]),
			SetUpOn:   created.CreatedAt.Format("Jan 2, 2006"),
			Restarted: restarted,
		})
		return
	}
	if err != nil {
		s.serverError(w, r, "creating sync job", err)
		return
	}

	s.manager.StartJob(created)
	s.audit(r, db.AuditEntry{ActorEmail: user.Email, Action: db.ActionSyncCreated, SyncJobID: &created.ID, FormID: formID,
		Details: fmt.Sprintf("Created sync for %q → %s/%s, linked to YSWS %q%s.",
			orDefault(formName, formID), created.AirtableBaseID, created.AirtableTable, canonicalYSWS, tagsSummary(tags))})
	http.Redirect(w, r, "/?flash="+url.QueryEscape("Sync started for "+orDefault(created.FilloutFormName, created.FilloutFormID)), http.StatusFound)
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	id, ok := parseJobID(r.FormValue("job_id"))
	if !ok {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		s.serverError(w, r, "loading job", err)
		return
	}
	user, ok := s.requireCanManageJob(w, r, job)
	if !ok {
		return
	}
	if err := s.store.SetJobStatus(r.Context(), id, db.StatusStopped); err != nil {
		s.serverError(w, r, "stopping job", err)
		return
	}
	s.manager.StopJob(id)
	s.audit(r, db.AuditEntry{ActorEmail: user.Email, Action: db.ActionSyncStopped, SyncJobID: &id, Details: "Stopped sync."})
	http.Redirect(w, r, "/?flash="+url.QueryEscape("Sync stopped"), http.StatusFound)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	id, ok := parseJobID(r.FormValue("job_id"))
	if !ok {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		s.serverError(w, r, "loading job", err)
		return
	}
	user, ok := s.requireCanManageJob(w, r, job)
	if !ok {
		return
	}
	if err := s.store.SetJobStatus(r.Context(), id, db.StatusActive); err != nil {
		s.serverError(w, r, "resuming job", err)
		return
	}
	job.Status = db.StatusActive
	s.manager.StartJob(job)
	s.audit(r, db.AuditEntry{ActorEmail: user.Email, Action: db.ActionSyncResumed, SyncJobID: &id, FormID: job.FilloutFormID, Details: "Resumed sync."})
	http.Redirect(w, r, "/?flash="+url.QueryEscape("Sync resumed"), http.StatusFound)
}

type removeView struct {
	baseView
	JobID    int64
	FormName string
	FormID   string
	BaseID   string
	Table    string
	// Created is how many Airtable records this sync created (and could delete).
	Created int
}

// handleRemoveConfirm shows the confirmation page for removing a sync, including
// how many Airtable records it created so the user can choose to delete them.
func (s *Server) handleRemoveConfirm(w http.ResponseWriter, r *http.Request) {
	id, ok := parseJobID(r.URL.Query().Get("job_id"))
	if !ok {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	job, err := s.store.GetJob(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		http.Redirect(w, r, "/?flash="+url.QueryEscape("That sync was already removed."), http.StatusFound)
		return
	}
	if err != nil {
		s.serverError(w, r, "loading job", err)
		return
	}
	if _, ok := s.requireCanManageJob(w, r, job); !ok {
		return
	}
	stats, err := s.store.StatsForJob(r.Context(), id)
	if err != nil {
		s.serverError(w, r, "loading job stats", err)
		return
	}
	s.render(w, "page_remove_confirm", removeView{
		baseView: s.base(r, "Remove sync"),
		JobID:    job.ID,
		FormName: orDefault(job.FilloutFormName, job.FilloutFormID),
		FormID:   job.FilloutFormID,
		BaseID:   job.AirtableBaseID,
		Table:    job.AirtableTable,
		Created:  stats.Created,
	})
}

// handleRemove stops a job's poller and deletes the job (its ledger rows cascade
// away). When "delete_records" is set it also deletes, best-effort, the Airtable
// records the sync created — captured from the ledger after the poller is stopped
// and before the job is deleted. Removing the sync always succeeds; record
// deletion is reported in the flash.
func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	id, ok := parseJobID(r.FormValue("job_id"))
	if !ok {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	job, err := s.store.GetJob(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		http.Redirect(w, r, "/?flash="+url.QueryEscape("That sync was already removed."), http.StatusFound)
		return
	}
	if err != nil {
		s.serverError(w, r, "loading job", err)
		return
	}
	user, ok := s.requireCanManageJob(w, r, job)
	if !ok {
		return
	}
	deleteRecords := r.FormValue("delete_records") != ""

	if err := s.store.SetJobStatus(r.Context(), id, db.StatusStopped); err != nil {
		s.serverError(w, r, "stopping job", err)
		return
	}
	s.manager.StopJob(id)

	// Capture the record IDs while the stopped job's ledger still exists
	// (DeleteJob cascades it).
	var recordIDs []string
	if deleteRecords {
		recordIDs, err = s.store.CreatedRecordIDs(r.Context(), id)
		if err != nil {
			s.serverError(w, r, "loading created records", err)
			return
		}
	}

	if err := s.store.DeleteJob(r.Context(), id); err != nil {
		s.serverError(w, r, "removing job", err)
		return
	}

	formName := orDefault(job.FilloutFormName, job.FilloutFormID)

	// Job is gone; record deletion is best-effort and reported, not fatal.
	if deleteRecords && len(recordIDs) > 0 {
		deleted, delErr := s.airtable.DeleteRecords(r.Context(), job.AirtableTable, recordIDs)
		if delErr != nil {
			log.Printf("removing job %d: deleting airtable records: %v", id, delErr)
			s.audit(r, db.AuditEntry{ActorEmail: user.Email, Action: db.ActionSyncRemoved, SyncJobID: &id, FormID: job.FilloutFormID,
				Details: fmt.Sprintf("Removed sync for %q; deleted %d of %d Airtable records before an error: %v", formName, len(deleted), len(recordIDs), delErr)})
			http.Redirect(w, r, "/?flash="+url.QueryEscape(fmt.Sprintf("Removed sync for %s. Deleted %d of %d Airtable records; some could not be deleted — check Airtable.", formName, len(deleted), len(recordIDs))), http.StatusFound)
			return
		}
		s.audit(r, db.AuditEntry{ActorEmail: user.Email, Action: db.ActionSyncRemoved, SyncJobID: &id, FormID: job.FilloutFormID,
			Details: fmt.Sprintf("Removed sync for %q and deleted %d Airtable record(s) it created.", formName, len(deleted))})
		http.Redirect(w, r, "/?flash="+url.QueryEscape(fmt.Sprintf("Removed sync for %s and deleted %d Airtable record(s).", formName, len(deleted))), http.StatusFound)
		return
	}

	s.audit(r, db.AuditEntry{ActorEmail: user.Email, Action: db.ActionSyncRemoved, SyncJobID: &id, FormID: job.FilloutFormID,
		Details: fmt.Sprintf("Removed sync for %q; Airtable records kept.", formName)})
	http.Redirect(w, r, "/?flash="+url.QueryEscape("Removed sync for "+formName+"."), http.StatusFound)
}

type auditView struct {
	baseView
	Logs []auditRow
}

type auditRow struct {
	When   string
	Actor  string
	Action string
	JobID  string
	FormID string
	Detail string
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	logs, err := s.store.ListAuditLogs(r.Context(), 200)
	if err != nil {
		s.serverError(w, r, "loading audit logs", err)
		return
	}
	rows := make([]auditRow, 0, len(logs))
	for _, l := range logs {
		job := ""
		if l.SyncJobID != nil {
			job = "#" + strconv.FormatInt(*l.SyncJobID, 10)
		}
		rows = append(rows, auditRow{
			When:   l.CreatedAt.Format("2006-01-02 15:04:05"),
			Actor:  l.ActorEmail,
			Action: l.Action,
			JobID:  job,
			FormID: l.FormID,
			Detail: l.Details,
		})
	}
	s.render(w, "page_audit", auditView{baseView: s.base(r, "Audit log"), Logs: rows})
}

// --- helpers ---

// audit records an audit entry, logging (but not failing the request) on error.
func (s *Server) audit(r *http.Request, e db.AuditEntry) {
	if err := s.store.RecordAudit(r.Context(), e); err != nil {
		log.Printf("audit %s: %v", e.Action, err)
	}
}

// requestUser resolves the caller and records them on the request's log line,
// so every logged request — not only the ones that go through RequireUser — says
// who made it.
func (s *Server) requestUser(r *http.Request) (auth.User, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		user, ok = s.auth.CurrentUser(r)
	}
	if ok {
		httplog.SetUser(r, user.Email)
	}
	return user, ok
}

// jobRowFor renders one sync job for display: its ledger counts, whether the
// poller is live, and whether this viewer may manage it.
func (s *Server) jobRowFor(user auth.User, j *db.SyncJob, stats db.JobStats) jobRow {
	return jobRow{
		ID:            j.ID,
		FormName:      j.FilloutFormName,
		FormID:        j.FilloutFormID,
		BaseID:        j.AirtableBaseID,
		Table:         j.AirtableTable,
		YSWS:          j.YSWSProgram,
		Tags:          j.Tags,
		CreatedBy:     j.CreatedByEmail,
		Status:        j.Status,
		Running:       s.manager.IsRunning(j.ID),
		Created:       stats.Created,
		Skipped:       stats.SkippedExisting,
		Errored:       stats.Errored,
		LastPolled:    formatTime(j.LastPolledAt),
		LastPolledISO: isoTime(j.LastPolledAt),
		LastError:     j.LastError,
		CanManage:     s.canManageJob(user, j),
	}
}

func (s *Server) canManageJob(user auth.User, job *db.SyncJob) bool {
	if s.cfg.IsAdmin(user.Email) {
		return true
	}
	owner := strings.TrimSpace(job.CreatedByEmail)
	if owner == "" {
		return false
	}
	return strings.EqualFold(owner, strings.TrimSpace(user.Email))
}

func (s *Server) requireCanManageJob(w http.ResponseWriter, r *http.Request, job *db.SyncJob) (auth.User, bool) {
	user, ok := s.requestUser(r)
	if !ok || !s.canManageJob(user, job) {
		httplog.Note(r, "%s may not manage sync job %d (form %s, set up by %s) and is not an admin",
			orDefault(user.Email, "an unauthenticated request"), job.ID, job.FilloutFormID, orDefault(job.CreatedByEmail, "an unknown user"))
		http.Error(w, "forbidden", http.StatusForbidden)
		return auth.User{}, false
	}
	return user, true
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
	user, ok := s.requestUser(r)
	if !ok || !s.cfg.IsAdmin(user.Email) {
		httplog.Note(r, "%s is not in ADMIN_EMAILS (%d configured)",
			orDefault(user.Email, "an unauthenticated request"), len(s.cfg.AdminEmails))
		http.Error(w, "forbidden", http.StatusForbidden)
		return auth.User{}, false
	}
	return user, true
}

func tagsSummary(tags string) string {
	if strings.TrimSpace(tags) == "" {
		return ""
	}
	return " tagged " + tags
}

func (s *Server) base(r *http.Request, title string) baseView {
	user, _ := s.requestUser(r)
	return baseView{Title: title, UserEmail: user.Email, IsAdmin: s.cfg.IsAdmin(user.Email), CSRF: s.auth.CSRFToken(r)}
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, what string, err error) {
	httplog.Note(r, "%s: %v", what, err)
	log.Printf("%s: %v", what, err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// orderFields returns NPS record fields in a stable, human-friendly order. The
// rich-text columns (the templatable feedback fields and the Custom Fields
// catch-all) are rendered the way Airtable displays them; everything else is
// shown as escaped plain text. Custom Fields is shown last.
func orderFields(fields map[string]any) []fieldKV {
	var out []fieldKV
	add := func(name string, rich bool) {
		v, ok := fields[name]
		if !ok {
			return
		}
		value := template.HTML(template.HTMLEscapeString(formatValue(v)))
		if rich {
			value = renderRichText(formatValue(v))
		}
		out = append(out, fieldKV{Name: name, Value: value, Rich: rich})
	}
	for _, tf := range nps.TargetFields {
		add(tf.AirtableName, tf.Templatable)
	}
	add(nps.FieldYSWS, false)
	add(nps.FieldOverrideCreatedAt, false)
	add(nps.FieldCustomFields, true)
	return out
}

func formatValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []string:
		return strings.Join(t, ", ")
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// formatTime renders the no-JS fallback for a timestamp. The zone is named
// explicitly (UTC) so a viewer without JavaScript is not misled into reading
// server time as their own local time; the client localizes it when JS runs.
func formatTime(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// isoTime renders a timestamp as an RFC3339 UTC value: an absolute instant the
// browser can localize to the viewer's timezone. It returns "" for a nil time so
// the template can omit the <time> element entirely.
func isoTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func parseJobID(s string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// parseFormID extracts a Fillout form ID from a pasted URL or returns the input
// unchanged when it is already a bare ID. It understands the common Fillout URL
// shapes, where the ID follows a marker segment:
//
//	https://forms.fillout.com/t/<id>
//	https://build.fillout.com/editor/<id>/edit
//	https://build.fillout.com/editor/<id>
//
// and falls back to the last meaningful path segment otherwise.
func parseFormID(input string) string {
	s := strings.TrimSpace(input)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return s // already a bare ID (or not a URL)
	}

	var segs []string
	for _, p := range strings.Split(u.Path, "/") {
		if p = strings.TrimSpace(p); p != "" {
			segs = append(segs, p)
		}
	}
	if len(segs) == 0 {
		return s
	}

	// The form ID directly follows one of these marker segments.
	markers := map[string]bool{"t": true, "editor": true, "forms": true, "flow": true}
	for i, seg := range segs {
		if markers[strings.ToLower(seg)] && i+1 < len(segs) {
			return segs[i+1]
		}
	}

	// Otherwise take the last segment, skipping trailing editor verbs.
	skip := map[string]bool{"edit": true, "view": true, "preview": true, "responses": true, "results": true, "share": true, "analytics": true, "settings": true, "summary": true}
	for i := len(segs) - 1; i >= 0; i-- {
		if !skip[strings.ToLower(segs[i])] {
			return segs[i]
		}
	}
	return segs[len(segs)-1]
}
