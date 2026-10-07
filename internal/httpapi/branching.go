package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"ourtaiko.dev/fanmade/api/internal/tja"
)

// Charts uploaded before migration 031 lack difficulties[].branching. Only
// charts whose difficulties pass the current validator are rewritten, so the
// update cannot fail on archived unsupported metadata.
const branchingPending = `c.status<>'deleted' AND valid_chart_difficulties(c.difficulties,c.is_single)
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(c.difficulties) d WHERE NOT d ? 'branching')`

// BackfillBranching re-reads each pending chart's stored TJA. Row locks
// serialize with owner edits and file replacements across API replicas.
func (s *Server) BackfillBranching(ctx context.Context) error {
	rows, err := s.DB.Query(ctx, `SELECT id FROM charts c WHERE `+branchingPending+` ORDER BY id`)
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
		if err := s.backfillBranching(ctx, id); err != nil {
			log.Printf("branching backfill for %s failed: %v", id, err)
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Server) backfillBranching(parent context.Context, id string) error {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked string
	err = tx.QueryRow(ctx, `SELECT id FROM charts c WHERE c.id=$1 AND `+branchingPending+` FOR UPDATE SKIP LOCKED`, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	c, err := s.readChart(tx.QueryRow(ctx, chartSelect+` WHERE c.id=$1`, id))
	if err != nil {
		return err
	}
	f, err := s.Config.Objects.Open(ctx, c.TJAKey)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, tja.MaxTJA+1))
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	// The legacy parser accepts every stored file; it keeps P1/P2 out of the course name.
	meta, issue := tja.ParseLegacy(data, c.Encoding, c.Wave)
	if issue != nil {
		return issue
	}
	branching := map[string]bool{}
	for _, d := range meta.Difficulties {
		course := d.Course
		switch d.Player {
		case "P1":
			course += "_1p"
		case "P2":
			course += "_2p"
		}
		branching[course] = d.Branching
	}
	for i := range c.Difficulties {
		b, ok := branching[c.Difficulties[i].Course]
		if !ok {
			return fmt.Errorf("stored TJA has no %s block", c.Difficulties[i].Course)
		}
		c.Difficulties[i].Branching = b
	}
	if _, err = tx.Exec(ctx, `UPDATE charts SET difficulties=$2 WHERE id=$1`, id, c.Difficulties); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) RunBranchingBackfill(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		if err := s.BackfillBranching(ctx); err != nil && ctx.Err() == nil {
			log.Printf("branching backfill pending: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
