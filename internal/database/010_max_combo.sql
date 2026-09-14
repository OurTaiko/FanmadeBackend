-- Initialize existing development rows, then require an explicit value on inserts.
ALTER TABLE scores ADD COLUMN max_combo integer NOT NULL DEFAULT 0 CHECK (max_combo >= 0);
ALTER TABLE scores ALTER COLUMN max_combo DROP DEFAULT;
