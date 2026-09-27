package httpapi

import (
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Native routes use SSO game sessions; browser routes use OIDC access tokens.
// Neither session type grants access to the other.
func isGameRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/v1/game/")
}

func (s *Server) gameBootstrap(w http.ResponseWriter, r *http.Request) {
	// Catalog access is public. Only a native bearer session can opt into
	// personal scores; browser cookies never turn a guest into a game user.
	var user *User
	if r.Header.Get("Authorization") != "" {
		u, ok := s.required(w, r, false)
		if !ok {
			return
		}
		user = &u.User
	}
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	categories, err := readCategories(r.Context(), tx)
	if err != nil {
		internal(w, err)
		return
	}
	// Counts travel with the category metadata, without fetching chart bodies.
	// Each category counts memberships; the server total counts distinct charts.
	type gameCategoryInfo struct {
		Category
		ChartCount int `json:"chartCount"`
	}
	counts := make([]gameCategoryInfo, 0, len(categories))
	for _, category := range categories {
		var n int
		if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM chart_categories cc JOIN charts c ON c.id=cc.chart_id WHERE cc.category_id=$1 AND `+publishedChart, category.ID).Scan(&n); err != nil {
			internal(w, err)
			return
		}
		counts = append(counts, gameCategoryInfo{category, n})
	}
	var chartCount int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM charts c WHERE `+publishedChart+` AND EXISTS(SELECT 1 FROM chart_categories cc WHERE cc.chart_id=c.id)`).Scan(&chartCount); err != nil {
		internal(w, err)
		return
	}
	scores := []Score{}
	if user != nil {
		rows, err := tx.Query(r.Context(), `SELECT `+scoreColumns+` FROM scores WHERE user_id=$1 AND difficulty IN `+supportedCoursesSQL+` AND NOT EXISTS (SELECT 1 FROM difficulties excluded WHERE excluded.version_id=scores.version_id AND excluded.course NOT IN `+supportedCoursesSQL+`) ORDER BY submitted_at,id`, user.ID)
		if err != nil {
			internal(w, err)
			return
		}
		for rows.Next() {
			v, e := readScore(rows)
			if e != nil {
				rows.Close()
				internal(w, e)
				return
			}
			scores = append(scores, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			internal(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		internal(w, err)
		return
	}
	respond(w, 200, map[string]any{"user": user, "categories": counts, "chartCount": chartCount, "scores": scores, "scoreReplayVersion": 1})
}
