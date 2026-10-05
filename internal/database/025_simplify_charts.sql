-- One row per song; resource identity is (chart_id, kind), never a revision.
ALTER TABLE charts
 ADD COLUMN title text, ADD COLUMN subtitle text NOT NULL DEFAULT '',
 ADD COLUMN bpm double precision CHECK(bpm>0 AND bpm<'Infinity'::float8),
 ADD COLUMN offset_seconds double precision NOT NULL DEFAULT 0,
 ADD COLUMN demo_start double precision NOT NULL DEFAULT 0,
 ADD COLUMN duration double precision CHECK(duration>0 AND duration<=1200),
 ADD COLUMN encoding text CHECK(encoding IN ('utf-8','shift-jis')),
 ADD COLUMN wave_filename text,
 ADD COLUMN title_translations jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(title_translations)='object'),
 ADD COLUMN subtitle_translations jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(subtitle_translations)='object');
UPDATE charts c SET title=d.title,subtitle=d.subtitle,bpm=d.bpm,offset_seconds=d.offset_seconds,demo_start=d.demo_start,duration=d.duration,encoding=d.encoding,wave_filename=d.wave_filename,title_translations=d.title_translations,subtitle_translations=d.subtitle_translations FROM chart_data d WHERE d.chart_id=c.id;
ALTER TABLE charts ALTER COLUMN title SET NOT NULL, ALTER COLUMN bpm SET NOT NULL,
 ALTER COLUMN duration SET NOT NULL, ALTER COLUMN encoding SET NOT NULL, ALTER COLUMN wave_filename SET NOT NULL;
CREATE TABLE chart_resources (
 chart_id text NOT NULL REFERENCES charts(id) ON DELETE CASCADE,
 kind text NOT NULL CHECK(kind IN ('tja','audio','cover','archive')),
 storage_key text NOT NULL CHECK(storage_key<>''),
 original_filename text NOT NULL,
 sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),
 byte_size bigint NOT NULL CHECK(byte_size>0),
 media_type text NOT NULL,
 PRIMARY KEY(chart_id,kind),
 CHECK(kind<>'cover' OR (media_type='image/webp' AND byte_size BETWEEN 12 AND 8388608))
);
-- Shared source files are valid; retirement checks remaining references.
CREATE INDEX chart_resources_storage_key ON chart_resources(storage_key);
INSERT INTO chart_resources SELECT d.chart_id,'tja',f.storage_key,f.original_filename,f.sha256,f.byte_size,f.media_type FROM chart_data d JOIN files f ON f.id=d.tja_file_id;
INSERT INTO chart_resources SELECT d.chart_id,'audio',f.storage_key,f.original_filename,f.sha256,f.byte_size,f.media_type FROM chart_data d JOIN files f ON f.id=d.audio_file_id;
INSERT INTO chart_resources SELECT chart_id,'cover',storage_key,'cover.webp',sha256,byte_size,'image/webp' FROM chart_covers;
INSERT INTO chart_resources SELECT a.chart_id,'archive',a.storage_key,regexp_replace(f.original_filename,'\.[^.]*$','')||'.zip',a.sha256,a.byte_size,'application/zip' FROM chart_archives a JOIN chart_data d ON d.chart_id=a.chart_id JOIN files f ON f.id=d.tja_file_id;
ALTER TABLE charts DROP CONSTRAINT charts_have_data;
ALTER TABLE difficulties DROP CONSTRAINT difficulties_chart;
ALTER TABLE difficulties ADD CONSTRAINT difficulties_chart FOREIGN KEY(chart_id) REFERENCES charts(id) ON DELETE CASCADE;
DROP TABLE chart_archives;
DROP TABLE chart_covers;
DROP TABLE chart_data;
INSERT INTO retired_files(storage_key) SELECT f.storage_key FROM files f WHERE NOT EXISTS(SELECT 1 FROM chart_resources r WHERE r.storage_key=f.storage_key) ON CONFLICT DO NOTHING;
DROP TABLE files;
CREATE OR REPLACE FUNCTION retire_resource_object() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
 IF TG_OP='UPDATE' AND OLD.storage_key=NEW.storage_key THEN RETURN NEW; END IF;
 INSERT INTO retired_files(storage_key) VALUES(OLD.storage_key) ON CONFLICT DO NOTHING;
 RETURN OLD;
END;
$$;
CREATE TRIGGER retire_chart_resource AFTER DELETE OR UPDATE OF storage_key ON chart_resources FOR EACH ROW EXECUTE FUNCTION retire_resource_object();
CREATE FUNCTION require_chart_resources() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE song text;
BEGIN
 IF TG_TABLE_NAME='charts' THEN song:=NEW.id; ELSE song:=OLD.chart_id; END IF;
 IF EXISTS(SELECT 1 FROM charts WHERE id=song) AND
    (SELECT count(*) FROM chart_resources WHERE chart_id=song AND kind IN ('tja','audio'))<>2 THEN
  RAISE EXCEPTION 'song % requires TJA and audio resources',song USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER charts_require_resources AFTER INSERT OR UPDATE ON charts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION require_chart_resources();
CREATE CONSTRAINT TRIGGER resources_keep_required AFTER DELETE OR UPDATE ON chart_resources DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION require_chart_resources();
