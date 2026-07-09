ALTER TABLE synced_submissions
  DROP CONSTRAINT IF EXISTS synced_submissions_fillout_form_id_fillout_submission_id_key;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'synced_submissions_sync_job_submission_key'
  ) THEN
    ALTER TABLE synced_submissions
      ADD CONSTRAINT synced_submissions_sync_job_submission_key
      UNIQUE (sync_job_id, fillout_submission_id);
  END IF;
END $$;
