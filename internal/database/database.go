package database

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"ourtaiko.dev/fanmade/api/internal/tja"
)

//go:embed schema.sql
var schema string

//go:embed 002_ese.sql
var eseSchema string

//go:embed 003_cloud_score_policy.sql
var cloudScoreSchema string

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// Migrate applies the initial schema once, atomically, with a transaction lock.
func Migrate(ctx context.Context, pool *pgxpool.Pool, storage string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(73420118)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=1)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, schema); err != nil {
			return fmt.Errorf("migration 001: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(1)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=2)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, eseSchema); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(2)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=3)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, cloudScoreSchema); err != nil {
			return fmt.Errorf("migration 003: %w", err)
		}
		if err = backfillStyles(ctx, tx, storage); err != nil {
			return fmt.Errorf("migration 003: %w", err)
		}
		// New writers must explicitly provide the server-parsed style.
		if _, err = tx.Exec(ctx, `ALTER TABLE difficulties ALTER COLUMN style DROP DEFAULT;
		 INSERT INTO schema_migrations(version) VALUES(3)`); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Old rows only recorded P1/P2, so STYLE:Double with a plain #START must be
// recovered from the original file too. Never guess eligibility from player alone.
func backfillStyles(ctx context.Context, tx pgx.Tx, storage string) error {
	rows, err := tx.Query(ctx, `SELECT v.id, v.encoding, v.wave_filename, f.storage_key
	 FROM chart_versions v JOIN files f ON f.id=v.tja_file_id`)
	if err != nil {
		return err
	}
	type version struct{ id, encoding, wave, key string }
	versions := []version{}
	for rows.Next() {
		var v version
		if err = rows.Scan(&v.id, &v.encoding, &v.wave, &v.key); err != nil {
			rows.Close()
			return err
		}
		versions = append(versions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range versions {
		if !filepath.IsLocal(v.key) {
			return fmt.Errorf("invalid storage key for version %s", v.id)
		}
		data, err := os.ReadFile(filepath.Join(storage, v.key))
		if err != nil {
			return fmt.Errorf("read TJA for version %s: %w", v.id, err)
		}
		meta, issue := tja.Parse(data, v.encoding, v.wave)
		if issue != nil {
			return fmt.Errorf("parse TJA for version %s: %w", v.id, issue)
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM difficulties WHERE version_id=$1`, v.id).Scan(&count); err != nil {
			return err
		}
		if count != len(meta.Difficulties) {
			return fmt.Errorf("difficulty count mismatch for version %s", v.id)
		}
		for _, d := range meta.Difficulties {
			result, err := tx.Exec(ctx, `UPDATE difficulties SET style=$1
			 WHERE version_id=$2 AND block_index=$3 AND course=$4 AND player=$5`, d.Style, v.id, d.BlockIndex, d.Course, d.Player)
			if err != nil {
				return err
			}
			if result.RowsAffected() != 1 {
				return fmt.Errorf("difficulty mismatch for version %s block %d", v.id, d.BlockIndex)
			}
		}
	}
	return nil
}
