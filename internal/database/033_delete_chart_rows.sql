-- Deleting a song removes its row. Upload receipts outlive it with a NULL
-- chart so a retried upload still reports the song as removed.
ALTER TABLE upload_requests ALTER COLUMN chart_id DROP NOT NULL,
 DROP CONSTRAINT upload_requests_chart_id_fkey,
 ADD CONSTRAINT upload_requests_chart_id_fkey FOREIGN KEY(chart_id) REFERENCES charts(id) ON DELETE SET NULL;
-- Remove the tombstones of earlier deletions; their resources cascade and the
-- resource trigger queues any remaining files.
DELETE FROM scores WHERE song_id IN (SELECT id FROM charts WHERE status='deleted');
DELETE FROM charts WHERE status='deleted';
