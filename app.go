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
	"github.com/hackclub/fillout-ysws-nps/internal/nps"
)

//go:embed templates/*.html
var templatesFS embed.FS

// formClient is the subset of the Fillout client the web layer needs.
type formClient interface {
	GetForm(ctx context.Context, formID string) (*fillout.FormMetadata, error)
	GetSubmissions(ctx context.Context, formID string, params *fillout.GetSubmissionsParams) (*fillout.SubmissionsPage, error)
}

// Server holds the application's HTTP dependencies.
type Server struct {
	cfg      *config.Config
	auth     *auth.Authenticator
	store    *db.DB
	fillout  formClient
	airtable nps.AirtableAPI
	mapper   *nps.Mapper
	manager  *nps.Manager
	tmpl     *template.Template
}

// NewServer constructs a Server and parses templates.
func NewServer(cfg *config.Config, a *auth.Authenticator, store *db.DB, fc formClient, at nps.AirtableAPI, mapper *nps.Mapper, manager *nps.Manager) (*Server, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
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
	mux.HandleFunc("GET /{$}", s.handleHome)

	mux.Handle("GET /forms/new", s.auth.RequireUser(http.HandlerFunc(s.handleNewForm)))
	mux.Handle("POST /forms/preview", s.auth.RequireUser(http.HandlerFunc(s.handlePreview)))
	mux.Handle("POST /sync/start", s.auth.RequireUser(http.HandlerFunc(s.handleStart)))
	mux.Handle("POST /sync/stop", s.auth.RequireUser(http.HandlerFunc(s.handleStop)))
	mux.Handle("POST /sync/resume", s.auth.RequireUser(http.HandlerFunc(s.handleResume)))
	mux.Handle("GET /audit", s.auth.RequireUser(http.HandlerFunc(s.handleAudit)))
	return mux
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
}

