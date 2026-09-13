ALTER TABLE difficulties DROP CONSTRAINT difficulties_course_check;
ALTER TABLE difficulties ADD CONSTRAINT difficulties_course_check CHECK(course IN ('Easy','Normal','Hard','Oni','Edit','Tower','Dan'));
ALTER TABLE difficulties ADD COLUMN player text NOT NULL DEFAULT '' CHECK(player IN ('','P1','P2'));
