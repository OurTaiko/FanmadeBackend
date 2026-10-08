-- Reddit-style interactions: one up/down vote per user on songs and comments,
-- threaded comments and a notification center. Triggers keep the denormalized counters exact, also
-- when a song deletion cascades through its comments and votes.
CREATE TABLE chart_stats (
 chart_id text PRIMARY KEY REFERENCES charts(id) ON DELETE CASCADE,
 upvotes integer NOT NULL DEFAULT 0 CHECK(upvotes>=0),
 downvotes integer NOT NULL DEFAULT 0 CHECK(downvotes>=0),
 comment_count integer NOT NULL DEFAULT 0 CHECK(comment_count>=0)
);
CREATE INDEX chart_stats_score ON chart_stats((upvotes-downvotes) DESC);
CREATE TABLE chart_votes (
 chart_id text NOT NULL REFERENCES charts(id) ON DELETE CASCADE,
 user_id text NOT NULL REFERENCES users(id),
 value smallint NOT NULL CHECK(value IN (-1,1)),
 voted_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(chart_id,user_id)
);
CREATE INDEX chart_votes_user ON chart_votes(user_id);
-- A comment whose author deleted it, or an administrator removed it, stays as
-- an empty placeholder only while replies still hang below it.
CREATE TABLE comments (
 id text PRIMARY KEY,
 chart_id text NOT NULL REFERENCES charts(id) ON DELETE CASCADE,
 parent_id text REFERENCES comments(id) ON DELETE CASCADE,
 root_id text NOT NULL,
 depth integer NOT NULL CHECK(depth BETWEEN 0 AND 1000),
 author_id text NOT NULL REFERENCES users(id),
 body text NOT NULL CHECK(char_length(body)<=10000),
 created_at timestamptz NOT NULL DEFAULT now(),
 edited_at timestamptz,
 deleted_at timestamptz,
 removed_at timestamptz,
 upvotes integer NOT NULL DEFAULT 0 CHECK(upvotes>=0),
 downvotes integer NOT NULL DEFAULT 0 CHECK(downvotes>=0),
 reply_count integer NOT NULL DEFAULT 0 CHECK(reply_count>=0),
 CHECK((parent_id IS NULL)=(depth=0) AND (parent_id IS NOT NULL OR root_id=id)),
 CHECK((deleted_at IS NULL AND removed_at IS NULL)=(body<>''))
);
CREATE INDEX comments_chart_roots ON comments(chart_id,created_at DESC) WHERE parent_id IS NULL;
CREATE INDEX comments_root ON comments(root_id);
CREATE INDEX comments_parent ON comments(parent_id);
CREATE INDEX comments_author ON comments(author_id,created_at DESC);
CREATE TABLE comment_votes (
 comment_id text NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
 user_id text NOT NULL REFERENCES users(id),
 value smallint NOT NULL CHECK(value IN (-1,1)),
 voted_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(comment_id,user_id)
);
CREATE INDEX comment_votes_user ON comment_votes(user_id);

CREATE FUNCTION count_chart_vote() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE song text; up integer:=0; down integer:=0;
BEGIN
 IF TG_OP IN ('UPDATE','DELETE') THEN
  song:=OLD.chart_id;
  IF OLD.value=1 THEN up:=up-1; ELSE down:=down-1; END IF;
 END IF;
 IF TG_OP IN ('INSERT','UPDATE') THEN
  song:=NEW.chart_id;
  IF NEW.value=1 THEN up:=up+1; ELSE down:=down+1; END IF;
 END IF;
 -- A cascading song deletion has already removed the song row.
 IF EXISTS(SELECT 1 FROM charts WHERE id=song) THEN
  -- CHECK applies to the proposed row before ON CONFLICT, so never propose a
  -- negative count; a decrement always finds the existing row.
  INSERT INTO chart_stats(chart_id,upvotes,downvotes) VALUES(song,greatest(up,0),greatest(down,0))
  ON CONFLICT(chart_id) DO UPDATE SET upvotes=chart_stats.upvotes+up,downvotes=chart_stats.downvotes+down;
 END IF;
 RETURN NULL;
END;
$$;
CREATE TRIGGER chart_votes_count AFTER INSERT OR UPDATE OF value OR DELETE ON chart_votes FOR EACH ROW EXECUTE FUNCTION count_chart_vote();

