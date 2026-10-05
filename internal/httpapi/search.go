package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// gameSearch returns one ordered snapshot without web-only enrichment.
func (s *Server) gameSearch(w http.ResponseWriter, r *http.Request) {
	s.searchCharts(w, r, "", true)
}

// Both search APIs share validation, matching and completion ordering. Only
// the web API counts and paginates, then adds covers and public names.
func (s *Server) searchCharts(w http.ResponseWriter, r *http.Request, owner string, all bool) {
	q := r.URL.Query().Get("q")
	if len(q) > 200 {
		problem(w, 400, "QUERY_INVALID", "搜索内容过长")
		return
	}
	page := 1
	if !all {
		page, _ = strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		if page > 10000 {
			problem(w, 400, "QUERY_INVALID", "页码超出范围")
			return
		}
	}
	course := r.URL.Query().Get("course")
	canonical, validCourse := normalizeCourse(course)
	if course != "" && (!validCourse || canonical != course) {
		problem(w, 400, "DIFFICULTY_INVALID", "仅支持五种规范难度及其 _1p / _2p 双人难度")
		return
	}
	level := -1
	if raw := r.URL.Query().Get("level"); raw != "" {
		var err error
		level, err = strconv.Atoi(raw)
		if err != nil || level < 1 || level > 10 {
			problem(w, 400, "QUERY_INVALID", "星数必须在 1 到 10 之间")
			return
		}
	}
	order := r.URL.Query().Get("order")
	if order != "" && order != "default" && order != "unfc" && order != "unperfect" {
		problem(w, 400, "QUERY_INVALID", "顺序必须为 default / unfc / unperfect")
		return
	}
	userID := ""
	if order == "unfc" || order == "unperfect" {
		u, err := s.current(r)
		if err == nil {
			userID = u.User.ID
		} else if !errors.Is(err, pgx.ErrNoRows) {
			internal(w, err)
			return
		}
	}
	where := ` WHERE ` + publishedChart + ` AND ($1='' OR c.title ILIKE '%'||$1||'%' OR c.subtitle ILIKE '%'||$1||'%' OR EXISTS(SELECT 1 FROM jsonb_array_elements(c.difficulties) dm WHERE dm->>'maker' ILIKE '%'||$1||'%')
	 OR EXISTS(SELECT 1 FROM jsonb_each_text(c.title_translations) t WHERE t.value ILIKE '%'||$1||'%')
	 OR EXISTS(SELECT 1 FROM jsonb_each_text(c.subtitle_translations) t WHERE t.value ILIKE '%'||$1||'%')) AND ($2='' OR c.owner_id=$2) AND EXISTS(SELECT 1 FROM jsonb_to_recordset(c.difficulties) AS d(course text,level integer,maker text) WHERE ($3='' OR d.course=$3) AND ($4::int=-1 OR d.level=$4))`
	var total int
	if !all {
		if e := s.DB.QueryRow(r.Context(), `SELECT count(*) FROM charts c JOIN users u ON u.id=c.owner_id`+where, q, owner, course, level).Scan(&total); e != nil {
			internal(w, e)
			return
		}
	}
	// Prioritize a chart when any matching difficulty has no qualifying score
	// for this user and song. Guests keep the default stable order.
	ordering := ` ORDER BY ($5::text<>'' AND EXISTS(
	 SELECT 1 FROM jsonb_to_recordset(c.difficulties) AS d(course text,level integer,maker text) WHERE ($3='' OR d.course=$3) AND ($4::int=-1 OR d.level=$4)
	 AND NOT EXISTS(SELECT 1 FROM scores sc WHERE sc.user_id=$5 AND sc.song_id=c.id
	 AND sc.difficulty=d.course AND sc.bad=0 AND (sc.good>0 OR sc.ok>0)
	 AND ($6::text='unfc' OR sc.ok=0)))) DESC, c.created_at DESC,c.id DESC`
	args := []any{q, owner, course, level, userID, order}
	if !all {
		ordering += ` LIMIT 12 OFFSET $7`
		args = append(args, (page-1)*12)
	}
	rows, e := s.DB.Query(r.Context(), chartSelect+where+ordering, args...)
	if e != nil {
		internal(w, e)
		return
	}
	defer rows.Close()
	items := []Chart{}
	for rows.Next() {
		c, e := readChart(rows)
		if e != nil {
			internal(w, e)
			return
		}
		items = append(items, c)
	}
	if e := rows.Err(); e != nil {
		internal(w, e)
		return
	}
	if all {
		respond(w, 200, map[string]any{"items": items, "total": len(items)})
		return
	}
	if e := s.coverHashes(r.Context(), items); e != nil {
		internal(w, e)
		return
	}
	s.chartNames(r.Context(), items)
	respond(w, 200, map[string]any{"items": items, "total": total, "page": page, "pageSize": 12})
}
