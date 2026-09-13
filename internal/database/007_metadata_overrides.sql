-- Site display edits never rewrite the immutable uploaded files or versions.
ALTER TABLE charts
 ADD COLUMN title_override text CHECK(title_override IS NULL OR (length(btrim(title_override))>0 AND octet_length(title_override)<=500)),
 ADD COLUMN subtitle_override text CHECK(subtitle_override IS NULL OR octet_length(subtitle_override)<=500),
 ADD COLUMN title_translation_overrides jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(title_translation_overrides)='object'),
 ADD COLUMN subtitle_translation_overrides jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(subtitle_translation_overrides)='object'),
 ADD COLUMN metadata_updated_at timestamptz;
