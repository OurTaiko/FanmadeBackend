DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM difficulties GROUP BY chart_id HAVING count(DISTINCT style)>1) THEN
  RAISE EXCEPTION 'migration 028: mixed single/double charts must be split first';
 END IF;
 IF EXISTS(SELECT 1 FROM difficulties WHERE style='Double' AND player NOT IN ('P1','P2')) THEN
  RAISE EXCEPTION 'migration 028: double charts require explicit P1/P2';
 END IF;
 IF EXISTS(SELECT 1 FROM difficulties GROUP BY chart_id,course,player HAVING count(*)>1) THEN
  RAISE EXCEPTION 'migration 028: duplicate courses must be resolved first';
 END IF;
END $$;
ALTER TABLE charts ADD COLUMN is_single boolean NOT NULL DEFAULT true,
 ADD COLUMN difficulties jsonb NOT NULL DEFAULT '[]';
UPDATE charts c SET
 is_single=NOT EXISTS(SELECT 1 FROM difficulties d WHERE d.chart_id=c.id AND d.style='Double'),
 difficulties=COALESCE((SELECT jsonb_agg(jsonb_build_object('course',d.course||CASE d.player WHEN 'P1' THEN '_1p' WHEN 'P2' THEN '_2p' ELSE '' END,'level',d.level,'maker',d.maker) ORDER BY d.block_index) FROM difficulties d WHERE d.chart_id=c.id),'[]');
SET CONSTRAINTS ALL IMMEDIATE;
ALTER TABLE scores DROP CONSTRAINT scores_current_difficulty;
ALTER TABLE scores DROP COLUMN block_index, DROP COLUMN cloud_score_eligible;
ALTER TABLE scores DROP CONSTRAINT scores_difficulty_check;
ALTER TABLE scores ADD CONSTRAINT scores_difficulty_check CHECK(difficulty IN ('Easy','Normal','Hard','Oni','Edit','Easy_1p','Easy_2p','Normal_1p','Normal_2p','Hard_1p','Hard_2p','Oni_1p','Oni_2p','Edit_1p','Edit_2p')) NOT VALID;
CREATE INDEX scores_leaderboard_current ON scores(song_id,difficulty,user_id,score DESC,submitted_at,id);
DROP TABLE difficulties;
CREATE FUNCTION valid_chart_difficulties(data jsonb,single boolean) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE d jsonb; keys text[]:=ARRAY[]::text[]; course text; stars numeric;
BEGIN
 IF jsonb_typeof(data)<>'array' THEN RETURN false; END IF;
 IF jsonb_array_length(data)>(CASE WHEN single THEN 5 ELSE 10 END) THEN RETURN false; END IF;
 FOR d IN SELECT value FROM jsonb_array_elements(data) LOOP
  IF jsonb_typeof(d)<>'object' THEN RETURN false; END IF;
  IF NOT (d ?& ARRAY['course','level','maker']) OR (d-'course'-'level'-'maker')<>'{}'::jsonb THEN RETURN false; END IF;
  IF jsonb_typeof(d->'course')<>'string' OR jsonb_typeof(d->'maker')<>'string' OR jsonb_typeof(d->'level')<>'number' THEN RETURN false; END IF;
  course:=d->>'course'; stars:=(d->>'level')::numeric;
  IF course=ANY(keys) OR stars<1 OR stars>10 OR stars<>trunc(stars) OR octet_length(d->>'maker')>500 THEN RETURN false; END IF;
  IF (single AND course !~ '^(Easy|Normal|Hard|Oni|Edit)$') OR
     (NOT single AND course !~ '^(Easy|Normal|Hard|Oni|Edit)_[12]p$') THEN RETURN false; END IF;
  keys:=array_append(keys,course);
 END LOOP;
 RETURN true;
END;
$$;
-- Keep archived unsupported metadata, enforce the new shape on all new writes.
ALTER TABLE charts ADD CONSTRAINT charts_difficulties_check CHECK(valid_chart_difficulties(difficulties,is_single)) NOT VALID;
CREATE INDEX charts_difficulties_gin ON charts USING gin(difficulties jsonb_path_ops);
-- Lock the song while checking a score, serializing with file replacement.
CREATE FUNCTION require_score_course() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE data jsonb;
BEGIN
 SELECT difficulties INTO data FROM charts WHERE id=NEW.song_id FOR SHARE;
 IF data IS NULL OR NOT data @> jsonb_build_array(jsonb_build_object('course',NEW.difficulty)) THEN
  RAISE EXCEPTION 'score difficulty does not exist in song %',NEW.song_id USING ERRCODE='23503';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER scores_require_course BEFORE INSERT OR UPDATE OF song_id,difficulty ON scores FOR EACH ROW EXECUTE FUNCTION require_score_course();
CREATE FUNCTION keep_score_courses() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM scores s JOIN charts c ON c.id=s.song_id WHERE c.id=NEW.id AND NOT c.difficulties @> jsonb_build_array(jsonb_build_object('course',s.difficulty))) THEN
  RAISE EXCEPTION 'cannot remove a difficulty referenced by scores' USING ERRCODE='23503';
 END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER charts_keep_score_courses AFTER UPDATE ON charts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION keep_score_courses();
SET CONSTRAINTS ALL DEFERRED;
