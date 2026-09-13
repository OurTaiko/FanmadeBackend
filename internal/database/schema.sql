-- Migration 001: local demonstration schema. Email verification is deferred.
CREATE TABLE users (
 id text PRIMARY KEY,
 username text NOT NULL UNIQUE CHECK (username ~ '^[a-zA-Z0-9_]{3,24}$'),
 password_hash text NOT NULL,
 email text,
 email_verified_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK (email_verified_at IS NULL OR email IS NOT NULL)
);
CREATE UNIQUE INDEX users_email_unique ON users (lower(email)) WHERE email IS NOT NULL;
CREATE TABLE sessions (
 token_hash text PRIMARY KEY,
 user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 csrf_token text NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_expiry ON sessions(expires_at);
CREATE TABLE files (
 id text PRIMARY KEY,
 storage_key text NOT NULL UNIQUE,
 original_filename text NOT NULL,
 sha256 text NOT NULL CHECK(length(sha256) = 64),
 byte_size bigint NOT NULL CHECK(byte_size > 0),
 media_type text NOT NULL
);
CREATE TABLE charts (
 id text PRIMARY KEY,
 owner_id text NOT NULL REFERENCES users(id),
 description text NOT NULL DEFAULT '',
 status text NOT NULL DEFAULT 'published' CHECK(status IN ('published','deleted','hidden')),
 current_version_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE chart_versions (
 id text PRIMARY KEY,
 chart_id text NOT NULL REFERENCES charts(id),
 version_number integer NOT NULL CHECK(version_number > 0),
 title text NOT NULL,
 subtitle text NOT NULL DEFAULT '',
 maker text NOT NULL DEFAULT '',
 bpm double precision NOT NULL CHECK(bpm > 0 AND bpm < 'Infinity'::float8),
 offset_seconds double precision NOT NULL DEFAULT 0,
 demo_start double precision NOT NULL DEFAULT 0,
 duration double precision NOT NULL CHECK(duration > 0 AND duration <= 1200),
 encoding text NOT NULL CHECK(encoding IN ('utf-8','shift-jis')),
 wave_filename text NOT NULL,
 tja_file_id text NOT NULL REFERENCES files(id),
 audio_file_id text NOT NULL REFERENCES files(id),
 validation_version text NOT NULL,
 UNIQUE(chart_id, version_number),
 UNIQUE(chart_id, id)
);
ALTER TABLE charts ADD CONSTRAINT charts_current_version
 FOREIGN KEY(id, current_version_id) REFERENCES chart_versions(chart_id, id) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE difficulties (
 version_id text NOT NULL REFERENCES chart_versions(id) ON DELETE CASCADE,
 block_index integer NOT NULL,
 course text NOT NULL CHECK(course IN ('Easy','Normal','Hard','Oni','Edit')),
 level integer NOT NULL CHECK(level BETWEEN 1 AND 10),
 PRIMARY KEY(version_id, block_index)
);
CREATE INDEX charts_published_recent ON charts(created_at DESC, id DESC) WHERE status = 'published';
CREATE INDEX charts_owner ON charts(owner_id, created_at DESC);
CREATE TABLE upload_requests (
 user_id text NOT NULL REFERENCES users(id),
 idempotency_key text NOT NULL,
 payload_digest text NOT NULL,
 chart_id text NOT NULL REFERENCES charts(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id, idempotency_key)
);
