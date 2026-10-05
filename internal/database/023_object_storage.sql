-- Additive compatibility: local backends can continue using cover bytes.
ALTER TABLE chart_covers ALTER COLUMN webp DROP NOT NULL;
ALTER TABLE chart_covers ADD COLUMN storage_key text;
ALTER TABLE chart_covers ADD COLUMN byte_size bigint;
ALTER TABLE chart_covers ADD CONSTRAINT chart_cover_payload CHECK (
 webp IS NOT NULL OR (storage_key IS NOT NULL AND byte_size IS NOT NULL AND byte_size BETWEEN 12 AND 8388608)
);
CREATE TABLE chart_archives (
 version_id text PRIMARY KEY REFERENCES chart_versions(id) ON DELETE CASCADE,
 storage_key text NOT NULL UNIQUE,
 sha256 text NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
 byte_size bigint NOT NULL CHECK (byte_size > 0)
);
-- Register intent before external writes; crash leftovers are reconciled later.
CREATE TABLE pending_objects (
 storage_key text PRIMARY KEY,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION retire_resource_object() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.storage_key IS NOT NULL THEN
  INSERT INTO retired_files(storage_key) VALUES(OLD.storage_key) ON CONFLICT DO NOTHING;
 END IF;
 RETURN OLD;
END;
$$;
CREATE TRIGGER retire_cover_object AFTER DELETE ON chart_covers FOR EACH ROW EXECUTE FUNCTION retire_resource_object();
CREATE TRIGGER retire_archive_object AFTER DELETE ON chart_archives FOR EACH ROW EXECUTE FUNCTION retire_resource_object();
