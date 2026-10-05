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

// CategoryFlags are persisted in charts.category_flags. Never renumber or reuse
// a bit; adding a category also requires widening the database CHECK constraint.
type CategoryFlags int32

const (
	CategoryGame          CategoryFlags = 1
	CategoryVirtualSinger CategoryFlags = 2
	CategoryPop           CategoryFlags = 4
	CategoryClassic       CategoryFlags = 8
	CategoryVariety       CategoryFlags = 16
	CategoryAnime         CategoryFlags = 32
)

// Catalog order and metadata are part of the existing public API contract.
var categoryCatalog = [...]struct {
	Flag CategoryFlags
	Category
}{
	{CategoryGame, Category{"game", "Game", "GAME"}},
	{CategoryVirtualSinger, Category{"virtual-singer", "Virtual Singer", "VOCALOID"}},
	{CategoryPop, Category{"pop", "Pop", "J-POP"}},
	{CategoryClassic, Category{"classic", "Classic", "CLASSICAL"}},
	{CategoryVariety, Category{"variety", "Variety", "VARIETY"}},
	{CategoryAnime, Category{"anime", "Anime", "ANIME"}},
}

func categoryFlag(id string) (CategoryFlags, bool) {
	for _, category := range categoryCatalog {
		if category.ID == id {
			return category.Flag, true
		}
	}
	return 0, false
}

func encodeCategories(ids []string) (CategoryFlags, error) {
	var flags CategoryFlags
	for _, id := range ids {
		flag, ok := categoryFlag(id)
		if !ok {
			return 0, errors.New("invalid category ID")
		}
		flags |= flag
	}
	return flags, nil
}

func (flags CategoryFlags) IDs() []string {
	ids := []string{}
	for _, category := range categoryCatalog {
		if flags&category.Flag != 0 {
			ids = append(ids, category.ID)
		}
	}
	// Chart responses and idempotency payloads use lexical ID order.
	sort.Strings(ids)
	return ids
}

func readCategories() []Category {
	items := make([]Category, 0, len(categoryCatalog))
	for _, category := range categoryCatalog {
		items = append(items, category.Category)
	}
	return items
}

func (s *Server) categories(w http.ResponseWriter, r *http.Request) {
	respond(w, 200, map[string]any{"items": readCategories()})
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

func validCategories(ids []string) bool {
	_, err := encodeCategories(ids)
	return err == nil
}

func setCategories(ctx context.Context, tx pgx.Tx, chart string, ids []string) error {
	flags, err := encodeCategories(ids)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE charts SET category_flags=$2 WHERE id=$1`, chart, flags)
	return err
}

func (s *Server) gameCategory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("category")
	flag, exists := categoryFlag(id)
	if !exists {
		problem(w, 404, "CATEGORY_NOT_FOUND", "分类不存在")
		return
	}
	rows, err := s.DB.Query(r.Context(), chartSelect+` WHERE `+publishedChart+` AND (c.category_flags & $1) <> 0 ORDER BY c.id`, flag)
	if err != nil {
		internal(w, err)
		return
	}
	defer rows.Close()
	charts := []Chart{}
	for rows.Next() {
		c, err := s.readChart(rows)
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
	s.chartNames(r.Context(), charts)
	respond(w, 200, map[string]any{"categoryId": id, "charts": charts})
}
