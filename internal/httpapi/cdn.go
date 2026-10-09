package httpapi

import (
	"mime"
	"net/http"
	"net/url"
	"ourtaiko.dev/fanmade/api/internal/objectstore"
)

// Call only after checking publication and resolving the current object's key.
func (s *Server) redirectResource(w http.ResponseWriter, r *http.Request, key, downloadName string) bool {
	remote, ok := s.Config.Objects.(*objectstore.S3)
	if !ok {
		return false
	}
	target, err := remote.PublicURL(key)
	if err != nil {
		internal(w, err)
		return true
	}
	if target == "" {
		return false
	}
	if downloadName != "" {
		u, _ := url.Parse(target)
		q := u.Query()
		q.Set("response-content-disposition", mime.FormatMediaType("attachment", map[string]string{"filename": downloadName}))
		u.RawQuery = q.Encode()
		target = u.String()
	}
	// Do not cache the chart-ID redirect: replacement and deletion must be rechecked.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
	return true
}
