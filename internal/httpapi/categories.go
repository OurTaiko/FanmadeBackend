package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5"
)

type Category struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Genre string `json:"genre"`
}

type categoryQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func readCategories(ctx context.Context, db categoryQuery) ([]Category, error) {
	rows, err := db.Query(ctx, `SELECT id,title,genre FROM categories ORDER BY sort_order,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Category{}
	for rows.Next() {
		var c Category
		if err = rows.Scan(&c.ID, &c.Title, &c.Genre); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

func (s *Server) categories(w http.ResponseWriter, r *http.Request) {
	items, err := readCategories(r.Context(), s.DB)
	if err != nil {
		internal(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items})
}

// Omitted, null or empty selection defaults to Variety. Canonical ordering also
// makes upload retries independent of checkbox order and duplicate IDs.
func categorySelection(raw json.RawMessage) ([]string, error) {
	var ids []string
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil, err
		}
	}
	if len(ids) == 0 {
		return []string{"variety"}, nil
	}
	sort.Strings(ids)
	unique := ids[:0]
	for _, id := range ids {
		if id == "" || len(id) > 64 {
			return nil, errors.New("invalid category ID")
		}
		if len(unique) == 0 || unique[len(unique)-1] != id {
			unique = append(unique, id)
		}
	}
	return unique, nil
}

func validCategories(ctx context.Context, db categoryQuery, ids []string) (bool, error) {
	rows, err := db.Query(ctx, `SELECT id FROM categories WHERE id=ANY($1::text[])`, ids)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	return count == len(ids), rows.Err()
}

func setCategories(ctx context.Context, tx pgx.Tx, chart string, ids []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM chart_categories WHERE chart_id=$1`, chart); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO chart_categories(category_id,chart_id) SELECT unnest($2::text[]),$1`, chart, ids)
	return err
}

func (s *Server) gameCategory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("category")
	var exists bool
	if err := s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM categories WHERE id=$1)`, id).Scan(&exists); err != nil {
		internal(w, err)
		return
	}
	if !exists {
		problem(w, 404, "CATEGORY_NOT_FOUND", "分类不存在")
		return
	}
	rows, err := s.DB.Query(r.Context(), chartSelect+` WHERE `+publishedChart+` AND EXISTS(SELECT 1 FROM chart_categories cc WHERE cc.chart_id=c.id AND cc.category_id=$1) ORDER BY c.id`, id)
	if err != nil {
		internal(w, err)
		return
	}
	defer rows.Close()
	charts := []Chart{}
	for rows.Next() {
		c, err := readChart(rows)
		if err != nil {
			internal(w, err)
			return
		}
		charts = append(charts, c)
	}
	if err = rows.Err(); err != nil {
		internal(w, err)
		return
	}
	respond(w, 200, map[string]any{"categoryId": id, "charts": charts})
}
