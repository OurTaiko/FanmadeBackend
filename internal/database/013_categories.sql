-- Categories are independent collections of stable chart IDs. Existing chart,
-- version, file and score rows are never rewritten by this migration.
CREATE TABLE categories (
 id text PRIMARY KEY CHECK (id ~ '^[a-z][a-z0-9-]{0,63}$'),
 title text NOT NULL,
 genre text NOT NULL,
 sort_order integer NOT NULL UNIQUE
);
INSERT INTO categories(id,title,genre,sort_order) VALUES
 ('game','Game','GAME',1),
 ('virtual-singer','Virtual Singer','VOCALOID',2),
 ('pop','Pop','J-POP',3),
 ('classic','Classic','CLASSICAL',4),
 ('variety','Variety','VARIETY',5);
CREATE TABLE chart_categories (
 category_id text NOT NULL REFERENCES categories(id),
 chart_id text NOT NULL REFERENCES charts(id) ON DELETE CASCADE,
 PRIMARY KEY(category_id,chart_id)
);
CREATE INDEX chart_categories_chart ON chart_categories(chart_id);
INSERT INTO chart_categories(category_id,chart_id) SELECT 'variety',id FROM charts;
