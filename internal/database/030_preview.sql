ALTER TABLE charts ADD COLUMN demo_end double precision;
-- Preserve archived unsupported difficulties while backfilling an unrelated field.
ALTER TABLE charts DROP CONSTRAINT charts_difficulties_check;
UPDATE charts SET demo_end=demo_start+15;
SET CONSTRAINTS ALL IMMEDIATE;
ALTER TABLE charts ADD CONSTRAINT charts_difficulties_check CHECK(valid_chart_difficulties(difficulties,is_single)) NOT VALID;
SET CONSTRAINTS ALL DEFERRED;
-- A trigger supplies a row-dependent default for older writers too.
CREATE FUNCTION default_demo_end() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.demo_end IS NULL THEN NEW.demo_end := NEW.demo_start+15; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER charts_default_demo_end BEFORE INSERT ON charts FOR EACH ROW EXECUTE FUNCTION default_demo_end();
ALTER TABLE charts ALTER COLUMN demo_end SET NOT NULL;
ALTER TABLE chart_resources DROP CONSTRAINT chart_resources_kind_check;
ALTER TABLE chart_resources ADD CONSTRAINT chart_resources_kind_check CHECK(kind IN ('tja','audio','cover','archive','preview'));
ALTER TABLE chart_resources ADD CONSTRAINT chart_resources_preview_check CHECK(kind<>'preview' OR media_type='audio/ogg');
