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
	SongID     string `json:"songId"`
	VersionID  string `json:"versionId,omitempty"`
	Difficulty string `json:"difficulty"`
	Good       *int64 `json:"good"`
	OK         *int64 `json:"ok"`
	Bad        *int64 `json:"bad"`
	Score      *int64 `json:"score"`
	Drumroll   *int64 `json:"drumroll"`
	MaxCombo   *int64 `json:"max_combo"`
}

type Score struct {
	ID          string    `json:"id"`
	UserID      string    `json:"userId"`
	SongID      string    `json:"songId"`
	VersionID   string    `json:"versionId"`
	BlockIndex  int       `json:"blockIndex"`
	Difficulty  string    `json:"difficulty"`
	Good        int64     `json:"good"`
	OK          int64     `json:"ok"`
	Bad         int64     `json:"bad"`
	Score       int64     `json:"score"`
	Drumroll    int64     `json:"drumroll"`
	MaxCombo    int64     `json:"max_combo"`
	SubmittedAt time.Time `json:"submittedAt"`
}

var songIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (v *scoreSubmission) valid() bool {
	if !songIDPattern.MatchString(v.SongID) || (v.VersionID != "" && !songIDPattern.MatchString(v.VersionID)) {
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
	return v.Score != nil && *v.Score >= 0 && *v.Score <= 9007199254740991
}

const scoreColumns = `id,user_id,song_id,version_id,block_index,difficulty,good,ok,bad,score,drumroll,max_combo,submitted_at`

func readScore(row pgx.Row) (Score, error) {
	var v Score
	err := row.Scan(&v.ID, &v.UserID, &v.SongID, &v.VersionID, &v.BlockIndex, &v.Difficulty, &v.Good, &v.OK, &v.Bad, &v.Score, &v.Drumroll, &v.MaxCombo, &v.SubmittedAt)
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
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input scoreSubmission
	if err = decoder.Decode(&input); err != nil {
		problem(w, 400, "REQUEST_INVALID", "成绩需要完整的 JSON 对象，计数与分数必须为整数")
		return
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		problem(w, 400, "REQUEST_INVALID", "请求只能包含一个 JSON 对象")
		return
	}
	if !input.valid() || (isGameRequest(r) && input.VersionID == "") {
		problem(w, 422, "SCORE_INVALID", "需要有效的歌曲 ID、难度和全部六项非负整数；计数（含最大连击）上限 2147483647，分数上限 9007199254740991")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key != "" && !keyPattern.MatchString(key) {
		problem(w, 400, "IDEMPOTENCY_KEY_INVALID", "成绩请求标识需要 16–80 位字母、数字或短横线")
		return
	}
	payload, _ := json.Marshal(input)
	digest := hash(string(payload))
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if key != "" {
		if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "score:"+u.User.ID+":"+key); err != nil {
			internal(w, err)
			return
		}
		var oldDigest string
		err = tx.QueryRow(r.Context(), `SELECT payload_digest FROM scores WHERE user_id=$1 AND idempotency_key=$2`, u.User.ID, key).Scan(&oldDigest)
		if err == nil {
			if oldDigest != digest {
				problem(w, 409, "IDEMPOTENCY_CONFLICT", "此请求标识已用于不同成绩")
				return
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
	// Hold the published version stable until the score has been committed.
	var version string
	err = tx.QueryRow(r.Context(), `SELECT current_version_id FROM charts c WHERE c.id=$1 AND `+publishedChart+` FOR SHARE`, input.SongID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "歌曲不存在或已下架")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if input.VersionID != "" && input.VersionID != version {
		problem(w, 409, "CHART_VERSION_CHANGED", "谱面版本已更新，请刷新曲库后游玩；本次成绩不会归到新版本")
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT block_index,cloud_score_eligible FROM difficulties WHERE version_id=$1 AND course=$2 ORDER BY block_index FOR SHARE`, version, input.Difficulty)
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
	 (id,user_id,song_id,version_id,block_index,difficulty,good,ok,bad,score,drumroll,max_combo,idempotency_key,payload_digest)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULLIF($13,''),$14) RETURNING `+scoreColumns,
		ID(), u.User.ID, input.SongID, version, block, input.Difficulty, *input.Good, *input.OK, *input.Bad, *input.Score, *input.Drumroll, *input.MaxCombo, key, digest))
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
