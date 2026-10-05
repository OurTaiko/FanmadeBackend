package httpapi

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"ourtaiko.dev/fanmade/api/internal/tja"
)

type Chart struct {
	AudioPreview *AudioPreview `json:"audioPreview,omitempty"`
	CoverHash    string        `json:"coverHash,omitempty"`
	CategoryIDs  []string      `json:"categoryIds"`
	ID           string        `json:"id"`
	OwnerID      string        `json:"ownerId"`
	Uploader     string        `json:"uploader"`
	VersionID    string        `json:"versionId"`
	Description  string        `json:"description"`
	CreatedAt    time.Time     `json:"createdAt"`
	Duration     float64       `json:"duration"`
	Encoding     string        `json:"encoding"`
	TJAName      string        `json:"tjaName"`
	AudioName    string        `json:"audioName"`
	TJAHash      string        `json:"tjaHash"`
	AudioHash    string        `json:"audioHash"`
	AudioSize    int64         `json:"audioSize"`
	TJAKey       string        `json:"-"`
	AudioKey     string        `json:"-"`
	tja.Metadata
}

// Mixed files are unsupported as a whole; never expose their regular blocks alone.
const supportedCoursesSQL = "('Easy','Normal','Hard','Oni','Edit')"
const publishedChart = `c.status='published' AND NOT EXISTS (SELECT 1 FROM difficulties excluded
 WHERE excluded.version_id=c.current_version_id AND excluded.course NOT IN ` + supportedCoursesSQL + `)`

const chartSelect = `SELECT c.id,c.owner_id,''::text,v.id,c.description,c.created_at,v.duration,v.encoding,tf.original_filename,af.original_filename,tf.sha256,af.sha256,af.byte_size,tf.storage_key,af.storage_key,COALESCE(c.title_override,v.title),COALESCE(c.subtitle_override,v.subtitle),v.bpm,v.offset_seconds,v.demo_start,v.wave_filename,v.title_translations || c.title_translation_overrides,v.subtitle_translations || c.subtitle_translation_overrides,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('maker',d.maker,'course',d.course,'level',d.level,'blockIndex',d.block_index,'player',d.player,'style',d.style,'cloudScoreEligible',d.cloud_score_eligible) ORDER BY d.block_index) FROM difficulties d WHERE d.version_id=v.id),'[]'::jsonb),
 ARRAY(SELECT cc.category_id FROM chart_categories cc WHERE cc.chart_id=c.id ORDER BY cc.category_id)
 FROM charts c JOIN chart_versions v ON v.id=c.current_version_id JOIN files tf ON tf.id=v.tja_file_id JOIN files af ON af.id=v.audio_file_id `

func readChart(row pgx.Row) (Chart, error) {
	var c Chart
	var difficulties []byte
	e := row.Scan(&c.ID, &c.OwnerID, &c.Uploader, &c.VersionID, &c.Description, &c.CreatedAt, &c.Duration, &c.Encoding, &c.TJAName, &c.AudioName, &c.TJAHash, &c.AudioHash, &c.AudioSize, &c.TJAKey, &c.AudioKey, &c.Title, &c.Subtitle, &c.BPM, &c.Offset, &c.DemoStart, &c.Wave, &c.TitleTranslations, &c.SubtitleTranslations, &difficulties, &c.CategoryIDs)
	if e == nil {
		e = json.Unmarshal(difficulties, &c.Difficulties)
		c.Maker = difficultyMakers(c.Difficulties)
		c.AudioPreview = audioPreview(c)
	}
	return c, e
}
func (s *Server) chart(ctx context.Context, id string) (Chart, error) {
	c, e := readChart(s.DB.QueryRow(ctx, chartSelect+` WHERE c.id=$1 AND `+publishedChart, id))
	if e == nil {
		c.Uploader = s.publicNames(ctx, []string{c.OwnerID})[c.OwnerID]
	}
	if e == nil {
		items := []Chart{c}
		e = s.coverHashes(ctx, items)
		c = items[0]
	}
	return c, e
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	owner := r.URL.Query().Get("owner")
	if owner != "" && !validSubject(owner) {
		problem(w, 400, "QUERY_INVALID", "用户标识无效")
		return
	}
	s.listFor(w, r, owner)
}
func (s *Server) mine(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, false)
	if !ok {
		return
	}
	s.listFor(w, r, u.User.ID)
}
func (s *Server) listFor(w http.ResponseWriter, r *http.Request, owner string) {
	s.searchCharts(w, r, owner, false)
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
	if _, e := s.DB.Exec(r.Context(), `WITH removed AS (UPDATE charts SET status='deleted' WHERE id=$1 AND owner_id=$2 RETURNING id) DELETE FROM chart_covers WHERE chart_id IN (SELECT id FROM removed)`, c.ID, u.User.ID); e != nil {
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
		if s.remoteStorage() {
			var key, digest string
			if e = s.DB.QueryRow(r.Context(), `SELECT storage_key,sha256 FROM chart_archives WHERE version_id=$1`, c.VersionID).Scan(&key, &digest); e != nil {
				internal(w, e)
				return
			}
			f, e := s.Config.Objects.Open(r.Context(), key)
			if e != nil {
				internal(w, e)
				return
			}
			defer f.Close()
			name := strings.TrimSuffix(c.TJAName, filepath.Ext(c.TJAName)) + ".zip"
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("ETag", `"`+digest+`"`)
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
			http.ServeContent(w, r, name, f.ModTime(), f)
			return
		}

		tf, e := s.Config.Objects.Open(r.Context(), c.TJAKey)
		if e != nil {
			internal(w, e)
			return
		}
		defer tf.Close()
		af, e := s.Config.Objects.Open(r.Context(), c.AudioKey)
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
			file io.Reader
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
	default:
		problem(w, 404, "RESOURCE_NOT_FOUND", "资源不存在")
		return
	}
	f, e := s.Config.Objects.Open(r.Context(), key)
	if e != nil {
		internal(w, e)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", `"`+etag+`"`)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, f.ModTime(), f)
}
