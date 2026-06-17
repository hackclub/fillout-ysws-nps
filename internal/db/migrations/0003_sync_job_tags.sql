-- Job-level source tags applied to every record a sync writes (e.g.
-- "2026-06-15_2nd_newsletter_feedback"), stored comma-separated.
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS tags TEXT NOT NULL DEFAULT '';
