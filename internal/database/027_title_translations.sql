-- One authoritative translation dictionary, including English. Keep raw TJA
-- title/subtitle as language-neutral source text; never choose a display locale.
UPDATE charts SET
 title_translations=jsonb_build_object('en',title)||title_translations||title_translation_overrides||CASE WHEN title_override IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('en',title_override) END,
 subtitle_translations=jsonb_build_object('en',subtitle)||subtitle_translations||subtitle_translation_overrides||CASE WHEN subtitle_override IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('en',subtitle_override) END;
-- Flush deferred resource checks before PostgreSQL changes the table layout.
SET CONSTRAINTS ALL IMMEDIATE;
ALTER TABLE charts DROP COLUMN title_override, DROP COLUMN subtitle_override,
 DROP COLUMN title_translation_overrides, DROP COLUMN subtitle_translation_overrides;
SET CONSTRAINTS ALL DEFERRED;
