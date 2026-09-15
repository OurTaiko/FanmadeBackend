-- Receipts outlive replaced versions so retries cannot repeat a destructive update.
ALTER TABLE upload_requests ADD COLUMN version_id text NOT NULL DEFAULT '';
UPDATE upload_requests r SET version_id=c.current_version_id FROM charts c WHERE c.id=r.chart_id;

-- Committed deletions survive process crashes and temporary filesystem failures.
CREATE TABLE retired_files (
 storage_key text PRIMARY KEY,
 created_at timestamptz NOT NULL DEFAULT now()
);
