package httpapi

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"ourtaiko.dev/fanmade/api/internal/audio"
	"ourtaiko.dev/fanmade/api/internal/objectstore"
	"path/filepath"
	"strings"
	"time"
)

// Resolve immediately before download. Never persist expiring signed URLs in
// catalogs or forward the API bearer token to the separate object host.
func (s *Server) resourceLinks(w http.ResponseWriter, r *http.Request) {
	remote, ok := s.Config.Objects.(*objectstore.S3)
	if !ok {
		problem(w, 404, "DIRECT_DOWNLOAD_UNAVAILABLE", "此服务器尚未启用直连下载")
		return
	}
	c, e := s.chart(r.Context(), r.PathValue("id"))
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在或已下架")
		return
	}
	if e != nil {
		internal(w, e)
		return
	}
	type resource struct {
		URL         string `json:"url"`
		HeadURL     string `json:"headUrl"`
		SHA256      string `json:"sha256"`
		Size        int64  `json:"size"`
		ContentType string `json:"contentType"`
	}
	resources := map[string]resource{}
	add := func(kind, key, name, media, digest string, size int64) error {
		get, e := remote.Sign(r.Context(), key, "GET", name, media, 15*time.Minute)
		if e != nil {
			return e
		}
		head, e := remote.Sign(r.Context(), key, "HEAD", name, media, 15*time.Minute)
		if e != nil {
			return e
		}
		resources[kind] = resource{get, head, digest, size, media}
		return nil
	}
	var tjaSize int64
	if e = s.DB.QueryRow(r.Context(), `SELECT byte_size FROM chart_resources WHERE chart_id=$1 AND kind='tja'`, c.ID).Scan(&tjaSize); e != nil {
		internal(w, e)
		return
	}
	if e = add("tja", c.TJAKey, c.TJAName, "application/octet-stream", c.TJAHash, tjaSize); e != nil {
		internal(w, e)
		return
	}
	if e = add("audio", c.AudioKey, c.AudioName, audio.MediaType(c.AudioName), c.AudioHash, c.AudioSize); e != nil {
		internal(w, e)
		return
	}
	var key, digest string
	var size int64
	if e = s.DB.QueryRow(r.Context(), `SELECT storage_key,sha256,byte_size FROM chart_resources WHERE kind='archive' AND chart_id=$1`, c.ID).Scan(&key, &digest, &size); e != nil {
		internal(w, e)
		return
	}
	if e = add("download", key, strings.TrimSuffix(c.TJAName, filepath.Ext(c.TJAName))+".zip", "application/zip", digest, size); e != nil {
		internal(w, e)
		return
	}
	var coverKey *string
	var coverHash string
	var coverSize *int64
	e = s.DB.QueryRow(r.Context(), `SELECT storage_key,sha256,byte_size FROM chart_resources WHERE kind='cover' AND chart_id=$1`, c.ID).Scan(&coverKey, &coverHash, &coverSize)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		internal(w, e)
		return
	}
	if e == nil && coverKey != nil && coverSize != nil {
		if e = add("cover", *coverKey, "cover.webp", "image/webp", coverHash, *coverSize); e != nil {
			internal(w, e)
			return
		}
	}
	respond(w, 200, map[string]any{"chartId": c.ID, "expiresAt": time.Now().UTC().Add(15 * time.Minute), "resources": resources})
}
