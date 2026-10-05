package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Pointers distinguish required zero-valued counts from missing/null fields.
type scoreSubmission struct {
	SongID     string          `json:"songId"`
	Difficulty string          `json:"difficulty"`
	Good       *int64          `json:"good"`
	OK         *int64          `json:"ok"`
	Bad        *int64          `json:"bad"`
	Score      *int64          `json:"score"`
	Drumroll   *int64          `json:"drumroll"`
	MaxCombo   *int64          `json:"max_combo"`
	ReplayData json.RawMessage `json:"replay_data,omitempty"`
	// Omit zero from the digest to preserve retries of pre-ClearStatus requests.
	ClearStatus int `json:"ClearStatus,omitempty"`
}

type Score struct {
	ID          string    `json:"id"`
	UserID      string    `json:"userId"`
	SongID      string    `json:"songId"`
	BlockIndex  int       `json:"blockIndex"`
	Difficulty  string    `json:"difficulty"`
	Good        int64     `json:"good"`
	OK          int64     `json:"ok"`
	Bad         int64     `json:"bad"`
	Score       int64     `json:"score"`
	Drumroll    int64     `json:"drumroll"`
	MaxCombo    int64     `json:"max_combo"`
	ClearStatus int       `json:"ClearStatus"`
	SubmittedAt time.Time `json:"submittedAt"`
}

var songIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (v *scoreSubmission) valid() bool {
	if !songIDPattern.MatchString(v.SongID) {
		return false
	}
	courses := map[string]string{"easy": "Easy", "normal": "Normal", "hard": "Hard", "oni": "Oni", "edit": "Edit", "ura": "Edit"}
	c, ok := courses[strings.ToLower(strings.TrimSpace(v.Difficulty))]
	if !ok {
		return false
	}
	v.Difficulty = c
	for _, count := range []*int64{v.Good, v.OK, v.Bad, v.Drumroll, v.MaxCombo} {
		if count == nil || *count < 0 || *count > 2147483647 {
			return false
		}
	}
	return v.Score != nil && *v.Score >= 0 && *v.Score <= 9007199254740991 && v.ClearStatus >= 0 && v.ClearStatus <= 3
}

const scoreColumns = `id,user_id,song_id,block_index,difficulty,good,ok,bad,score,drumroll,max_combo,clear_status,submitted_at`

func readScore(row pgx.Row) (Score, error) {
	var v Score
	err := row.Scan(&v.ID, &v.UserID, &v.SongID, &v.BlockIndex, &v.Difficulty, &v.Good, &v.OK, &v.Bad, &v.Score, &v.Drumroll, &v.MaxCombo, &v.ClearStatus, &v.SubmittedAt)
	return v, err
}

