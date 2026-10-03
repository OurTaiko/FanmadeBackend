package httpapi

import (
	"errors"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"

	"github.com/jackc/pgx/v5"
	"ourtaiko.dev/fanmade/api/internal/audio"
)

// AudioPreview describes the original audio stream, not a transcoded excerpt.
// Times are in seconds; SHA-256 and byte size remain Chart.AudioHash/AudioSize.
type AudioPreview struct {
	URL             string  `json:"url"`
	ContentType     string  `json:"contentType"`
	StartSeconds    float64 `json:"startSeconds"`
	DurationSeconds float64 `json:"durationSeconds"`
}

func audioPreview(c Chart) *AudioPreview {
	if c.Duration <= 0 || math.IsNaN(c.Duration) || math.IsInf(c.Duration, 0) {
		return nil
	}
	start := c.DemoStart
	if start < 0 || start >= c.Duration || math.IsNaN(start) || math.IsInf(start, 0) {
		start = 0
	}
	return &AudioPreview{
		URL:             "/api/v1/charts/" + url.PathEscape(c.ID) + "/versions/" + url.PathEscape(c.VersionID) + "/audio",
		ContentType:     audio.MediaType(c.AudioName),
		StartSeconds:    start,
		DurationSeconds: math.Min(15, c.Duration-start),
	}
}

// Each decoder probe/range needs only the current published audio object. Do
// not call chart(): SSO nickname lookups and cover/difficulty payloads would
// delay every seek and make public playback depend on unrelated services.
func (s *Server) streamAudio(w http.ResponseWriter, r *http.Request) {
	var version, key, name, digest, originalName string
	err := s.DB.QueryRow(r.Context(), `SELECT v.id,af.storage_key,v.wave_filename,af.sha256,af.original_filename
 FROM charts c JOIN chart_versions v ON v.id=c.current_version_id JOIN files af ON af.id=v.audio_file_id
 WHERE c.id=$1 AND `+publishedChart, r.PathValue("id")).Scan(&version, &key, &name, &digest, &originalName)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在或已删除")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if version != r.PathValue("version") {
		problem(w, 404, "VERSION_NOT_FOUND", "版本不存在")
		return
	}
	root, err := os.OpenRoot(s.Config.Storage)
	if err != nil {
		internal(w, err)
		return
	}
	defer root.Close()
	f, err := root.Open(key)
	if err != nil {
		internal(w, err)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		internal(w, err)
		return
	}
	if !stat.Mode().IsRegular() {
		internal(w, errors.New("audio object is not a regular file"))
		return
	}
	w.Header().Set("Content-Type", audio.MediaType(originalName))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	w.Header().Set("ETag", `"`+digest+`"`)
	// Revalidate cached bytes so deletion/replacement cannot leave an old URL
	// playable indefinitely. Proxies must preserve encoded bytes and byte ranges.
	w.Header().Set("Cache-Control", "public, no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	// Serve directly from a seekable file: bounded memory, GET/HEAD, 206/416,
	// conditional requests and If-Range are handled by the standard library.
	http.ServeContent(w, r, name, stat.ModTime(), f)
}
