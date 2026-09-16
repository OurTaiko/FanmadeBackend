package httpapi

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"ourtaiko.dev/fanmade/api/internal/audio"
	"ourtaiko.dev/fanmade/api/internal/tja"
)

type Chart struct {
	CategoryIDs []string  `json:"categoryIds"`
	ID          string    `json:"id"`
	OwnerID     string    `json:"ownerId"`
	Uploader    string    `json:"uploader"`
	VersionID   string    `json:"versionId"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	Duration    float64   `json:"duration"`
	Encoding    string    `json:"encoding"`
	TJAName     string    `json:"tjaName"`
	AudioName   string    `json:"audioName"`
	TJAHash     string    `json:"tjaHash"`
	AudioHash   string    `json:"audioHash"`
	AudioSize   int64     `json:"audioSize"`
	TJAKey      string    `json:"-"`
	AudioKey    string    `json:"-"`
	tja.Metadata
}

// Mixed files are unsupported as a whole; never expose their regular blocks alone.
const supportedCoursesSQL = "('Easy','Normal','Hard','Oni','Edit')"
const publishedChart = `c.status='published' AND NOT EXISTS (SELECT 1 FROM difficulties excluded
 WHERE excluded.version_id=c.current_version_id AND excluded.course NOT IN ` + supportedCoursesSQL + `)`

const chartSelect = `SELECT c.id,c.owner_id,u.nickname,v.id,c.description,c.created_at,v.duration,v.encoding,tf.original_filename,af.original_filename,tf.sha256,af.sha256,af.byte_size,tf.storage_key,af.storage_key,COALESCE(c.title_override,v.title),COALESCE(c.subtitle_override,v.subtitle),v.bpm,v.offset_seconds,v.demo_start,v.wave_filename,v.title_translations || c.title_translation_overrides,v.subtitle_translations || c.subtitle_translation_overrides,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('maker',d.maker,'course',d.course,'level',d.level,'blockIndex',d.block_index,'player',d.player,'style',d.style,'cloudScoreEligible',d.cloud_score_eligible) ORDER BY d.block_index) FROM difficulties d WHERE d.version_id=v.id),'[]'::jsonb),
 ARRAY(SELECT cc.category_id FROM chart_categories cc WHERE cc.chart_id=c.id ORDER BY cc.category_id)
 FROM charts c JOIN users u ON u.id=c.owner_id JOIN chart_versions v ON v.id=c.current_version_id JOIN files tf ON tf.id=v.tja_file_id JOIN files af ON af.id=v.audio_file_id `

func readChart(row pgx.Row) (Chart, error) {
	var c Chart
	var difficulties []byte
	e := row.Scan(&c.ID, &c.OwnerID, &c.Uploader, &c.VersionID, &c.Description, &c.CreatedAt, &c.Duration, &c.Encoding, &c.TJAName, &c.AudioName, &c.TJAHash, &c.AudioHash, &c.AudioSize, &c.TJAKey, &c.AudioKey, &c.Title, &c.Subtitle, &c.BPM, &c.Offset, &c.DemoStart, &c.Wave, &c.TitleTranslations, &c.SubtitleTranslations, &difficulties, &c.CategoryIDs)
	if e == nil {
		e = json.Unmarshal(difficulties, &c.Difficulties)
		c.Maker = difficultyMakers(c.Difficulties)
	}
	return c, e
}
func (s *Server) chart(ctx context.Context, id string) (Chart, error) {
	return readChart(s.DB.QueryRow(ctx, chartSelect+` WHERE c.id=$1 AND `+publishedChart, id))
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) { s.listFor(w, r, "") }
func (s *Server) mine(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, false)
	if !ok {
		return
	}
	s.listFor(w, r, u.User.ID)
}
func (s *Server) listFor(w http.ResponseWriter, r *http.Request, owner string) {
	q := r.URL.Query().Get("q")
	if len(q) > 200 {
		problem(w, 400, "QUERY_INVALID", "搜索内容过长")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 10000 {
		problem(w, 400, "QUERY_INVALID", "页码超出范围")
		return
	}
	course := r.URL.Query().Get("course")
	if course != "" && course != "Easy" && course != "Normal" && course != "Hard" && course != "Oni" && course != "Edit" {
		problem(w, 400, "DIFFICULTY_INVALID", "仅支持 Easy / Normal / Hard / Oni / Edit 难度")
		return
	}
	where := ` WHERE ` + publishedChart + ` AND ($1='' OR COALESCE(c.title_override,v.title) ILIKE '%'||$1||'%' OR COALESCE(c.subtitle_override,v.subtitle) ILIKE '%'||$1||'%' OR EXISTS(SELECT 1 FROM difficulties dm WHERE dm.version_id=v.id AND dm.maker ILIKE '%'||$1||'%') OR u.nickname ILIKE '%'||$1||'%'
	 OR EXISTS(SELECT 1 FROM jsonb_each_text(v.title_translations || c.title_translation_overrides) t WHERE t.value ILIKE '%'||$1||'%')
	 OR EXISTS(SELECT 1 FROM jsonb_each_text(v.subtitle_translations || c.subtitle_translation_overrides) t WHERE t.value ILIKE '%'||$1||'%')) AND ($2='' OR c.owner_id=$2) AND ($3='' OR EXISTS(SELECT 1 FROM difficulties d WHERE d.version_id=v.id AND d.course=$3))`
	var total int
	if e := s.DB.QueryRow(r.Context(), `SELECT count(*) FROM charts c JOIN chart_versions v ON v.id=c.current_version_id JOIN users u ON u.id=c.owner_id`+where, q, owner, course).Scan(&total); e != nil {
		internal(w, e)
		return
	}
	rows, e := s.DB.Query(r.Context(), chartSelect+where+` ORDER BY c.created_at DESC,c.id DESC LIMIT 12 OFFSET $4`, q, owner, course, (page-1)*12)
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
	respond(w, 200, map[string]any{"items": items, "total": total, "page": page, "pageSize": 12})
}
func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	c, e := s.chart(r.Context(), r.PathValue("id"))
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在或已删除")
		return
	}
	if e != nil {
		internal(w, e)
		return
	}
	respond(w, 200, c)
}
func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
	if !ok {
		return
	}
	c, e := s.chart(r.Context(), r.PathValue("id"))
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在")
		return
	}
	if e != nil {
		internal(w, e)
		return
	}
	if c.OwnerID != u.User.ID {
		problem(w, 403, "FORBIDDEN", "只能删除自己的作品")
		return
	}
	if _, e := s.DB.Exec(r.Context(), `UPDATE charts SET status='deleted' WHERE id=$1 AND owner_id=$2`, c.ID, u.User.ID); e != nil {
		internal(w, e)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	c, e := s.chart(r.Context(), r.PathValue("id"))
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在或已删除")
		return
	}
	if e != nil {
		internal(w, e)
		return
	}
	if c.VersionID != r.PathValue("version") {
		problem(w, 404, "VERSION_NOT_FOUND", "版本不存在")
		return
	}
	kind := r.PathValue("kind")
	if kind == "download" {
		tf, e := os.Open(filepath.Join(s.Config.Storage, c.TJAKey))
		if e != nil {
			internal(w, e)
			return
		}
		defer tf.Close()
		af, e := os.Open(filepath.Join(s.Config.Storage, c.AudioKey))
		if e != nil {
			internal(w, e)
			return
		}
		defer af.Close()
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": strings.TrimSuffix(c.TJAName, filepath.Ext(c.TJAName)) + ".zip"}))
		z := zip.NewWriter(w)
		for _, f := range []struct {
			name string
			file *os.File
		}{{c.TJAName, tf}, {c.Wave, af}} {
			dst, e := z.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Store})
			if e != nil {
				return
			}
			if _, e = io.Copy(dst, f.file); e != nil {
				return
			}
		}
		z.Close()
		return
	}
	var key, name, contentType, etag string
	switch kind {
	case "tja":
		key, name, contentType, etag = c.TJAKey, c.TJAName, "application/octet-stream", c.TJAHash
	case "audio":
		key, name, contentType, etag = c.AudioKey, c.Wave, audio.MediaType(c.AudioName), c.AudioHash
	default:
		problem(w, 404, "RESOURCE_NOT_FOUND", "资源不存在")
		return
	}
	f, e := os.Open(filepath.Join(s.Config.Storage, key))
	if e != nil {
		internal(w, e)
		return
	}
	defer f.Close()
	stat, e := f.Stat()
	if e != nil {
		internal(w, e)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", `"`+etag+`"`)
	disposition := "attachment"
	if kind == "audio" {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	http.ServeContent(w, r, name, stat.ModTime(), f)
}
