// migrate-storage copies a database/file snapshot into an isolated S3 prefix.
// It never deletes source files or replaces an existing object with different bytes.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/smithy-go"
	"github.com/jackc/pgx/v5/pgxpool"
	"ourtaiko.dev/fanmade/api/internal/database"
	"ourtaiko.dev/fanmade/api/internal/objectstore"
)

type entry struct {
	ID, Key, Name, Media, SHA string
	Size                      int64
	Data                      []byte
}
type migrator struct {
	ctx              context.Context
	store            *objectstore.S3
	copied, verified int
	bytes            int64
	apply            bool
}

func (m *migrator) copy(v entry, r io.ReadSeeker) error {
	h := sha256.New()
	n, e := io.Copy(h, r)
	if e != nil {
		return e
	}
	if n != v.Size || hex.EncodeToString(h.Sum(nil)) != v.SHA {
		return fmt.Errorf("source integrity mismatch: %s", v.Key)
	}
	if !m.apply {
		return nil
	}
	if _, e = r.Seek(0, io.SeekStart); e != nil {
		return e
	}
	if e = m.store.Put(m.ctx, v.Key, r, v.Size, v.Media, v.SHA); e != nil {
		var api smithy.APIError
		if !errors.As(e, &api) || api.ErrorCode() != "PreconditionFailed" {
			return e
		}
	} else {
		m.copied++
	}
	// Read back every byte, including on retries. Metadata alone is not proof.
	object, e := m.store.Open(m.ctx, v.Key)
	if e != nil {
		return e
	}
	defer object.Close()
	h.Reset()
	n, e = io.Copy(h, object)
	if e != nil {
		return e
	}
	if n != v.Size || hex.EncodeToString(h.Sum(nil)) != v.SHA {
		return fmt.Errorf("destination integrity mismatch (not overwritten): %s", v.Key)
	}
	m.verified++
	m.bytes += n
	return nil
}
func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
func run() error {
	source := flag.String("source", "", "snapshot file directory")
	bucket := flag.String("bucket", "", "destination bucket")
	region := flag.String("region", "", "destination region")
	prefix := flag.String("prefix", "", "isolated destination prefix")
	apply := flag.Bool("apply", false, "copy and read back all objects (default: inventory/source verification only)")
	activate := flag.Bool("activate", false, "migrate and activate S3 metadata in a database ending _s3_test")
	flag.Parse()
	if *source == "" {
		return errors.New("source snapshot directory required")
	}
	if *activate && !*apply {
		return errors.New("activate requires apply")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	db, e := database.Open(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer db.Close()
	if *activate {
		var name string
		if e = db.QueryRow(ctx, `SELECT current_database()`).Scan(&name); e != nil {
			return e
		}
		if !strings.HasSuffix(name, "_s3_test") {
			return errors.New("activation is restricted to a dedicated _s3_test database")
		}
		if e = database.Migrate(ctx, db, *source); e != nil {
			return e
		}
	}
	store, e := objectstore.NewS3(ctx, *bucket, *region, *prefix)
	if e != nil {
		return e
	}
	m := migrator{ctx: ctx, store: store, apply: *apply}
	rows, e := db.Query(ctx, `SELECT id,storage_key,original_filename,media_type,sha256,byte_size FROM files ORDER BY storage_key`)
	if e != nil {
		return e
	}
	var files []entry
	for rows.Next() {
		var v entry
		if e = rows.Scan(&v.ID, &v.Key, &v.Name, &v.Media, &v.SHA, &v.Size); e != nil {
			rows.Close()
			return e
		}
		files = append(files, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	local := objectstore.Local{Root: *source}
	for _, v := range files {
		f, e := local.Open(ctx, v.Key)
		if e != nil {
			return e
		}
		e = m.copy(v, f)
		f.Close()
		if e != nil {
			return e
		}
	}
	rows, e = db.Query(ctx, `SELECT id,webp,sha256 FROM chart_covers ORDER BY id`)
	if e != nil {
		return e
	}
	var covers []entry
	for rows.Next() {
		var v entry
		if e = rows.Scan(&v.ID, &v.Data, &v.SHA); e != nil {
			rows.Close()
			return e
		}
		if v.Data == nil {
			rows.Close()
			return errors.New("migration requires original cover bytes retained in snapshot")
		}
		v.Key = "covers/" + v.ID + "/cover.webp"
		v.Size = int64(len(v.Data))
		v.Media = "image/webp"
		covers = append(covers, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, v := range covers {
		if e = m.copy(v, bytes.NewReader(v.Data)); e != nil {
			return e
		}
	}
	archives, e := copyArchives(ctx, db, &m, *source)
	if e != nil {
		return e
	}
	if *activate {
		tx, e := db.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		for _, v := range covers {
			if _, e = tx.Exec(ctx, `UPDATE chart_covers SET storage_key=$2,byte_size=$3 WHERE id=$1 AND sha256=$4`, v.ID, v.Key, v.Size, v.SHA); e != nil {
				return e
			}
		}
		for _, v := range archives {
			if _, e = tx.Exec(ctx, `INSERT INTO chart_archives(version_id,storage_key,sha256,byte_size) VALUES($1,$2,$3,$4) ON CONFLICT(version_id) DO UPDATE SET storage_key=EXCLUDED.storage_key,sha256=EXCLUDED.sha256,byte_size=EXCLUDED.byte_size`, v.ID, v.Key, v.SHA, v.Size); e != nil {
				return e
			}
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"files": len(files), "covers": len(covers), "archives": len(archives), "uploaded": m.copied, "verified": m.verified, "verifiedBytes": m.bytes, "activated": *activate, "applied": *apply})
}
func copyArchives(ctx context.Context, db *pgxpool.Pool, m *migrator, source string) ([]entry, error) {
	rows, e := db.Query(ctx, `SELECT v.id,tf.storage_key,af.storage_key,tf.original_filename,v.wave_filename FROM chart_versions v JOIN files tf ON tf.id=v.tja_file_id JOIN files af ON af.id=v.audio_file_id ORDER BY v.id`)
	if e != nil {
		return nil, e
	}
	type pair struct{ id, tja, audio, tjaName, audioName string }
	var pairs []pair
	for rows.Next() {
		var p pair
		if e = rows.Scan(&p.id, &p.tja, &p.audio, &p.tjaName, &p.audioName); e != nil {
			rows.Close()
			return nil, e
		}
		pairs = append(pairs, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	dir, e := os.MkdirTemp("", "fanmade-migration-zips-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(dir)
	var entries []entry
	for _, p := range pairs {
		if !objectstore.ValidKey(p.tja) || !objectstore.ValidKey(p.audio) {
			return nil, objectstore.ErrKey
		}
		file, size, digest, e := objectstore.Archive(dir, filepath.Join(source, p.tja), filepath.Join(source, p.audio), p.tjaName, p.audioName)
		if e != nil {
			return nil, e
		}
		v := entry{ID: p.id, Key: "archives/" + p.id + "/download.zip", Size: size, SHA: digest, Media: "application/zip"}
		f, e := os.Open(file)
		if e != nil {
			return nil, e
		}
		e = m.copy(v, f)
		f.Close()
		os.Remove(file)
		if e != nil {
			return nil, e
		}
		entries = append(entries, v)
	}
	return entries, nil
}
