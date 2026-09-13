-- Tie scores to an actual eligible difficulty, including its course and version.
ALTER TABLE difficulties ADD CONSTRAINT difficulties_score_target
 UNIQUE (version_id, block_index, course, cloud_score_eligible);

CREATE TABLE scores (
 id text PRIMARY KEY,
 user_id text NOT NULL REFERENCES users(id),
 song_id text NOT NULL REFERENCES charts(id),
 version_id text NOT NULL,
 block_index integer NOT NULL,
 difficulty text NOT NULL,
 cloud_score_eligible boolean NOT NULL DEFAULT true CHECK (cloud_score_eligible),
 good integer NOT NULL CHECK (good >= 0),
 ok integer NOT NULL CHECK (ok >= 0),
 bad integer NOT NULL CHECK (bad >= 0),
 score bigint NOT NULL CHECK (score BETWEEN 0 AND 9007199254740991),
 drumroll integer NOT NULL CHECK (drumroll >= 0),
 submitted_at timestamptz NOT NULL DEFAULT now(),
 idempotency_key text,
 payload_digest text NOT NULL CHECK (length(payload_digest) = 64),
 FOREIGN KEY (song_id, version_id) REFERENCES chart_versions(chart_id, id),
 FOREIGN KEY (version_id, block_index, difficulty, cloud_score_eligible)
  REFERENCES difficulties(version_id, block_index, course, cloud_score_eligible),
 UNIQUE (user_id, idempotency_key)
);
CREATE INDEX scores_user_recent ON scores(user_id, submitted_at DESC, id DESC);
CREATE INDEX scores_chart_difficulty ON scores(song_id, difficulty, version_id, submitted_at DESC);
