package nps

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"sync"
	"time"

	"github.com/hackclub/fillout-ysws-nps/fillout"
	"github.com/hackclub/fillout-ysws-nps/internal/db"
)

// syncBatchSize is how many submissions are buffered and processed per batch.
const syncBatchSize = 50

// Store is the persistence the sync poller needs. The real *db.DB satisfies it.
type Store interface {
	ListActiveJobs(ctx context.Context) ([]*db.SyncJob, error)
	ExistingSubmissionIDs(ctx context.Context, formID string, submissionIDs []string) (map[string]bool, error)
	RecordSubmissions(ctx context.Context, recs []db.SubmissionRecord) (int, error)
	UpdateJobProgress(ctx context.Context, id int64, cursor *time.Time, polledAt time.Time, lastErr string) error
}

// FilloutAPI is the subset of the Fillout client the poller needs.
type FilloutAPI interface {
	AllSubmissions(ctx context.Context, formID string, params *fillout.GetSubmissionsParams) iter.Seq2[fillout.Submission, error]
}

// Manager runs a background poller goroutine per active sync job. Each poller
// fetches new Fillout submissions, deduplicates, and writes NPS records.
type Manager struct {
	store    Store
	fillout  FilloutAPI
	airtable AirtableAPI
	interval time.Duration
	logger   *log.Logger

	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
	baseCtx context.Context
	wg      sync.WaitGroup
}

// NewManager builds a Manager. A nil logger defaults to the standard logger.
func NewManager(store Store, fc FilloutAPI, ac AirtableAPI, interval time.Duration, logger *log.Logger) *Manager {
	if logger == nil {
		logger = log.Default()
	}
	return &Manager{
		store:    store,
		fillout:  fc,
		airtable: ac,
		interval: interval,
		logger:   logger,
		cancels:  make(map[int64]context.CancelFunc),
	}
}

// Start records the base context governing all pollers and launches one for each
// currently active job.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.baseCtx = ctx
	m.mu.Unlock()

	jobs, err := m.store.ListActiveJobs(ctx)
	if err != nil {
		return fmt.Errorf("nps: loading active jobs: %w", err)
	}
	for _, job := range jobs {
		m.StartJob(job)
	}
	m.logger.Printf("sync manager started with %d active job(s)", len(jobs))
	return nil
}

// StartJob launches a poller for job. It is a no-op if one is already running.
func (m *Manager) StartJob(job *db.SyncJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.baseCtx == nil {
		m.baseCtx = context.Background()
	}
	if _, running := m.cancels[job.ID]; running {
		return
	}
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.cancels[job.ID] = cancel
	m.wg.Add(1)
	go m.run(ctx, job)
}

// StopJob cancels a job's poller if running.
func (m *Manager) StopJob(id int64) {
	m.mu.Lock()
	cancel, ok := m.cancels[id]
	delete(m.cancels, id)
	m.mu.Unlock()
	if ok {
		cancel()
	}
}

// IsRunning reports whether a poller is active for the job.
func (m *Manager) IsRunning(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.cancels[id]
	return ok
}

// Shutdown cancels every poller and waits for them to exit.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	for id, cancel := range m.cancels {
		cancel()
		delete(m.cancels, id)
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) run(ctx context.Context, job *db.SyncJob) {
	defer m.wg.Done()
	defer func() {
		m.mu.Lock()
		delete(m.cancels, job.ID)
		m.mu.Unlock()
	}()

	mapping, err := parseMapping(job.Mapping)
	if err != nil {
		m.logger.Printf("sync job %d: %v; poller not started", job.ID, err)
		return
	}

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	cursor := job.Cursor
	cursor = m.syncOnce(ctx, job, mapping, cursor)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cursor = m.syncOnce(ctx, job, mapping, cursor)
		}
	}
}

