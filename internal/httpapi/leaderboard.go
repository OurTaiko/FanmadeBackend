package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

type leaderboardEntry struct {
	Score
	Nickname string `json:"nickname"`
	Rank     int64  `json:"rank"`
}

type leaderboardResponse struct {
	SongID     string             `json:"songId"`
	VersionID  string             `json:"versionId"`
	Difficulty string             `json:"difficulty"`
	Supported  bool               `json:"supported"`
	Items      []leaderboardEntry `json:"items"`
	Total      int                `json:"total"`
	Page       int                `json:"page"`
	PageSize   int                `json:"pageSize"`
}

func (s *Server) leaderboard(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	page := 1
	if raw := query.Get("page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 10000 {
			problem(w, 400, "QUERY_INVALID", "页码需要为 1–10000 的整数")
			return
		}
	}
	course := strings.ToLower(strings.TrimSpace(query.Get("difficulty")))
	courses := map[string]string{"easy": "Easy", "normal": "Normal", "hard": "Hard", "oni": "Oni", "edit": "Edit", "ura": "Edit"}
	if course != "" {
		var ok bool
		course, ok = courses[course]
		if !ok {
			problem(w, 400, "DIFFICULTY_INVALID", "难度无效")
			return
		}
	}
	// One snapshot keeps the selected version, count, ranks and page consistent.
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	result := leaderboardResponse{SongID: r.PathValue("id"), Page: page, PageSize: 20, Items: []leaderboardEntry{}}
	err = tx.QueryRow(r.Context(), `SELECT current_version_id FROM charts c WHERE c.id=$1 AND `+publishedChart, result.SongID).Scan(&result.VersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "歌曲不存在或已下架")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if version := query.Get("versionId"); version != "" && version != result.VersionID {
		problem(w, 409, "CHART_VERSION_CHANGED", "谱面版本已更新，请刷新歌曲详情")
		return
	}
	if course == "" {
		err = tx.QueryRow(r.Context(), `SELECT course FROM difficulties WHERE version_id=$1
		 ORDER BY CASE course WHEN 'Oni' THEN 0 WHEN 'Edit' THEN 1 WHEN 'Hard' THEN 2 WHEN 'Normal' THEN 3 WHEN 'Easy' THEN 4 ELSE 5 END,block_index LIMIT 1`, result.VersionID).Scan(&course)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			internal(w, err)
			return
		}
	}
	result.Difficulty = course
	var count, eligible, block int
	err = tx.QueryRow(r.Context(), `SELECT count(*),count(*) FILTER (WHERE cloud_score_eligible),COALESCE(min(block_index) FILTER (WHERE cloud_score_eligible),0)
	 FROM difficulties WHERE version_id=$1 AND course=$2`, result.VersionID, course).Scan(&count, &eligible, &block)
	if err != nil {
		internal(w, err)
		return
	}
	if count == 0 {
		problem(w, 404, "DIFFICULTY_NOT_FOUND", "歌曲不存在此难度")
		return
	}
	if eligible > 1 {
		problem(w, 409, "DIFFICULTY_AMBIGUOUS", "此难度包含多个单人谱面，无法确定排行榜")
		return
	}
	result.Supported = eligible == 1
	if result.Supported {
		err = tx.QueryRow(r.Context(), `SELECT count(DISTINCT user_id) FROM scores WHERE song_id=$1 AND version_id=$2 AND difficulty=$3 AND block_index=$4`, result.SongID, result.VersionID, course, block).Scan(&result.Total)
		if err != nil {
			internal(w, err)
			return
		}
		rows, err := tx.Query(r.Context(), `WITH best AS (
		 SELECT DISTINCT ON (user_id) * FROM scores
		 WHERE song_id=$1 AND version_id=$2 AND difficulty=$3 AND block_index=$4
		 ORDER BY user_id,score DESC,submitted_at,id
		), ranked AS (
		 SELECT best.*,rank() OVER (ORDER BY score DESC) AS place FROM best
		)
		SELECT r.id,r.user_id,r.song_id,r.version_id,r.block_index,r.difficulty,r.good,r.ok,r.bad,r.score,r.drumroll,r.max_combo,r.clear_status,r.submitted_at,''::text,r.place
		FROM ranked r
		ORDER BY r.score DESC,r.submitted_at,r.id LIMIT $5 OFFSET $6`, result.SongID, result.VersionID, course, block, result.PageSize, (page-1)*result.PageSize)
		if err != nil {
			internal(w, err)
			return
		}
		for rows.Next() {
			var v leaderboardEntry
			if err = rows.Scan(&v.ID, &v.UserID, &v.SongID, &v.VersionID, &v.BlockIndex, &v.Difficulty, &v.Good, &v.OK, &v.Bad, &v.Score.Score, &v.Drumroll, &v.MaxCombo, &v.ClearStatus, &v.SubmittedAt, &v.Nickname, &v.Rank); err != nil {
				rows.Close()
				internal(w, err)
				return
			}
			result.Items = append(result.Items, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			internal(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		internal(w, err)
		return
	}
	ids := []string{}
	for _, entry := range result.Items {
		ids = append(ids, entry.UserID)
	}
	names := s.publicNames(r.Context(), ids)
	for i := range result.Items {
		result.Items[i].Nickname = names[result.Items[i].UserID]
	}
	respond(w, 200, result)
}
