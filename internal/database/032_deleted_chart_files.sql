-- A deleted song keeps its row as a tombstone, but its scores and files are
-- removed. Only songs that are not deleted must keep TJA and audio resources.
CREATE OR REPLACE FUNCTION require_chart_resources() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE song text;
BEGIN
 IF TG_TABLE_NAME='charts' THEN song:=NEW.id; ELSE song:=OLD.chart_id; END IF;
 IF EXISTS(SELECT 1 FROM charts WHERE id=song AND status<>'deleted') AND
    (SELECT count(*) FROM chart_resources WHERE chart_id=song AND kind IN ('tja','audio'))<>2 THEN
  RAISE EXCEPTION 'song % requires TJA and audio resources',song USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
-- Earlier soft deletions kept scores and files. Remove them now; the resource
-- trigger queues each file, and the server deletes those no song still uses.
DELETE FROM scores WHERE song_id IN (SELECT id FROM charts WHERE status='deleted');
DELETE FROM chart_resources WHERE chart_id IN (SELECT id FROM charts WHERE status='deleted');
