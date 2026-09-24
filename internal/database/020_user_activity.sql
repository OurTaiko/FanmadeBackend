-- Historical login times were not recorded. Leave them unknown rather than
-- presenting the migration time or an upload date as the user's first login.
ALTER TABLE users
 ADD COLUMN first_login_at timestamptz,
 ADD COLUMN last_active_at timestamptz,
 ADD CONSTRAINT users_activity_order CHECK (
  first_login_at IS NULL OR (last_active_at IS NOT NULL AND last_active_at >= first_login_at)
 );
CREATE INDEX users_recent_activity ON users(last_active_at DESC NULLS LAST, id);
CREATE INDEX users_first_login ON users(first_login_at DESC NULLS LAST, id);
