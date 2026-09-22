package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Server) beginUpload(w http.ResponseWriter, r *http.Request, user, key, digest string) (pgx.Tx, bool) {
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		internal(w, err)
		return nil, false
	}
	proceed := false
	defer func() {
		if !proceed {
			tx.Rollback(r.Context())
		}
	}()
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, user+":"+key); err != nil {
		internal(w, err)
		return nil, false
	}
	var chartID, previousDigest, version string
	err = tx.QueryRow(r.Context(), `SELECT chart_id,payload_digest,version_id FROM upload_requests WHERE user_id=$1 AND idempotency_key=$2`, user, key).Scan(&chartID, &previousDigest, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		proceed = true
		return tx, true
	}
	if err != nil {
		internal(w, err)
		return nil, false
	}
	if previousDigest != digest {
		problem(w, 409, "IDEMPOTENCY_CONFLICT", "此请求标识已用于不同的内容，请重新选择后提交")
		return nil, false
	}
	c, err := readChart(tx.QueryRow(r.Context(), chartSelect+` WHERE c.id=$1 AND `+publishedChart, chartID))
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 409, "CHART_REMOVED", "该次上传的作品已删除，请重新选择文件")
		return nil, false
	}
	if err != nil {
		internal(w, err)
		return nil, false
	}
	if version != "" && version != c.VersionID {
		problem(w, 409, "CHART_VERSION_CHANGED", "该次上传已被后续更新替换，请重新打开歌曲页面")
		return nil, false
	}
	items := []Chart{c}
	if err = s.coverHashes(r.Context(), items); err != nil {
		internal(w, err)
		return nil, false
	}
	c = items[0]
	c.Uploader = s.publicNames(r.Context(), []string{c.OwnerID})[c.OwnerID]
	respond(w, 200, c)
	return nil, false
}

// Copy retained audio into the new version; the original object is still retired.
// The caller holds the chart lock so a concurrent replacement cannot remove it.
func (s *Server) copyAudio(c *Chart, dir string) (*stagedFile, error) {
	root, err := os.OpenRoot(s.Config.Storage)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	in, err := root.Open(c.AudioKey)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	f := &stagedFile{name: c.AudioName, path: filepath.Join(dir, "audio"), id: ID(), sha: c.AudioHash, size: c.AudioSize}
	out, err := os.OpenFile(f.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, c.AudioSize+1))
	closeErr := out.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return nil, err
	}
	if n != c.AudioSize || hex.EncodeToString(h.Sum(nil)) != c.AudioHash {
		return nil, errors.New("stored audio integrity mismatch")
	}
	return f, nil
}

func retireChart(ctx context.Context, tx pgx.Tx, chartID, versionID, description string) error {
	// The deferred current-version FK permits replacing the version atomically.
	if _, err := tx.Exec(ctx, `UPDATE charts SET current_version_id=$2,description=$3,title_override=NULL,subtitle_override=NULL,title_translation_overrides='{}',subtitle_translation_overrides='{}',metadata_updated_at=now() WHERE id=$1`, chartID, versionID, description); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM scores WHERE song_id=$1`, chartID); err != nil {
		return err
	}
	var ids []string
	if err := tx.QueryRow(ctx, `SELECT ARRAY(SELECT tja_file_id FROM chart_versions WHERE chart_id=$1 UNION SELECT audio_file_id FROM chart_versions WHERE chart_id=$1)`, chartID).Scan(&ids); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM chart_versions WHERE chart_id=$1`, chartID); err != nil {
		return err
	}
	// Protect any shared file references from earlier imports.
	_, err := tx.Exec(ctx, `WITH removed AS (
  DELETE FROM files f WHERE id=ANY($1) AND NOT EXISTS (
   SELECT 1 FROM chart_versions v WHERE v.tja_file_id=f.id OR v.audio_file_id=f.id
  ) RETURNING storage_key
 ) INSERT INTO retired_files(storage_key) SELECT storage_key FROM removed ON CONFLICT DO NOTHING`, ids)
	return err
}

// RunFileCleanup retries committed file deletions on startup and every 30 seconds.
func (s *Server) RunFileCleanup(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		s.cleanupReplacedFiles()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) cleanupReplacedFiles() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.deleteRetiredFiles(ctx); err != nil {
		log.Printf("retired file cleanup pending: %v", err)
	}
}

func (s *Server) deleteRetiredFiles(ctx context.Context) error {
	root, err := os.OpenRoot(s.Config.Storage)
	if err != nil {
		return err
	}
	defer root.Close()
	rows, err := s.DB.Query(ctx, `SELECT storage_key FROM retired_files ORDER BY created_at,storage_key LIMIT 100`)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, key := range keys {
		if !filepath.IsLocal(key) {
			failures = append(failures, errors.New("invalid retired storage key"))
			continue
		}
		if err = root.Remove(key); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, err)
			continue
		}
		if _, err = s.DB.Exec(ctx, `DELETE FROM retired_files WHERE storage_key=$1`, key); err != nil {
			failures = append(failures, err)
			continue
		}
		if dir := filepath.Dir(key); strings.HasPrefix(dir, "objects"+string(filepath.Separator)) {
			_ = root.Remove(dir)
		}
	}
	return errors.Join(failures...)
}