// syncOnce performs a single polling pass. A producer goroutine streams
// submissions newer than the cursor from Fillout and groups them into batches,
// while the consumer writes each batch to Airtable — so the next Fillout page is
// fetched in the background while the current batch is being written. It
// persists progress and returns the advanced cursor. On the first batch error it
// stops, leaving the cursor at the last fully processed submission so the next
// pass retries from there (dedup makes any reprocessing harmless).
func (m *Manager) syncOnce(ctx context.Context, job *db.SyncJob, mapping Mapping, cursor *time.Time) *time.Time {
	maxProcessed := cursor

	params := &fillout.GetSubmissionsParams{Sort: fillout.SortAsc}
	if cursor != nil {
		// Query a second early so same-second siblings are never skipped; the
		// dedup layer absorbs the small overlap.
		params.AfterDate = cursor.Add(-time.Second)
	}

	opts := RecordOptions{YSWSProgram: job.YSWSProgram, Tags: NormalizeTags(job.Tags)}

	// Producer: fetch Fillout pages and emit batches; the buffered channel lets
	// it prefetch the next batch while the consumer is writing to Airtable.
	prodCtx, stopProducer := context.WithCancel(ctx)
	defer stopProducer()
	batches := make(chan []fillout.Submission, 1)
	var prodErr error
	go func() {
		defer close(batches)
		buf := make([]fillout.Submission, 0, syncBatchSize)
		send := func(b []fillout.Submission) bool {
			select {
			case batches <- b:
				return true
			case <-prodCtx.Done():
				return false
			}
		}
		for sub, err := range m.fillout.AllSubmissions(prodCtx, job.FilloutFormID, params) {
			if err != nil {
				prodErr = err
				return
			}
			if prodCtx.Err() != nil {
				return
			}
			buf = append(buf, sub)
			if len(buf) >= syncBatchSize {
				if !send(buf) {
					return
				}
				buf = make([]fillout.Submission, 0, syncBatchSize)
			}
		}
		if len(buf) > 0 {
			send(buf)
		}
	}()

	// Consumer: write each batch to Airtable as it arrives.
	var syncErr error
	for batch := range batches {
		if err := m.syncBatch(ctx, job, mapping, opts, batch, &maxProcessed); err != nil {
			syncErr = err
			stopProducer()
			for range batches { // drain so the producer goroutine can exit
			}
			break
		}
	}
	if syncErr == nil {
		syncErr = prodErr // safe: producer has finished once batches is closed
	}

	lastErr := ""
	if syncErr != nil {
		lastErr = syncErr.Error()
		m.logger.Printf("sync job %d: %v", job.ID, syncErr)
	}
	// Persist progress even if ctx was cancelled (graceful stop) so a resume
	// continues from here.
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.store.UpdateJobProgress(persistCtx, job.ID, maxProcessed, time.Now(), lastErr); err != nil {
		m.logger.Printf("sync job %d: persisting progress: %v", job.ID, err)
	}
	return maxProcessed
}

// syncBatch deduplicates and writes one batch of submissions, minimizing API
// calls: one ledger lookup, batched Airtable existence checks, and batched
// record creation. It records every outcome in the ledger and advances
// maxProcessed across all seen submissions.
func (m *Manager) syncBatch(ctx context.Context, job *db.SyncJob, mapping Mapping, opts RecordOptions, batch []fillout.Submission, maxProcessed **time.Time) error {
	// Authoritative "already synced" check against our own ledger. Because this
	// runs before any Airtable lookup, a submission we've recorded is never
	// re-created — so manually deleting its record in Airtable (e.g. a test
	// record) does NOT cause it to be re-synced later.
	ids := make([]string, len(batch))
	for i, s := range batch {
		ids[i] = s.SubmissionID
	}
	synced, err := m.store.ExistingSubmissionIDs(ctx, job.FilloutFormID, ids)
	if err != nil {
		return err
	}

	var candidates []fillout.Submission
	for _, s := range batch {
		if !synced[s.SubmissionID] {
			candidates = append(candidates, s)
		}
	}

	if len(candidates) > 0 {
		candIDs := make([]string, len(candidates))
		for i, s := range candidates {
			candIDs[i] = s.SubmissionID
		}
		stamped, err := FindStampedRecords(ctx, m.airtable, job.AirtableTable, candIDs)
		if err != nil {
			return err
		}

		ledger := make([]db.SubmissionRecord, 0, len(candidates))
		var toCreate []fillout.Submission
		for _, s := range candidates {
			if rid, ok := stamped[s.SubmissionID]; ok {
				ledger = append(ledger, db.SubmissionRecord{
					SyncJobID:           job.ID,
					FilloutFormID:       job.FilloutFormID,
					FilloutSubmissionID: s.SubmissionID,
					AirtableRecordID:    rid,
					Outcome:             db.OutcomeSkippedExisting,
				})
			} else {
				toCreate = append(toCreate, s)
			}
		}

		var createErr error
		if len(toCreate) > 0 {
			fieldsList := make([]map[string]any, len(toCreate))
			for i, s := range toCreate {
				fieldsList[i] = BuildFields(s, mapping, opts)
			}
			// CreateRecords chunks into Airtable's 10-per-request limit internally
			// and returns created records in order (partial on error).
			created, cerr := m.airtable.CreateRecords(ctx, job.AirtableTable, fieldsList, true)
			for i, rec := range created {
				if i >= len(toCreate) {
					break
				}
				ledger = append(ledger, db.SubmissionRecord{
					SyncJobID:           job.ID,
					FilloutFormID:       job.FilloutFormID,
					FilloutSubmissionID: toCreate[i].SubmissionID,
					AirtableRecordID:    rec.ID,
					Outcome:             db.OutcomeCreated,
				})
			}
			createErr = cerr
		}

		// Record the ledger (including any partial creates) before surfacing a
		// create error, so retries never duplicate already-written records.
		_, recErr := m.store.RecordSubmissions(ctx, ledger)
		if createErr != nil {
			return createErr // cursor not advanced: failed submissions are retried
		}
		if recErr != nil {
			return recErr
		}
	}

	// Whole batch handled — advance the cursor across all seen submissions.
	for _, s := range batch {
		if *maxProcessed == nil || s.SubmissionTime.After(**maxProcessed) {
			t := s.SubmissionTime
			*maxProcessed = &t
		}
	}
	return nil
}

func parseMapping(raw json.RawMessage) (Mapping, error) {
	var m Mapping
	if len(raw) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("nps: parsing stored mapping: %w", err)
	}
	return m, nil
}
