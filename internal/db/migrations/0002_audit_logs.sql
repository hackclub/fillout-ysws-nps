-- audit_logs: an append-only record of who configured what, when. Not tied to
-- sync_jobs by FK so entries survive job deletion.
CREATE TABLE IF NOT EXISTS audit_logs (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_email TEXT        NOT NULL,
    action      TEXT        NOT NULL,
    sync_job_id BIGINT,
    form_id     TEXT        NOT NULL DEFAULT '',
    details     TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs (created_at DESC);
