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
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatarUrl"`
	Rank      int64  `json:"rank"`
}

type leaderboardResponse struct {
	SongID     string             `json:"songId"`
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
	if course != "" {
		var ok bool
		course, ok = normalizeCourse(course)
		if !ok {
			problem(w, 400, "DIFFICULTY_INVALID", "难度无效")
			return
		}
	}
	// One snapshot keeps the chart, count, ranks and page consistent.
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	result := leaderboardResponse{SongID: r.PathValue("id"), Page: page, PageSize: 20, Items: []leaderboardEntry{}}
	err = tx.QueryRow(r.Context(), `SELECT id FROM charts c WHERE c.id=$1 AND `+publishedChart, result.SongID).Scan(&result.SongID)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "歌曲不存在或已下架")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if course == "" {
		err = tx.QueryRow(r.Context(), `SELECT d->>'course' AS course FROM charts c CROSS JOIN LATERAL jsonb_array_elements(c.difficulties) d WHERE c.id=$1
		 ORDER BY CASE split_part(d->>'course','_',1) WHEN 'Oni' THEN 0 WHEN 'Edit' THEN 1 WHEN 'Hard' THEN 2 WHEN 'Normal' THEN 3 WHEN 'Easy' THEN 4 ELSE 5 END,course LIMIT 1`, result.SongID).Scan(&course)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			internal(w, err)
			return
		}
	}
	result.Difficulty = course
	var found bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM charts WHERE id=$1 AND difficulties @> jsonb_build_array(jsonb_build_object('course',$2::text)))`, result.SongID, course).Scan(&found)
	if err != nil {
		internal(w, err)
		return
	}
	if !found {
		problem(w, 404, "DIFFICULTY_NOT_FOUND", "歌曲不存在此难度")
		return
	}
	result.Supported = true
	if result.Supported {
		err = tx.QueryRow(r.Context(), `SELECT count(DISTINCT user_id) FROM scores WHERE song_id=$1 AND difficulty=$2`, result.SongID, course).Scan(&result.Total)
		if err != nil {
			internal(w, err)
			return
		}
		rows, err := tx.Query(r.Context(), `WITH best AS (
		 SELECT DISTINCT ON (user_id) * FROM scores
		 WHERE song_id=$1 AND difficulty=$2
		 ORDER BY user_id,score DESC,submitted_at,id
		), ranked AS (
		 SELECT best.*,rank() OVER (ORDER BY score DESC) AS place FROM best
		)
		SELECT r.id,r.user_id,r.song_id,r.difficulty,r.good,r.ok,r.bad,r.score,r.drumroll,r.max_combo,r.clear_status,r.submitted_at,''::text,r.place
		FROM ranked r
		ORDER BY r.score DESC,r.submitted_at,r.id LIMIT $3 OFFSET $4`, result.SongID, course, result.PageSize, (page-1)*result.PageSize)
		if err != nil {
			internal(w, err)
			return
		}
		for rows.Next() {
			var v leaderboardEntry
			if err = rows.Scan(&v.ID, &v.UserID, &v.SongID, &v.Difficulty, &v.Good, &v.OK, &v.Bad, &v.Score.Score, &v.Drumroll, &v.MaxCombo, &v.ClearStatus, &v.SubmittedAt, &v.Nickname, &v.Rank); err != nil {
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
	profiles := s.publicProfiles(r.Context(), ids)
	for i := range result.Items {
		p := profiles[result.Items[i].UserID]
		result.Items[i].Nickname, result.Items[i].AvatarURL = p.Nickname, p.AvatarURL
	}
	respond(w, 200, result)
}
