package httpapi

import (
	"errors"
	"math"
	"mime"
	"net/http"
	"net/url"

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
		URL:             "/api/v1/charts/" + url.PathEscape(c.ID) + "/audio",
		ContentType:     audio.MediaType(c.AudioName),
		StartSeconds:    start,
		DurationSeconds: math.Min(15, c.Duration-start),
	}
}

// Each decoder probe/range needs only the current published audio object. Do
// not call chart(): SSO nickname lookups and cover/difficulty payloads would
// delay every seek and make public playback depend on unrelated services.
func (s *Server) streamAudio(w http.ResponseWriter, r *http.Request) {
	var key, name, digest, originalName string
	err := s.DB.QueryRow(r.Context(), `SELECT af.storage_key,c.wave_filename,af.sha256,af.original_filename
 FROM charts c JOIN chart_resources af ON af.chart_id=c.id AND af.kind='audio'
 WHERE c.id=$1 AND `+publishedChart, r.PathValue("id")).Scan(&key, &name, &digest, &originalName)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在或已删除")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	f, err := s.Config.Objects.Open(r.Context(), key)
	if err != nil {
		internal(w, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", audio.MediaType(originalName))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	w.Header().Set("ETag", `"`+digest+`"`)
	// Revalidate cached bytes so deletion/replacement cannot leave an old URL
	// playable indefinitely. Proxies must preserve encoded bytes and byte ranges.
	w.Header().Set("Cache-Control", "public, no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	// Serve directly from a seekable file: bounded memory, GET/HEAD, 206/416,
	// conditional requests and If-Range are handled by the standard library.
	http.ServeContent(w, r, name, f.ModTime(), f)
}