CREATE FUNCTION count_comment_vote() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE target text; up integer:=0; down integer:=0;
BEGIN
 IF TG_OP IN ('UPDATE','DELETE') THEN
  target:=OLD.comment_id;
  IF OLD.value=1 THEN up:=up-1; ELSE down:=down-1; END IF;
 END IF;
 IF TG_OP IN ('INSERT','UPDATE') THEN
  target:=NEW.comment_id;
  IF NEW.value=1 THEN up:=up+1; ELSE down:=down+1; END IF;
 END IF;
 UPDATE comments SET upvotes=upvotes+up,downvotes=downvotes+down WHERE id=target;
 RETURN NULL;
END;
$$;
CREATE TRIGGER comment_votes_count AFTER INSERT OR UPDATE OF value OR DELETE ON comment_votes FOR EACH ROW EXECUTE FUNCTION count_comment_vote();

CREATE FUNCTION count_comment() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE delta integer; rec comments;
BEGIN
 IF TG_OP='INSERT' THEN delta:=1; rec:=NEW; ELSE delta:=-1; rec:=OLD; END IF;
 IF rec.parent_id IS NOT NULL THEN
  UPDATE comments SET reply_count=reply_count+delta WHERE id=rec.parent_id;
 END IF;
 IF EXISTS(SELECT 1 FROM charts WHERE id=rec.chart_id) THEN
  INSERT INTO chart_stats(chart_id,comment_count) VALUES(rec.chart_id,greatest(delta,0))
  ON CONFLICT(chart_id) DO UPDATE SET comment_count=chart_stats.comment_count+delta;
 END IF;
 RETURN NULL;
END;
$$;
CREATE TRIGGER comments_count AFTER INSERT OR DELETE ON comments FOR EACH ROW EXECUTE FUNCTION count_comment();

-- Comment ranking as on Reddit. "best" is the lower bound of the Wilson score
-- interval at 80% confidence, so a few early votes cannot outrank many.
CREATE FUNCTION comment_confidence(up integer,down integer) RETURNS double precision
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT CASE WHEN up+down=0 THEN 0 ELSE
  (up::float8/(up+down) + 1.642374415149/(2*(up+down))
   - 1.281551565545*sqrt((up::float8/(up+down)*(1-up::float8/(up+down)) + 1.642374415149/(4*(up+down)))/(up+down)))
  / (1 + 1.642374415149/(up+down)) END
$$;
-- "controversial": many votes, split close to evenly.
CREATE FUNCTION comment_controversy(up integer,down integer) RETURNS double precision
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT CASE WHEN up<=0 OR down<=0 THEN 0
  ELSE power((up+down)::float8, CASE WHEN up>down THEN down::float8/up ELSE up::float8/down END) END
$$;

-- General notification center. Each row is one notice to one user with its
-- own read state. kind names an event registered by the server; chart and
-- comment links cascade, so a notice disappears with what it points to.
-- dedupe_key makes repeatable events (vote milestones, moderation) notify once.
CREATE TABLE notifications (
 id text PRIMARY KEY,
 recipient_id text NOT NULL REFERENCES users(id),
 kind text NOT NULL CHECK(kind ~ '^[a-z][a-z_]{0,39}$'),
 actor_id text REFERENCES users(id),
 chart_id text REFERENCES charts(id) ON DELETE CASCADE,
 comment_id text REFERENCES comments(id) ON DELETE CASCADE,
 data jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(data)='object'),
 dedupe_key text,
 created_at timestamptz NOT NULL DEFAULT now(),
 read_at timestamptz,
 CHECK(actor_id IS NULL OR actor_id<>recipient_id)
);
CREATE INDEX notifications_recipient ON notifications(recipient_id,created_at DESC,id);
CREATE INDEX notifications_unread ON notifications(recipient_id) WHERE read_at IS NULL;
CREATE UNIQUE INDEX notifications_dedupe ON notifications(recipient_id,dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX notifications_comment ON notifications(comment_id) WHERE comment_id IS NOT NULL;
CREATE INDEX notifications_chart ON notifications(chart_id) WHERE chart_id IS NOT NULL;
-- A row turns one kind of notice off for one user; no row means enabled.
CREATE TABLE notification_mutes (
 user_id text NOT NULL REFERENCES users(id),
 kind text NOT NULL CHECK(kind ~ '^[a-z][a-z_]{0,39}$'),
 PRIMARY KEY(user_id,kind)
);
