package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/jackc/pgx/v5"
	"ourtaiko.dev/fanmade/api/internal/objectstore"
)

// Export inline local covers before removing bytea storage. Existing S3 keys are
// unchanged. A failed transaction may leave a verified file, which a retry reuses.
func exportLocalCovers(ctx context.Context, tx pgx.Tx, root string, remote bool) error {
	rows, err := tx.Query(ctx, `SELECT chart_id,webp,sha256 FROM chart_covers WHERE storage_key IS NULL`)
	if err != nil {
		return err
	}
	type cover struct {
		id     string
		data   []byte
		digest string
	}
	var covers []cover
	for rows.Next() {
		var c cover
		if err = rows.Scan(&c.id, &c.data, &c.digest); err != nil {
			rows.Close()
			return err
		}
		covers = append(covers, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if remote && len(covers) > 0 {
		return fmt.Errorf("inline covers must be migrated to S3 before starting an S3-only backend")
	}
	local := objectstore.Local{Root: root}
	for _, c := range covers {
		sum := sha256.Sum256(c.data)
		if len(c.data) < 12 || hex.EncodeToString(sum[:]) != c.digest {
			return fmt.Errorf("cover integrity mismatch for chart %s", c.id)
		}
		key := "covers/imported/" + c.digest + ".webp"
		if err = os.MkdirAll(root, 0700); err != nil {
			return err
		}
		err = local.Put(ctx, key, bytes.NewReader(c.data), int64(len(c.data)), "image/webp", c.digest)
		if err != nil && !os.IsExist(err) {
			return err
		}
		f, e := local.Open(ctx, key)
		if e != nil {
			return e
		}
		data, e := io.ReadAll(io.LimitReader(f, int64(len(c.data))+1))
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if !bytes.Equal(data, c.data) {
			return fmt.Errorf("exported cover integrity mismatch for chart %s", c.id)
		}
		if _, err = tx.Exec(ctx, `UPDATE chart_covers SET storage_key=$2,byte_size=$3 WHERE chart_id=$1`, c.id, key, len(data)); err != nil {
			return err
		}
	}
	return nil
}

// Production uses public/auth/internal. Isolated test schemas get their own
// sibling namespaces, so concurrent tests cannot share authentication or queues.
func SchemaSearchPath(schema string) string {
	auth, internal := auxiliarySchemas(schema)
	return pgx.Identifier{schema}.Sanitize() + "," + pgx.Identifier{auth}.Sanitize() + "," + pgx.Identifier{internal}.Sanitize()
}
func auxiliarySchemas(schema string) (string, string) {
	if schema == "public" {
		return "auth", "internal"
	}
	return schema + "_auth", schema + "_internal"
}
func groupSchemas(ctx context.Context, tx pgx.Tx) error {
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=26)`).Scan(&done); err != nil {
		return err
	}
	if done {
		return nil
	}
	var schema string
	if err := tx.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return err
	}
	auth, internal := auxiliarySchemas(schema)
	for _, ns := range []string{auth, internal} {
		// Refuse to silently reuse an unrelated schema on first migration.
		if _, err := tx.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{ns}.Sanitize()); err != nil {
			return err
		}
	}
	for _, group := range []struct {
		schema string
		tables []string
	}{
		{auth, []string{"sessions", "oidc_flows"}},
		{internal, []string{"upload_requests", "retired_score_requests", "pending_objects", "retired_files", "schema_migrations"}},
	} {
		for _, table := range group.tables {
			if _, err := tx.Exec(ctx, "ALTER TABLE "+pgx.Identifier{schema, table}.Sanitize()+" SET SCHEMA "+pgx.Identifier{group.schema}.Sanitize()); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec(ctx, "INSERT INTO "+pgx.Identifier{internal, "schema_migrations"}.Sanitize()+"(version) VALUES(26)")
	return err
}
