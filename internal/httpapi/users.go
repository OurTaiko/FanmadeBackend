package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// PublicUser is intentionally separate from authenticated User: never add login
// names, email, roles or credentials to the public directory response.
type PublicUser struct {
	ID           string     `json:"id"`
	Nickname     *string    `json:"nickname"`
	AvatarURL    string     `json:"avatarUrl"`
	FirstLoginAt *time.Time `json:"firstLoginAt"`
	LastActiveAt *time.Time `json:"lastActiveAt"`
	ChartCount   int64      `json:"chartCount"`
	ScoreCount   int64      `json:"scoreCount"`
}

type UserDirectory struct {
	Items             []PublicUser `json:"items"`
	Total             int          `json:"total"`
	Page              int          `json:"page"`
	PageSize          int          `json:"pageSize"`
	ProfilesAvailable bool         `json:"profilesAvailable"`
}

const publicUserColumns = `u.id,u.first_login_at,u.last_active_at,
 (SELECT count(*) FROM charts c WHERE c.owner_id=u.id AND ` + publishedChart + `),
 (SELECT count(*) FROM scores sc JOIN charts c ON c.id=sc.song_id
 WHERE sc.user_id=u.id AND ` + publishedChart + `)`

type UserSpace struct {
	User              PublicUser `json:"user"`
	ProfilesAvailable bool       `json:"profilesAvailable"`
}

func (s *Server) userSpace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validSubject(id) {
		problem(w, 404, "USER_NOT_FOUND", "用户不存在")
		return
	}
	result := UserSpace{ProfilesAvailable: true}
	u := &result.User
	err := s.DB.QueryRow(r.Context(), `SELECT `+publicUserColumns+` FROM users u WHERE u.id=$1`, id).
		Scan(&u.ID, &u.FirstLoginAt, &u.LastActiveAt, &u.ChartCount, &u.ScoreCount)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "USER_NOT_FOUND", "用户不存在")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	names, err := s.Config.SSO.Profiles(ctx, []string{id})
	if err != nil {
		result.ProfilesAvailable = false
	}
	if p, ok := names[id]; ok {
		u.Nickname, u.AvatarURL = &p.Nickname, p.AvatarURL
	}
	respond(w, http.StatusOK, result)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	q := strings.TrimSpace(query.Get("q"))
	page := 1
	if raw := query.Get("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 10000 {
			problem(w, 400, "QUERY_INVALID", "页码无效")
			return
		}
		page = n
	}
	if len(q) > 200 {
		problem(w, 400, "QUERY_INVALID", "搜索内容过长")
		return
	}
	order := "u.last_active_at DESC NULLS LAST,u.id"
	switch query.Get("sort") {
	case "", "active":
	case "newest":
		order = "u.first_login_at DESC NULLS LAST,u.id"
	default:
		problem(w, 400, "QUERY_INVALID", "排序方式无效")
		return
	}
	matching := []string{}
	if q != "" {
		var err error
		matching, err = s.Config.SSO.search(r.Context(), q)
		if err != nil {
			internal(w, err)
			return
		}
	}
	// Count, pagination and statistics all describe the same database snapshot.
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	result := UserDirectory{Items: []PublicUser{}, Page: page, PageSize: 12, ProfilesAvailable: true}
	const filter = ` WHERE ($1='' OR u.id=ANY($2::text[]))`
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM users u`+filter, q, matching).Scan(&result.Total); err != nil {
		internal(w, err)
		return
	}
	rows, err := tx.Query(r.Context(), `WITH selected AS (
 SELECT u.id,u.first_login_at,u.last_active_at FROM users u`+filter+`
 ORDER BY `+order+` LIMIT 12 OFFSET $3)
 SELECT `+publicUserColumns+` FROM selected u ORDER BY `+order, q, matching, (page-1)*12)
	if err != nil {
		internal(w, err)
		return
	}
	ids := []string{}
	for rows.Next() {
		var user PublicUser
		if err = rows.Scan(&user.ID, &user.FirstLoginAt, &user.LastActiveAt, &user.ChartCount, &user.ScoreCount); err != nil {
			rows.Close()
			internal(w, err)
			return
		}
		result.Items = append(result.Items, user)
		ids = append(ids, user.ID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		internal(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		internal(w, err)
		return
	}
	// Release the DB transaction before contacting SSO. An outage still allows
	// browsing local public statistics, but never masquerades as an empty search.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	names, err := s.Config.SSO.Profiles(ctx, ids)
	if err != nil {
		result.ProfilesAvailable = false
	}
	for i := range result.Items {
		if p, ok := names[result.Items[i].ID]; ok {
			result.Items[i].Nickname, result.Items[i].AvatarURL = &p.Nickname, p.AvatarURL
		}
	}
	respond(w, http.StatusOK, result)
}
