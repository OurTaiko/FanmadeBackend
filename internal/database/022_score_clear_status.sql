-- Historical scores have no reliable clear state; keep them at unknown/none.
ALTER TABLE scores ADD COLUMN clear_status integer NOT NULL DEFAULT 0
 CHECK (clear_status BETWEEN 0 AND 3);
