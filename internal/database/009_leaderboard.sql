CREATE INDEX scores_leaderboard_best ON scores
 (song_id, version_id, difficulty, block_index, user_id, score DESC, submitted_at, id);
