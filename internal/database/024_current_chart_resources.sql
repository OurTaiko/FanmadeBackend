-- Preserve every current score. Refuse ambiguous historical scores rather than
-- silently relabeling them as results for different chart content.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM scores s JOIN charts c ON c.id=s.song_id WHERE s.version_id<>c.current_version_id) THEN
  RAISE EXCEPTION 'historical score content must be resolved before removing chart history';
 END IF;
END $$;

CREATE TABLE chart_data (
 chart_id text PRIMARY KEY REFERENCES charts(id) ON DELETE CASCADE,
 title text NOT NULL,
 subtitle text NOT NULL DEFAULT '',
 bpm double precision NOT NULL CHECK(bpm>0 AND bpm<'Infinity'::float8),
 offset_seconds double precision NOT NULL DEFAULT 0,
 demo_start double precision NOT NULL DEFAULT 0,
 duration double precision NOT NULL CHECK(duration>0 AND duration<=1200),
 encoding text NOT NULL CHECK(encoding IN ('utf-8','shift-jis')),
 wave_filename text NOT NULL,
 tja_file_id text NOT NULL REFERENCES files(id),
 audio_file_id text NOT NULL REFERENCES files(id),
 validation_version text NOT NULL,
 title_translations jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(title_translations)='object'),
 subtitle_translations jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(subtitle_translations)='object')
);
INSERT INTO chart_data SELECT c.id,v.title,v.subtitle,v.bpm,v.offset_seconds,v.demo_start,v.duration,v.encoding,v.wave_filename,v.tja_file_id,v.audio_file_id,v.validation_version,v.title_translations,v.subtitle_translations
FROM charts c JOIN chart_versions v ON v.id=c.current_version_id;

-- Upload retry receipts compare file content, not a generated chart revision.
ALTER TABLE upload_requests ADD COLUMN tja_sha256 text NOT NULL DEFAULT '';
ALTER TABLE upload_requests ADD COLUMN audio_sha256 text NOT NULL DEFAULT '';
UPDATE upload_requests r SET tja_sha256=tf.sha256,audio_sha256=af.sha256
FROM chart_versions v JOIN files tf ON tf.id=v.tja_file_id JOIN files af ON af.id=v.audio_file_id
WHERE v.id=r.version_id AND v.chart_id=r.chart_id;
ALTER TABLE upload_requests DROP COLUMN version_id;

-- Drop only the legacy relationship constraints; keep all score business data.
DO $$ DECLARE c record; BEGIN
 FOR c IN SELECT conname FROM pg_constraint WHERE conrelid='scores'::regclass AND contype='f' AND confrelid IN ('chart_versions'::regclass,'difficulties'::regclass)
 LOOP EXECUTE format('ALTER TABLE scores DROP CONSTRAINT %I',c.conname); END LOOP;
 FOR c IN SELECT conname FROM pg_constraint WHERE conrelid='difficulties'::regclass AND (contype IN ('p','u') OR (contype='f' AND confrelid='chart_versions'::regclass))
 LOOP EXECUTE format('ALTER TABLE difficulties DROP CONSTRAINT %I',c.conname); END LOOP;
 FOR c IN SELECT conname FROM pg_constraint WHERE conrelid='chart_archives'::regclass AND (contype='p' OR (contype='f' AND confrelid='chart_versions'::regclass))
 LOOP EXECUTE format('ALTER TABLE chart_archives DROP CONSTRAINT %I',c.conname); END LOOP;
END $$;
DELETE FROM difficulties d WHERE NOT EXISTS(SELECT 1 FROM charts c WHERE c.current_version_id=d.version_id);
-- Historical unsupported blocks remain hidden; NOT VALID checks still apply on UPDATE.
ALTER TABLE difficulties DROP CONSTRAINT difficulties_course_check;
UPDATE difficulties d SET version_id=c.id FROM charts c WHERE c.current_version_id=d.version_id;
ALTER TABLE difficulties RENAME COLUMN version_id TO chart_id;
ALTER TABLE difficulties ADD CONSTRAINT difficulties_course_check CHECK(course IN ('Easy','Normal','Hard','Oni','Edit')) NOT VALID;
ALTER TABLE difficulties ADD PRIMARY KEY(chart_id,block_index);
ALTER TABLE difficulties ADD CONSTRAINT difficulties_chart FOREIGN KEY(chart_id) REFERENCES chart_data(chart_id) ON DELETE CASCADE;
ALTER TABLE difficulties ADD CONSTRAINT difficulties_score_target UNIQUE(chart_id,block_index,course,cloud_score_eligible);
ALTER TABLE scores DROP COLUMN version_id;
ALTER TABLE scores ADD CONSTRAINT scores_current_difficulty FOREIGN KEY(song_id,block_index,difficulty,cloud_score_eligible) REFERENCES difficulties(chart_id,block_index,course,cloud_score_eligible);
CREATE INDEX scores_chart_difficulty ON scores(song_id,difficulty,submitted_at DESC);
CREATE INDEX scores_leaderboard_current ON scores(song_id,difficulty,block_index,user_id,score DESC,submitted_at,id);

DELETE FROM chart_archives a WHERE NOT EXISTS(SELECT 1 FROM charts c WHERE c.current_version_id=a.version_id);
UPDATE chart_archives a SET version_id=c.id FROM charts c WHERE c.current_version_id=a.version_id;
ALTER TABLE chart_archives RENAME COLUMN version_id TO chart_id;
ALTER TABLE chart_archives ADD PRIMARY KEY(chart_id);
ALTER TABLE chart_archives ADD CONSTRAINT chart_archives_chart FOREIGN KEY(chart_id) REFERENCES chart_data(chart_id) ON DELETE CASCADE;
ALTER TABLE charts DROP CONSTRAINT charts_current_version;
ALTER TABLE charts DROP COLUMN current_version_id;
DROP TABLE chart_versions;
ALTER TABLE charts ADD CONSTRAINT charts_have_data FOREIGN KEY(id) REFERENCES chart_data(chart_id) DEFERRABLE INITIALLY DEFERRED;

WITH removed AS (DELETE FROM files f WHERE NOT EXISTS(SELECT 1 FROM chart_data d WHERE d.tja_file_id=f.id OR d.audio_file_id=f.id) RETURNING storage_key)
INSERT INTO retired_files(storage_key) SELECT storage_key FROM removed ON CONFLICT DO NOTHING;

-- Retrying an already accepted score after replacement must not recreate it.
CREATE TABLE retired_score_requests (
 user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 idempotency_key text NOT NULL,
 PRIMARY KEY(user_id,idempotency_key)
);
