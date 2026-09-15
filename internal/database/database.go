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

//go:embed 005_scores.sql
var scoresSchema string

//go:embed 006_localized_titles.sql
var localizedTitlesSchema string

//go:embed 007_metadata_overrides.sql
var metadataOverridesSchema string

//go:embed 008_admin.sql
var adminSchema string

//go:embed 009_leaderboard.sql
var leaderboardSchema string

//go:embed 010_max_combo.sql
var maxComboSchema string

//go:embed 011_email_verification.sql
var emailVerificationSchema string

//go:embed 012_supported_courses.sql
var supportedCoursesSchema string

//go:embed 013_categories.sql
var categoriesSchema string

//go:embed 014_difficulty_makers.sql
var difficultyMakersSchema string

//go:embed 015_chart_replacement.sql
var chartReplacementSchema string

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
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=4)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		// Migration 004 repairs STYLE leaking across COURSE boundaries in 003.
		if err = backfillStyles(ctx, tx, storage); err != nil {
			return fmt.Errorf("migration 004: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(4)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=5)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, scoresSchema); err != nil {
			return fmt.Errorf("migration 005: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(5)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=6)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, localizedTitlesSchema); err != nil {
			return fmt.Errorf("migration 006: %w", err)
		}
		if err = backfillLocalizedTitles(ctx, tx, storage); err != nil {
			return fmt.Errorf("migration 006: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(6)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=7)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, metadataOverridesSchema); err != nil {
			return fmt.Errorf("migration 007: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(7)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=8)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, adminSchema); err != nil {
			return fmt.Errorf("migration 008: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(8)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=9)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, leaderboardSchema); err != nil {
			return fmt.Errorf("migration 009: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(9)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=10)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, maxComboSchema); err != nil {
			return fmt.Errorf("migration 010: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(10)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=11)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, emailVerificationSchema); err != nil {
			return fmt.Errorf("migration 011: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(11)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=12)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, supportedCoursesSchema); err != nil {
			return fmt.Errorf("migration 012: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(12)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=13)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, categoriesSchema); err != nil {
			return fmt.Errorf("migration 013: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(13)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=14)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, difficultyMakersSchema); err != nil {
			return fmt.Errorf("migration 014: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(14)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=15)`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, chartReplacementSchema); err != nil {
			return fmt.Errorf("migration 015: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(15)`); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func backfillLocalizedTitles(ctx context.Context, tx pgx.Tx, storage string) error {
	rows, err := tx.Query(ctx, `SELECT v.id,v.encoding,v.wave_filename,f.storage_key FROM chart_versions v JOIN files f ON f.id=v.tja_file_id`)
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
			return fmt.Errorf("read version %s: %w", v.id, err)
		}
		meta, issue := tja.Parse(data, v.encoding, v.wave)
		if issue != nil {
			return fmt.Errorf("parse version %s: %w", v.id, issue)
		}
		if _, err = tx.Exec(ctx, `UPDATE chart_versions SET title_translations=$2,subtitle_translations=$3 WHERE id=$1`, v.id, meta.TitleTranslations, meta.SubtitleTranslations); err != nil {
			return err
		}
	}
	return nil
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
