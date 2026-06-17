-- sync_jobs: one row per configured Fillout form -> Airtable table sync.
-- The UNIQUE constraint lets us detect when a sync for the same form/target has
-- already been set up (possibly by someone else) instead of creating a duplicate.
CREATE TABLE IF NOT EXISTS sync_jobs (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    fillout_form_id   TEXT        NOT NULL,
    fillout_form_name TEXT        NOT NULL DEFAULT '',
    airtable_base_id  TEXT        NOT NULL,
    airtable_table    TEXT        NOT NULL,
    mapping           JSONB       NOT NULL,
    ysws_program      TEXT        NOT NULL DEFAULT '',
    status            TEXT        NOT NULL DEFAULT 'active',
    cursor            TIMESTAMPTZ,
    created_by_email  TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_polled_at    TIMESTAMPTZ,
    last_error        TEXT        NOT NULL DEFAULT '',
    UNIQUE (fillout_form_id, airtable_base_id, airtable_table)
);

-- synced_submissions: ledger of every Fillout submission we have processed.
-- The UNIQUE constraint is the fast-path dedup so re-polling the same submission
-- is a no-op even across restarts.
CREATE TABLE IF NOT EXISTS synced_submissions (
    id                    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    sync_job_id           BIGINT      NOT NULL REFERENCES sync_jobs(id) ON DELETE CASCADE,
    fillout_form_id       TEXT        NOT NULL,
    fillout_submission_id TEXT        NOT NULL,
    airtable_record_id    TEXT        NOT NULL DEFAULT '',
    outcome               TEXT        NOT NULL,
    error_text            TEXT        NOT NULL DEFAULT '',
    synced_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (fillout_form_id, fillout_submission_id)
);

CREATE INDEX IF NOT EXISTS idx_synced_submissions_job ON synced_submissions (sync_job_id);
