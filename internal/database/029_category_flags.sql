-- Stable bits match httpapi.CategoryFlags. Reject unknown IDs before removing
-- the old catalog so a customized database cannot silently lose memberships.
LOCK TABLE charts, categories, chart_categories IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM categories WHERE id NOT IN ('game','virtual-singer','pop','classic','variety','anime')) THEN
  RAISE EXCEPTION 'migration 029: unknown category IDs must be mapped before upgrading';
 END IF;
END $$;

ALTER TABLE charts ADD COLUMN category_flags integer NOT NULL DEFAULT 16
 CONSTRAINT charts_category_flags_check CHECK (category_flags >= 0 AND (category_flags & 63) = category_flags);
-- 028 intentionally retains archived unsupported difficulties. PostgreSQL also
-- checks NOT VALID constraints on unrelated UPDATEs; restore this unchanged
-- constraint after the backfill within the same locked transaction.
ALTER TABLE charts DROP CONSTRAINT charts_difficulties_check;
-- Preserve even empty and unpublished memberships exactly. New uploads still
-- default to Variety; zero represents a legacy chart with no memberships.
UPDATE charts c SET category_flags=COALESCE((
 SELECT bit_or(CASE cc.category_id
  WHEN 'game' THEN 1 WHEN 'virtual-singer' THEN 2 WHEN 'pop' THEN 4
  WHEN 'classic' THEN 8 WHEN 'variety' THEN 16 WHEN 'anime' THEN 32
 END) FROM chart_categories cc WHERE cc.chart_id=c.id
),0);

-- Flush deferred chart checks before dropping referenced relations.
SET CONSTRAINTS ALL IMMEDIATE;
ALTER TABLE charts ADD CONSTRAINT charts_difficulties_check
 CHECK(valid_chart_difficulties(difficulties,is_single)) NOT VALID;
DROP TABLE chart_categories;
DROP TABLE categories;
SET CONSTRAINTS ALL DEFERRED;
