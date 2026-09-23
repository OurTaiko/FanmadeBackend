package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"ourtaiko.dev/fanmade/api/internal/cover"
	"ourtaiko.dev/fanmade/api/internal/tja"
)

// Website responses carry only a content hash, never the image bytes. Native
// category/bootstrap responses continue to use the original readChart contract.
func (s *Server) coverHashes(ctx context.Context, charts []Chart) error {
	if len(charts) == 0 {
		return nil
	}
	ids := make([]string, len(charts))
	positions := make(map[string]int, len(charts))
	for i, c := range charts {
		ids[i] = c.ID
		positions[c.ID] = i
	}
	rows, err := s.DB.Query(ctx, `SELECT chart_id,sha256 FROM chart_covers WHERE chart_id=ANY($1)`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, digest string
		if err = rows.Scan(&id, &digest); err != nil {
			return err
		}
		charts[positions[id]].CoverHash = digest
	}
	return rows.Err()
}

func saveCover(ctx context.Context, tx pgx.Tx, chartID string, data []byte) error {
	if _, err := tx.Exec(ctx, `DELETE FROM chart_covers WHERE chart_id=$1`, chartID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO chart_covers(id,chart_id,webp,sha256) VALUES($1,$2,$3,$4)`, ID(), chartID, data, hash(string(data)))
	return err
}

func (s *Server) getCover(w http.ResponseWriter, r *http.Request) {
	var data []byte
	var digest string
	err := s.DB.QueryRow(r.Context(), `SELECT cv.webp,cv.sha256 FROM chart_covers cv JOIN charts c ON c.id=cv.chart_id WHERE c.id=$1 AND `+publishedChart, r.PathValue("id")).Scan(&data, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "COVER_NOT_FOUND", "封面不存在")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if version := r.URL.Query().Get("v"); version != "" && version != digest {
		problem(w, 404, "COVER_NOT_FOUND", "该封面已被替换")
		return
	}
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Content-Disposition", `inline; filename="cover.webp"`)
	// Revalidate even hash URLs: replaced/deleted images must not stay addressable.
	w.Header().Set("Cache-Control", "public, no-cache")
	w.Header().Set("ETag", `"`+digest+`"`)
	http.ServeContent(w, r, "cover.webp", time.Time{}, bytes.NewReader(data))
}

func encodeCover(w http.ResponseWriter, r *http.Request, name string, data []byte) ([]byte, bool) {
	encoded, err := cover.Encode(r.Context(), name, data)
	if errors.Is(err, cover.ErrInvalid) {
		problem(w, 422, "COVER_INVALID", "封面必须是有效的 JPG、PNG 或 WebP，最大 8 MiB、1600 万像素，单边不超过 8192 像素")
		return nil, false
	}
	if err != nil {
		internal(w, err)
		return nil, false
	}
	return encoded, true
}

func (s *Server) replaceCover(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var owner string
	err := s.DB.QueryRow(r.Context(), `SELECT owner_id FROM charts c WHERE c.id=$1 AND `+publishedChart, id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在或已删除")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if owner != u.User.ID {
		problem(w, 403, "FORBIDDEN", "只有上传者可以修改封面")
		return
	}
	select {
	case s.uploads <- struct{}{}:
		defer func() { <-s.uploads }()
	default:
		problem(w, 503, "UPLOAD_BUSY", "当前正在处理其他上传，请稍后重试")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, cover.MaxBytes+64*1024)
	mr, err := r.MultipartReader()
	if err != nil {
		problem(w, 400, "COVER_INVALID", "请选择封面文件")
		return
	}
	part, err := mr.NextPart()
	if err != nil {
		uploadReadError(w, err)
		return
	}
	disposition, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	name := params["filename"]
	if err != nil || disposition != "form-data" || params["name"] != "cover" || !tja.SafeFilename(name) || !cover.ValidExtension(name) {
		problem(w, 400, "COVER_INVALID", "只能上传一个 JPG、PNG 或 WebP 封面")
		return
	}
	data, err := io.ReadAll(io.LimitReader(part, cover.MaxBytes+1))
	if err != nil {
		uploadReadError(w, err)
		return
	}
	if len(data) > cover.MaxBytes {
		problem(w, 413, "FILE_TOO_LARGE", "封面不能超过 8 MiB")
		return
	}
	if _, err = mr.NextPart(); err != io.EOF {
		if err != nil {
			uploadReadError(w, err)
		} else {
			problem(w, 400, "COVER_INVALID", "只能上传一个封面文件")
		}
		return
	}
	encoded, ok := encodeCover(w, r, name, data)
	if !ok {
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize with deletion/file replacement and recheck ownership after encoding.
	err = tx.QueryRow(r.Context(), `SELECT owner_id FROM charts c WHERE c.id=$1 AND `+publishedChart+` FOR UPDATE`, id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在或已删除")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if owner != u.User.ID {
		problem(w, 403, "FORBIDDEN", "只有上传者可以修改封面")
		return
	}
	if err = saveCover(r.Context(), tx, id, encoded); err != nil {
		internal(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		internal(w, err)
		return
	}
	respond(w, 200, map[string]string{"coverHash": hash(string(encoded))})
}
