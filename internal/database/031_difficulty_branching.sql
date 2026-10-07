-- difficulties[].branching: the block contains #BRANCHSTART. New uploads always
-- write it; existing charts gain it from the stored TJA in a background backfill.
CREATE OR REPLACE FUNCTION valid_chart_difficulties(data jsonb,single boolean) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE d jsonb; keys text[]:=ARRAY[]::text[]; course text; stars numeric;
BEGIN
 IF jsonb_typeof(data)<>'array' THEN RETURN false; END IF;
 IF jsonb_array_length(data)>(CASE WHEN single THEN 5 ELSE 10 END) THEN RETURN false; END IF;
 FOR d IN SELECT value FROM jsonb_array_elements(data) LOOP
  IF jsonb_typeof(d)<>'object' THEN RETURN false; END IF;
  IF NOT (d ?& ARRAY['course','level','maker']) OR (d-'course'-'level'-'maker'-'branching')<>'{}'::jsonb THEN RETURN false; END IF;
  IF jsonb_typeof(d->'course')<>'string' OR jsonb_typeof(d->'maker')<>'string' OR jsonb_typeof(d->'level')<>'number' THEN RETURN false; END IF;
  IF d ? 'branching' AND jsonb_typeof(d->'branching')<>'boolean' THEN RETURN false; END IF;
  course:=d->>'course'; stars:=(d->>'level')::numeric;
  IF course=ANY(keys) OR stars<1 OR stars>10 OR stars<>trunc(stars) OR octet_length(d->>'maker')>500 THEN RETURN false; END IF;
  IF (single AND course !~ '^(Easy|Normal|Hard|Oni|Edit)$') OR
     (NOT single AND course !~ '^(Easy|Normal|Hard|Oni|Edit)_[12]p$') THEN RETURN false; END IF;
  keys:=array_append(keys,course);
 END LOOP;
 RETURN true;
END;
$$;
