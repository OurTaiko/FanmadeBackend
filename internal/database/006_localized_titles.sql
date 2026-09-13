ALTER TABLE chart_versions
 ADD COLUMN title_translations jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(title_translations)='object'),
 ADD COLUMN subtitle_translations jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(subtitle_translations)='object');
