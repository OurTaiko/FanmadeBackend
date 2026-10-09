-- Verify the actual stored TJA bytes before upgrading an existing installation.
-- Refuse to discard metadata for charts still declared in a legacy encoding.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM charts WHERE encoding <> 'utf-8') THEN
  RAISE EXCEPTION 'non-UTF-8 charts remain; convert and verify stored TJA resources before migration 035';
 END IF;
END $$;
ALTER TABLE charts DROP COLUMN encoding;
