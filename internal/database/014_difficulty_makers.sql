-- Preserve existing credits before removing the version-level source of truth.
ALTER TABLE difficulties ADD COLUMN maker text NOT NULL DEFAULT '';
UPDATE difficulties d SET maker = btrim(v.maker)
FROM chart_versions v WHERE v.id = d.version_id;
ALTER TABLE chart_versions DROP COLUMN maker;
