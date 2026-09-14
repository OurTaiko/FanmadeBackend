-- Preserve existing files and scores, but withdraw any current version containing
-- an unsupported course (including files mixed with regular difficulties).
UPDATE charts c SET status='hidden'
 WHERE c.status='published' AND EXISTS (
 SELECT 1 FROM difficulties d WHERE d.version_id=c.current_version_id
 AND d.course NOT IN ('Easy','Normal','Hard','Oni','Edit'));

-- NOT VALID retains archived rows while enforcing the policy on new writes.
ALTER TABLE difficulties DROP CONSTRAINT difficulties_course_check;
ALTER TABLE difficulties ADD CONSTRAINT difficulties_course_check
 CHECK(course IN ('Easy','Normal','Hard','Oni','Edit')) NOT VALID;
ALTER TABLE scores ADD CONSTRAINT scores_difficulty_check
 CHECK(difficulty IN ('Easy','Normal','Hard','Oni','Edit')) NOT VALID;
