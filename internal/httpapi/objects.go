package httpapi

import (
	"context"
	"github.com/jackc/pgx/v5"
	"os"
	"ourtaiko.dev/fanmade/api/internal/objectstore"
)

func (s *Server) remoteStorage() bool { _, ok := s.Config.Objects.(*objectstore.S3); return ok }
func (s *Server) storeFile(ctx context.Context, key, path string, size int64, media, digest string) error {
	if s.remoteStorage() {
		if _, e := s.DB.Exec(ctx, `INSERT INTO pending_objects(storage_key) VALUES($1) ON CONFLICT DO NOTHING`, key); e != nil {
			return e
		}
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return s.Config.Objects.Put(ctx, key, f, size, media, digest)
}
func (s *Server) saveArchive(ctx context.Context, tx pgx.Tx, version, tjaPath, audioPath, tjaName, audioName, dir string) error {
	file, size, digest, e := objectstore.Archive(dir, tjaPath, audioPath, tjaName, audioName)
	if e != nil {
		return e
	}
	defer os.Remove(file)
	key := "archives/" + version + "/download.zip"
	if e = s.storeFile(ctx, key, file, size, "application/zip", digest); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO chart_archives(version_id,storage_key,sha256,byte_size) VALUES($1,$2,$3,$4)`, version, key, digest, size)
	return e
}
func (s *Server) reconcilePending(ctx context.Context) error {
	if !s.remoteStorage() {
		return nil
	}
	// Only age out abandoned writes after a full day. A commit with an unknown
	// outcome is resolved against live references before any object is retired.
	_, e := s.DB.Exec(ctx, `WITH settled AS (
 DELETE FROM pending_objects p WHERE p.created_at<now()-interval '1 day' RETURNING storage_key
 ) INSERT INTO retired_files(storage_key)
 SELECT storage_key FROM settled p WHERE
 NOT EXISTS(SELECT 1 FROM files f WHERE f.storage_key=p.storage_key) AND
 NOT EXISTS(SELECT 1 FROM chart_covers c WHERE c.storage_key=p.storage_key) AND
 NOT EXISTS(SELECT 1 FROM chart_archives a WHERE a.storage_key=p.storage_key)
 ON CONFLICT DO NOTHING`)
	return e
}
