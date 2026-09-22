-- Covers belong to songs, not chart versions or game resources.
-- Replacements delete the previous row and insert a new one in the same transaction.
CREATE TABLE chart_covers (
 id text PRIMARY KEY,
 chart_id text NOT NULL UNIQUE REFERENCES charts(id) ON DELETE CASCADE,
 webp bytea NOT NULL CHECK (octet_length(webp) BETWEEN 12 AND 8388608),
 sha256 text NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
 created_at timestamptz NOT NULL DEFAULT now()
);
