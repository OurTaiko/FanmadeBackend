-- Historical scores and writers without recordings retain SQL NULL.
ALTER TABLE scores ADD COLUMN replay_data jsonb;
ALTER TABLE scores ADD CONSTRAINT scores_replay_object
 CHECK (replay_data IS NULL OR jsonb_typeof(replay_data) = 'object');
