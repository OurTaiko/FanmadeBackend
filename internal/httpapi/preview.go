package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"ourtaiko.dev/fanmade/api/internal/audio"
	"ourtaiko.dev/fanmade/api/internal/objectstore"
)

func (s *Server) readChart(row pgx.Row) (Chart, error) {
	c, err := readChart(row)
	if remote, ok := s.Config.Objects.(*objectstore.S3); ok && c.PreviewPath != "" {
		c.PreviewPath = remote.Prefix + c.PreviewPath
	}
	return c, err
}

// The caller holds the chart row lock. Upload first, atomically switch the
// resource reference, then let retirement cleanup delete the previous object.
// A unique key prevents stale CDN caches and preserves the old preview on failure.
func (s *Server) savePreview(ctx context.Context, tx pgx.Tx, id, source string, start, end, duration float64) error {
	dir, err := os.MkdirTemp("", "fanmade-preview-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	output := filepath.Join(dir, "preview.ogg")
	if err = audio.Preview(ctx, source, output, start, end, duration); err != nil {
		return err
	}
	f, err := os.Open(output)
	if err != nil {
		return err
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	key := "previews/" + id + "/" + ID() + "/preview.ogg"
	if err = s.storeFile(ctx, key, output, size, "audio/ogg", digest); err != nil {
		return err
	}
	// pending_objects reconciles failed or ambiguous commits without deleting a
	// potentially live object. The existing trigger queues replaced objects.
	_, err = tx.Exec(ctx, `INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type)
 VALUES($1,'preview',$2,'preview.ogg',$3,$4,'audio/ogg') ON CONFLICT(chart_id,kind) DO UPDATE SET storage_key=EXCLUDED.storage_key,sha256=EXCLUDED.sha256,byte_size=EXCLUDED.byte_size`, id, key, digest, size)
	return err
}

func (s *Server) previewFromStoredAudio(ctx context.Context, tx pgx.Tx, c Chart) error {
	dir, err := os.MkdirTemp("", "fanmade-preview-source-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	source, err := s.copyAudio(ctx, &c, dir)
	if err != nil {
		return err
	}
	return s.savePreview(ctx, tx, c.ID, source.path, c.DemoStart, c.DemoEnd, c.Duration)
}

// Resume existing-chart backfill on startup and retry failures on later passes.
// Row locks serialize with owner edits and audio replacements across API replicas.
func (s *Server) BackfillPreviews(ctx context.Context) error {
	rows, err := s.DB.Query(ctx, `SELECT id FROM charts c WHERE c.status<>'deleted' AND NOT EXISTS(SELECT 1 FROM chart_resources r WHERE r.chart_id=c.id AND r.kind='preview') ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := s.backfillPreview(ctx, id)
		if err != nil {
			log.Printf("preview backfill for %s failed: %v", id, err)
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (s *Server) backfillPreview(parent context.Context, id string) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked string
	err = tx.QueryRow(ctx, `SELECT id FROM charts WHERE id=$1 AND status<>'deleted' FOR UPDATE SKIP LOCKED`, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chart_resources WHERE chart_id=$1 AND kind='preview')`, id).Scan(&exists); err != nil || exists {
		return err
	}
	c, err := s.readChart(tx.QueryRow(ctx, chartSelect+` WHERE c.id=$1`, id))
	if err != nil {
		return err
	}
	if err = s.previewFromStoredAudio(ctx, tx, c); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Server) RunPreviewBackfill(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		if err := s.BackfillPreviews(ctx); err != nil && ctx.Err() == nil {
			log.Printf("preview backfill pending: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