type messageView struct {
	baseView
	Heading string
	Message string
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
	ID         int64
	FormName   string
	FormID     string
	BaseID     string
	Table      string
	YSWS       string
	Tags       string
	CreatedBy  string
	Status     string
	Running    bool
	Created    int
	Skipped    int
	Errored    int
	LastPolled string
	LastError  string
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

type sampleView struct {
	SubmissionID   string
	SubmissionTime string
	Fields         []fieldKV
}

type fieldKV struct {
	Name  string
	Value string
	Pre   bool
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
		s.serverError(w, "loading jobs", err)
		return
	}
	rows := make([]jobRow, 0, len(jobs))
	for _, j := range jobs {
		stats, err := s.store.StatsForJob(r.Context(), j.ID)
		if err != nil {
			s.serverError(w, "loading job stats", err)
			return
		}
		rows = append(rows, jobRow{
			ID:         j.ID,
			FormName:   j.FilloutFormName,
			FormID:     j.FilloutFormID,
			BaseID:     j.AirtableBaseID,
			Table:      j.AirtableTable,
			YSWS:       j.YSWSProgram,
			Tags:       j.Tags,
			CreatedBy:  j.CreatedByEmail,
			Status:     j.Status,
			Running:    s.manager.IsRunning(j.ID),
			Created:    stats.Created,
			Skipped:    stats.SkippedExisting,
			Errored:    stats.Errored,
			LastPolled: formatTime(j.LastPolledAt),
			LastError:  j.LastError,
		})
	}

	s.render(w, "page_dashboard", dashboardView{
		baseView: baseView{Title: "Dashboard", UserEmail: user.Email},
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

// renderPreview renders the mapping/preview screen. It fetches the program
// picker options and a few sample transformed records (reflecting the chosen
// YSWS link and tags). It is shared by the initial preview and the re-render
// shown when starting a sync fails validation.
func (s *Server) renderPreview(w http.ResponseWriter, r *http.Request, formID, formName string, mapping nps.Mapping, ysws, tags, errMsg string) {
	programs, err := nps.ListProgramNames(r.Context(), s.airtable)
	if err != nil {
		s.serverError(w, "loading YSWS programs", err)
		return
	}

	var examples []tagExample
	if ex, err := nps.SampleTagsByProgram(r.Context(), s.airtable, s.cfg.NPSTable, 6, 5); err == nil {
		for _, e := range ex {
			examples = append(examples, tagExample{Program: e.Program, Tags: strings.Join(e.Tags, ", ")})
		}
	}

	opts := nps.RecordOptions{YSWSProgram: ysws, Tags: nps.NormalizeTags(tags)}
	var samples []sampleView
	if page, err := s.fillout.GetSubmissions(r.Context(), formID, &fillout.GetSubmissionsParams{Limit: 5, Sort: fillout.SortDesc}); err == nil {
		for _, sub := range page.Responses {
			samples = append(samples, sampleView{
				SubmissionID:   sub.SubmissionID,
				SubmissionTime: sub.SubmissionTime.Format("2006-01-02 15:04"),
				Fields:         orderFields(nps.BuildFields(sub, mapping, opts)),
			})
		}
	}

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
	formName := strings.TrimSpace(r.FormValue("form_name"))
	tagsInput := r.FormValue("tags")
	mapping := nps.BuildMapping(meta.Questions,
		func(qid string) string { return r.FormValue("target_" + qid) },
		func(qid string) string { return r.FormValue("template_" + qid) },
	)

	// The YSWS program link is required and must be a real program.
	yswsInput := strings.TrimSpace(r.FormValue("ysws_program"))
	programs, err := nps.ListProgramNames(r.Context(), s.airtable)
	if err != nil {
		s.serverError(w, "loading YSWS programs", err)
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
		s.serverError(w, "encoding mapping", err)
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
		// Gracefully handle a sync that's already set up (possibly by someone
		// else): make sure it's running and explain rather than duplicating.
		if err := s.store.SetJobStatus(r.Context(), created.ID, db.StatusActive); err != nil {
			s.serverError(w, "reactivating existing sync job", err)
			return
		}
		created.Status = db.StatusActive
		s.manager.StartJob(created)
		s.audit(r, db.AuditEntry{ActorEmail: user.Email, Action: db.ActionSyncDuplicateBlocked, SyncJobID: &created.ID, FormID: formID,
			Details: fmt.Sprintf("Attempted to set up a duplicate sync for %q; existing job kept.", orDefault(created.FilloutFormName, formID))})
		s.render(w, "page_message", messageView{
			baseView: s.base(r, "Already syncing"),
			Heading:  "This form is already being synced",
			Message: fmt.Sprintf("A sync for this form into %s / %s was set up by %s on %s. It's running now — no duplicate was created.",
				created.AirtableBaseID, created.AirtableTable, orDefault(created.CreatedByEmail, "another user"), created.CreatedAt.Format("Jan 2, 2006")),
		})
		return
	}
	if err != nil {
		s.serverError(w, "creating sync job", err)
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
	s.manager.StopJob(id)
	if err := s.store.SetJobStatus(r.Context(), id, db.StatusStopped); err != nil {
		s.serverError(w, "stopping job", err)
		return
	}
	if user, ok := auth.UserFromContext(r.Context()); ok {
		s.audit(r, db.AuditEntry{ActorEmail: user.Email, Action: db.ActionSyncStopped, SyncJobID: &id, Details: "Stopped sync."})
	}
	http.Redirect(w, r, "/?flash="+url.QueryEscape("Sync stopped"), http.StatusFound)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	id, ok := parseJobID(r.FormValue("job_id"))
	if !ok {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	if err := s.store.SetJobStatus(r.Context(), id, db.StatusActive); err != nil {
		s.serverError(w, "resuming job", err)
		return
	}
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		s.serverError(w, "loading job", err)
		return
	}
	s.manager.StartJob(job)
	if user, ok := auth.UserFromContext(r.Context()); ok {
		s.audit(r, db.AuditEntry{ActorEmail: user.Email, Action: db.ActionSyncResumed, SyncJobID: &id, FormID: job.FilloutFormID, Details: "Resumed sync."})
	}
	http.Redirect(w, r, "/?flash="+url.QueryEscape("Sync resumed"), http.StatusFound)
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
	logs, err := s.store.ListAuditLogs(r.Context(), 200)
	if err != nil {
		s.serverError(w, "loading audit logs", err)
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

func tagsSummary(tags string) string {
	if strings.TrimSpace(tags) == "" {
		return ""
	}
	return " tagged " + tags
}

func (s *Server) base(r *http.Request, title string) baseView {
	user, _ := s.auth.CurrentUser(r)
	return baseView{Title: title, UserEmail: user.Email}
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

func (s *Server) serverError(w http.ResponseWriter, what string, err error) {
	log.Printf("%s: %v", what, err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// orderFields returns NPS record fields in a stable, human-friendly order, with
// the Custom Fields rich text shown last and preformatted.
func orderFields(fields map[string]any) []fieldKV {
	var out []fieldKV
	add := func(name string, pre bool) {
		if v, ok := fields[name]; ok {
			out = append(out, fieldKV{Name: name, Value: formatValue(v), Pre: pre})
		}
	}
	for _, tf := range nps.TargetFields {
		add(tf.AirtableName, false)
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

func formatTime(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Format("2006-01-02 15:04")
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
