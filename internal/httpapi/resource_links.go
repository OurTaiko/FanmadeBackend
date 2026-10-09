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

// Resolve current resources before download. Never forward API credentials to the object host.
func (s *Server) resourceLinks(w http.ResponseWriter, r *http.Request) {
	remote, ok := s.Config.Objects.(*objectstore.S3)
	if !ok {
		problem(w, 404, "DIRECT_DOWNLOAD_UNAVAILABLE", "此服务器尚未启用直连下载")
		return
	}
	// Keep every key/hash/size from one committed upload, even if a replacement
	// commits while the manifest is being assembled.
	tx, e := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		internal(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	c, e := s.readChart(tx.QueryRow(r.Context(), chartSelect+` WHERE c.id=$1 AND `+publishedChart, r.PathValue("id")))
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
		public, e := remote.PublicURL(key)
		if e != nil {
			return e
		}
		if public != "" {
			resources[kind] = resource{public, public, digest, size, media}
			return nil
		}
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
	if e = tx.QueryRow(r.Context(), `SELECT byte_size FROM chart_resources WHERE chart_id=$1 AND kind='tja'`, c.ID).Scan(&tjaSize); e != nil {
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
	if e = tx.QueryRow(r.Context(), `SELECT storage_key,sha256,byte_size FROM chart_resources WHERE kind='archive' AND chart_id=$1`, c.ID).Scan(&key, &digest, &size); e != nil {
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
	e = tx.QueryRow(r.Context(), `SELECT storage_key,sha256,byte_size FROM chart_resources WHERE kind='cover' AND chart_id=$1`, c.ID).Scan(&coverKey, &coverHash, &coverSize)
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
	e = tx.QueryRow(r.Context(), `SELECT storage_key,sha256,byte_size FROM chart_resources WHERE kind='preview' AND chart_id=$1`, c.ID).Scan(&key, &digest, &size)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		internal(w, e)
		return
	}
	if e == nil {
		if e = add("preview", key, "preview.ogg", "audio/ogg", digest, size); e != nil {
			internal(w, e)
			return
		}
	}

	if e = tx.Commit(r.Context()); e != nil {
		internal(w, e)
		return
	}
	// Legacy clients require expiresAt: this is the manifest refresh deadline,
	// not an expiry on public CDN URLs.
	public, _ := remote.PublicURL(c.TJAKey)
	respond(w, 200, map[string]any{"urlsExpire": public == "", "chartId": c.ID, "expiresAt": time.Now().UTC().Add(15 * time.Minute), "resources": resources})
}
