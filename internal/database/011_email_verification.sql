CREATE TABLE registration_codes (
 email text PRIMARY KEY CHECK (email = lower(email)),
 verification_id text NOT NULL UNIQUE,
 code_hash text NOT NULL,
 expires_at timestamptz NOT NULL,
 sent_at timestamptz NOT NULL,
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
 window_started_at timestamptz NOT NULL,
 send_count integer NOT NULL CHECK (send_count BETWEEN 1 AND 5)
);
CREATE TABLE email_send_limits (
 ip_hash text PRIMARY KEY,
 window_started_at timestamptz NOT NULL,
 send_count integer NOT NULL CHECK (send_count > 0)
);
CREATE INDEX registration_codes_cleanup ON registration_codes(window_started_at);
CREATE INDEX email_send_limits_cleanup ON email_send_limits(window_started_at);
