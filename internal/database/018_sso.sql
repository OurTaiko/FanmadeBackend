-- Apply only after every existing ID has been copied to SSO and a backup exists.
DROP TRIGGER users_prepare_profile ON users;
DROP FUNCTION prepare_user_profile();
ALTER TABLE users DROP COLUMN username, DROP COLUMN password_hash, DROP COLUMN nickname,
 DROP COLUMN email, DROP COLUMN email_verified_at, DROP COLUMN is_admin, DROP COLUMN created_at;
DELETE FROM sessions;
ALTER TABLE sessions ADD COLUMN upstream_token bytea NOT NULL;
DROP TABLE registration_codes;
DROP TABLE email_send_limits;
CREATE TABLE oidc_flows (
 state_hash text PRIMARY KEY,
 binding_hash text NOT NULL,
 nonce text NOT NULL,
 verifier text NOT NULL,
 return_path text NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE INDEX oidc_flows_expiry ON oidc_flows(expires_at);
