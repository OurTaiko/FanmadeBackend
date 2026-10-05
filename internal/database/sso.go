package database

import (
	"context"
	_ "embed"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed 018_sso.sql
var ssoSchema string

// MigrateSSO refuses to remove legacy credentials until the caller confirms all IDs
// exist in SSO. The caller must take a database backup before enabling this migration.
func MigrateSSO(ctx context.Context, pool *pgxpool.Pool, check func(context.Context, []string) error) error {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(73420118)`); e != nil {
		return e
	}
	var applied bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=18)`).Scan(&applied); e != nil {
		return e
	}
	if applied {
		if e = groupSchemas(ctx, tx); e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	if _, e = tx.Exec(ctx, `LOCK TABLE users IN ACCESS EXCLUSIVE MODE`); e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT id FROM users ORDER BY id`)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(ids) > 0 {
		if check == nil {
			return errors.New("SSO migration requires identity preflight")
		}
		if e = check(ctx, ids); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, ssoSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES(18)`); e != nil {
		return e
	}
	if e = groupSchemas(ctx, tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
