-- Backfill styles from stored TJA files in the same migration transaction.
ALTER TABLE difficulties ADD COLUMN style text NOT NULL DEFAULT 'Double'
 CHECK (style IN ('Single', 'Double'));
ALTER TABLE difficulties ADD CONSTRAINT difficulties_player_style
 CHECK (player = '' OR style = 'Double');
ALTER TABLE difficulties ADD COLUMN cloud_score_eligible boolean
 GENERATED ALWAYS AS (style = 'Single' AND player = '') STORED;