func (s *Server) submitScore(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		problem(w, 415, "CONTENT_TYPE_INVALID", "请使用 JSON 请求")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxScoreRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input scoreSubmission
	if err = decoder.Decode(&input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			problem(w, 413, "REQUEST_TOO_LARGE", "成绩请求超过大小限制")
			return
		}
		problem(w, 400, "REQUEST_INVALID", "成绩需要完整的 JSON 对象，计数与分数必须为整数")
		return
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		problem(w, 400, "REQUEST_INVALID", "请求只能包含一个 JSON 对象")
		return
	}
	if !input.valid() {
		problem(w, 422, "SCORE_INVALID", "需要有效的歌曲 ID、难度和全部六项非负整数；计数（含最大连击）上限 2147483647，分数上限 9007199254740991；ClearStatus 必须为 0–3")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key != "" && !keyPattern.MatchString(key) {
		problem(w, 400, "IDEMPOTENCY_KEY_INVALID", "成绩请求标识需要 16–80 位字母、数字或短横线")
		return
	}
	input.ReplayData = normalizeScoreReplay(input.ReplayData)
	payload, _ := json.Marshal(input)
	digest := hash(string(payload))
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize submissions against resource replacement on the same song.
	var song string
	err = tx.QueryRow(r.Context(), `SELECT id FROM charts c WHERE c.id=$1 FOR SHARE`, input.SongID).Scan(&song)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "歌曲不存在或已下架")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if key != "" {
		if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "score:"+u.User.ID+":"+key); err != nil {
			internal(w, err)
			return
		}
		var retired bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM retired_score_requests WHERE user_id=$1 AND idempotency_key=$2)`, u.User.ID, key).Scan(&retired); err != nil {
			internal(w, err)
			return
		}
		if retired {
			problem(w, 409, "SCORE_REMOVED", "歌曲文件已替换，此成绩已清除")
			return
		}
		var oldDigest string
		err = tx.QueryRow(r.Context(), `SELECT payload_digest FROM scores WHERE user_id=$1 AND idempotency_key=$2`, u.User.ID, key).Scan(&oldDigest)
		if err == nil {
			if oldDigest != digest {
				var same bool
				err = tx.QueryRow(r.Context(), `SELECT jsonb_strip_nulls(jsonb_build_object('songId',song_id,'difficulty',difficulty,'good',good,'ok',ok,'bad',bad,'score',score,'drumroll',drumroll,'max_combo',max_combo,'replay_data',replay_data,'ClearStatus',NULLIF(clear_status,0)))=$3::jsonb FROM scores WHERE user_id=$1 AND idempotency_key=$2`, u.User.ID, key, string(payload)).Scan(&same)
				if err != nil {
					internal(w, err)
					return
				}
				if !same {
					problem(w, 409, "IDEMPOTENCY_CONFLICT", "此请求标识已用于不同成绩")
					return
				}
			}
			result, err := readScore(tx.QueryRow(r.Context(), `SELECT `+scoreColumns+` FROM scores WHERE user_id=$1 AND idempotency_key=$2`, u.User.ID, key))
			if err != nil {
				internal(w, err)
				return
			}
			respond(w, 200, result)
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			internal(w, err)
			return
		}
	}
	var published bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM charts c WHERE c.id=$1 AND `+publishedChart+`)`, input.SongID).Scan(&published); err != nil {
		internal(w, err)
		return
	}
	if !published {
		problem(w, 404, "CHART_NOT_FOUND", "歌曲不存在或已下架")
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT block_index,cloud_score_eligible FROM difficulties WHERE chart_id=$1 AND course=$2 ORDER BY block_index FOR SHARE`, input.SongID, input.Difficulty)
	if err != nil {
		internal(w, err)
		return
	}
	blocks, eligible, block := 0, 0, 0
	for rows.Next() {
		var index int
		var allowed bool
		if err = rows.Scan(&index, &allowed); err != nil {
			rows.Close()
			internal(w, err)
			return
		}
		blocks++
		if allowed {
			eligible++
			block = index
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		internal(w, err)
		return
	}
	if blocks == 0 {
		problem(w, 404, "DIFFICULTY_NOT_FOUND", "歌曲不存在此难度")
		return
	}
	if eligible == 0 {
		problem(w, 422, "DOUBLE_SCORE_UNSUPPORTED", "DOUBLE 谱面不支持云端成绩记录")
		return
	}
	if eligible > 1 {
		problem(w, 409, "DIFFICULTY_AMBIGUOUS", "此难度包含多个单人谱面块，无法唯一确定成绩归属")
		return
	}
	result, err := readScore(tx.QueryRow(r.Context(), `INSERT INTO scores
	 (id,user_id,song_id,block_index,difficulty,good,ok,bad,score,drumroll,max_combo,idempotency_key,payload_digest,replay_data,clear_status)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''),$13,$14,$15) RETURNING `+scoreColumns,
		ID(), u.User.ID, input.SongID, block, input.Difficulty, *input.Good, *input.OK, *input.Bad, *input.Score, *input.Drumroll, *input.MaxCombo, key, digest, input.ReplayData, input.ClearStatus))
	if err != nil {
		internal(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		internal(w, err)
		return
	}
	respond(w, 201, result)
}
