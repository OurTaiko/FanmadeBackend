package httpapi

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"ourtaiko.dev/fanmade/api/internal/tja"
)

type Chart struct {
	DemoEnd      float64       `json:"demoEnd"`
	PreviewPath  string        `json:"previewPath,omitempty"`
	AudioPreview *AudioPreview `json:"audioPreview,omitempty"`
	CoverHash    string        `json:"coverHash,omitempty"`
	CategoryIDs  []string      `json:"categoryIds"`
	ID           string        `json:"id"`
	OwnerID      string        `json:"ownerId"`
	Uploader     string        `json:"uploader"`
	// UploaderAvatarURL is empty when the uploader has no avatar or SSO is unavailable.
	UploaderAvatarURL string    `json:"uploaderAvatarUrl"`
	Description       string    `json:"description"`
	CreatedAt         time.Time `json:"createdAt"`
	Duration          float64   `json:"duration"`
	Encoding          string    `json:"encoding"`
	TJAName           string    `json:"tjaName"`
	AudioName         string    `json:"audioName"`
	TJAHash           string    `json:"tjaHash"`
	AudioHash         string    `json:"audioHash"`
	AudioSize         int64     `json:"audioSize"`
	TJAKey            string    `json:"-"`
	AudioKey          string    `json:"-"`
	tja.Metadata
}

// Mixed files are unsupported as a whole; never expose their regular blocks alone.
const supportedCoursesSQL = "('Easy','Normal','Hard','Oni','Edit','Easy_1p','Easy_2p','Normal_1p','Normal_2p','Hard_1p','Hard_2p','Oni_1p','Oni_2p','Edit_1p','Edit_2p')"
const publishedChart = `c.status='published' AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(c.difficulties) excluded
 WHERE excluded->>'course' NOT IN ` + supportedCoursesSQL + `)`

const chartSelect = `SELECT c.id,c.owner_id,''::text,c.description,c.created_at,c.duration,c.encoding,tf.original_filename,af.original_filename,tf.sha256,af.sha256,af.byte_size,tf.storage_key,af.storage_key,c.title,c.subtitle,c.bpm,c.offset_seconds,c.demo_start,c.wave_filename,c.title_translations,c.subtitle_translations,c.is_single,
 c.difficulties,
 c.category_flags,c.demo_end,COALESCE((SELECT storage_key FROM chart_resources WHERE chart_id=c.id AND kind='preview'),'')
 FROM charts c JOIN chart_resources tf ON tf.chart_id=c.id AND tf.kind='tja' JOIN chart_resources af ON af.chart_id=c.id AND af.kind='audio' `

func readChart(row pgx.Row) (Chart, error) {
	var c Chart
	var difficulties []byte
	var flags CategoryFlags
	e := row.Scan(&c.ID, &c.OwnerID, &c.Uploader, &c.Description, &c.CreatedAt, &c.Duration, &c.Encoding, &c.TJAName, &c.AudioName, &c.TJAHash, &c.AudioHash, &c.AudioSize, &c.TJAKey, &c.AudioKey, &c.Title, &c.Subtitle, &c.BPM, &c.Offset, &c.DemoStart, &c.Wave, &c.TitleTranslations, &c.SubtitleTranslations, &c.IsSingle, &difficulties, &flags, &c.DemoEnd, &c.PreviewPath)
	if e == nil {
		c.CategoryIDs = flags.IDs()
		e = json.Unmarshal(difficulties, &c.Difficulties)
		c.Maker = difficultyMakers(c.Difficulties)
		c.AudioPreview = audioPreview(c)
	}
	return c, e
}
func (s *Server) chart(ctx context.Context, id string) (Chart, error) {
	c, e := s.readChart(s.DB.QueryRow(ctx, chartSelect+` WHERE c.id=$1 AND `+publishedChart, id))
	if e == nil {
		c.setUploader(s.publicProfiles(ctx, []string{c.OwnerID})[c.OwnerID])
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
	keys, e := s.deleteChart(r.Context(), c.ID, u.User.ID)
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在")
		return
	}
	if e != nil {
		internal(w, e)
		return
	}
	// Files are queued by the deletion; the periodic cleanup retries any failure.
	for _, key := range keys {
		if e = s.deleteRetiredFile(r.Context(), key); e != nil {
			log.Printf("deleted chart file cleanup pending: %v", e)
		}
	}
	respond(w, 200, map[string]bool{"ok": true})
}

// deleteChart removes a song's scores, resources and row. The resource trigger
// queues each file in retired_files.
func (s *Server) deleteChart(ctx context.Context, chartID, ownerID string) ([]string, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	// Serialize with score submissions, which hold the song row FOR SHARE.
	if e = tx.QueryRow(ctx, `SELECT id FROM charts WHERE id=$1 AND owner_id=$2 AND status<>'deleted' FOR UPDATE`, chartID, ownerID).Scan(&chartID); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM scores WHERE song_id=$1`, chartID); e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, `DELETE FROM chart_resources WHERE chart_id=$1 RETURNING storage_key`, chartID)
	if e != nil {
		return nil, e
	}
	keys, e := pgx.CollectRows(rows, pgx.RowTo[string])
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM charts WHERE id=$1`, chartID); e != nil {
		return nil, e
	}
	return keys, tx.Commit(ctx)
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
	kind := r.PathValue("kind")
	if kind == "download" {
		if s.remoteStorage() {
			var key, digest string
			if e = s.DB.QueryRow(r.Context(), `SELECT storage_key,sha256 FROM chart_resources WHERE chart_id=$1 AND kind='archive'`, c.ID).Scan(&key, &digest); e != nil {
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
