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
	var chartID, previousDigest, tjaHash, audioHash string
	err = tx.QueryRow(r.Context(), `SELECT chart_id,payload_digest,tja_sha256,audio_sha256 FROM upload_requests WHERE user_id=$1 AND idempotency_key=$2`, user, key).Scan(&chartID, &previousDigest, &tjaHash, &audioHash)
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
	c, err := s.readChart(tx.QueryRow(r.Context(), chartSelect+` WHERE c.id=$1 AND `+publishedChart, chartID))
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 409, "CHART_REMOVED", "该次上传的作品已删除，请重新选择文件")
		return nil, false
	}
	if err != nil {
		internal(w, err)
		return nil, false
	}
	if tjaHash != c.TJAHash || audioHash != c.AudioHash {
		problem(w, 409, "CHART_FILES_CHANGED", "该次上传已被后续更新替换，请重新打开歌曲页面")
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

// Copy retained audio into the replacement; the original object is still retired.
// The caller holds the chart lock so a concurrent replacement cannot remove it.
func (s *Server) copyAudio(ctx context.Context, c *Chart, dir string) (*stagedFile, error) {
	in, err := s.Config.Objects.Open(ctx, c.AudioKey)
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

func retireChart(ctx context.Context, tx pgx.Tx, chartID, description string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO retired_score_requests(user_id,idempotency_key) SELECT user_id,idempotency_key FROM scores WHERE song_id=$1 AND idempotency_key IS NOT NULL ON CONFLICT DO NOTHING`, chartID); err != nil {
		return err
	}
	for _, query := range []string{`DELETE FROM scores WHERE song_id=$1`, `DELETE FROM chart_resources WHERE chart_id=$1 AND kind IN ('tja','audio','archive','preview')`} {
		if _, err := tx.Exec(ctx, query, chartID); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE charts SET description=$2,metadata_updated_at=now() WHERE id=$1`, chartID, description)
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
	if err := s.reconcilePending(ctx); err != nil {
		log.Printf("pending object cleanup failed: %v", err)
		return
	}
	if err := s.deleteRetiredFiles(ctx); err != nil {
		log.Printf("retired file cleanup pending: %v", err)
	}
}

func (s *Server) deleteRetiredFiles(ctx context.Context) error {
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
		var live bool
		if err = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chart_resources WHERE storage_key=$1)`, key).Scan(&live); err != nil {
			failures = append(failures, err)
			continue
		}
		if live {
			if _, err = s.DB.Exec(ctx, `DELETE FROM retired_files WHERE storage_key=$1`, key); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		if err = s.Config.Objects.Delete(ctx, key); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, err)
			continue
		}
		if _, err = s.DB.Exec(ctx, `DELETE FROM retired_files WHERE storage_key=$1`, key); err != nil {
			failures = append(failures, err)
			continue
		}

	}
	return errors.Join(failures...)
}
