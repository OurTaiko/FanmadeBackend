package httpapi

import (
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Native routes never consume browser cookies. Their bearer tokens use a
// separate hash domain, so neither session type grants access to the other.
func isGameRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/v1/game/")
}

func (s *Server) gameBootstrap(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, false)
	if !ok {
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), chartSelect+` WHERE c.status='published' ORDER BY c.id`)
	if err != nil {
		internal(w, err)
		return
	}
	charts := []Chart{}
	for rows.Next() {
		c, e := readChart(rows)
		if e != nil {
			rows.Close()
			internal(w, e)
			return
		}
		charts = append(charts, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		internal(w, err)
		return
	}
	rows, err = tx.Query(r.Context(), `SELECT `+scoreColumns+` FROM scores WHERE user_id=$1 ORDER BY submitted_at,id`, u.User.ID)
	if err != nil {
		internal(w, err)
		return
	}
	scores := []Score{}
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
	if err = tx.Commit(r.Context()); err != nil {
		internal(w, err)
		return
	}
	respond(w, 200, map[string]any{"user": u.User, "charts": charts, "scores": scores})
}
