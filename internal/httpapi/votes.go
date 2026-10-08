package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

type voteRequest struct {
	Value *int `json:"value"`
}

// VoteResult is the target's tally after the viewer's vote was applied.
type VoteResult struct {
	Score int `json:"score"`
	// Only songs publish their separate counts; comments show the score alone.
	Upvotes   *int `json:"upvotes,omitempty"`
	Downvotes *int `json:"downvotes,omitempty"`
	MyVote    int  `json:"myVote"`
}

// viewer identifies the browser session for read-only personalization such as
// showing the viewer's own votes. It never contacts SSO and never grants access.
func (s *Server) viewer(r *http.Request) string {
	v, e := s.localSession(r)
	if e != nil {
		return ""
	}
	return v.User.ID
}

func readVote(w http.ResponseWriter, r *http.Request) (int, bool) {
	var body voteRequest
	if !decode(w, r, &body) {
		return 0, false
	}
	if body.Value == nil || *body.Value < -1 || *body.Value > 1 {
		problem(w, 400, "VOTE_INVALID", "投票值必须为 1、0 或 -1")
		return 0, false
	}
	return *body.Value, true
}

// applyVote sets, changes or withdraws (value 0) one user's vote on a row.
func applyVote(ctx context.Context, tx pgx.Tx, table, column, target, user string, value int) error {
	var e error
	if value == 0 {
		_, e = tx.Exec(ctx, `DELETE FROM `+table+` WHERE `+column+`=$1 AND user_id=$2`, target, user)
	} else {
		_, e = tx.Exec(ctx, `INSERT INTO `+table+`(`+column+`,user_id,value) VALUES($1,$2,$3)
		 ON CONFLICT(`+column+`,user_id) DO UPDATE SET value=EXCLUDED.value,voted_at=now() WHERE `+table+`.value<>EXCLUDED.value`, target, user, value)
	}
	return e
}

func (s *Server) voteChart(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
	if !ok {
		return
	}
	value, ok := readVote(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		internal(w, e)
		return
	}
	defer tx.Rollback(ctx)
	// Holding the song row FOR SHARE serializes the vote with a deletion.
	var id, owner string
	e = tx.QueryRow(ctx, `SELECT c.id,c.owner_id FROM charts c WHERE c.id=$1 AND `+publishedChart+` FOR SHARE`, r.PathValue("id")).Scan(&id, &owner)
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在或已删除")
		return
	}
	if e != nil {
		internal(w, e)
		return
	}
	if e = applyVote(ctx, tx, "chart_votes", "chart_id", id, u.User.ID, value); e != nil {
		internal(w, e)
		return
	}
	result := VoteResult{MyVote: value, Upvotes: new(int), Downvotes: new(int)}
	e = tx.QueryRow(ctx, `SELECT COALESCE((SELECT upvotes FROM chart_stats WHERE chart_id=$1),0),COALESCE((SELECT downvotes FROM chart_stats WHERE chart_id=$1),0)`, id).Scan(result.Upvotes, result.Downvotes)
	if e == nil && value == 1 && owner != u.User.ID {
		e = notifyUpvoteMilestone(ctx, tx, "chart_upvotes", owner, id, "", *result.Upvotes)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		internal(w, e)
		return
	}
	result.Score = *result.Upvotes - *result.Downvotes
	respond(w, 200, result)
}

func (s *Server) voteComment(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
	if !ok {
		return
	}
	value, ok := readVote(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		internal(w, e)
		return
	}
	defer tx.Rollback(ctx)
	c, ok := lockComment(w, r, tx, "FOR UPDATE OF m")
	if !ok {
		return
	}
	// Withdrawing a vote stays possible after a comment was deleted.
	if value != 0 && c.gone {
		problem(w, 409, "COMMENT_DELETED", "评论已删除")
		return
	}
	if e = applyVote(ctx, tx, "comment_votes", "comment_id", c.id, u.User.ID, value); e != nil {
		internal(w, e)
		return
	}
	result := VoteResult{MyVote: value}
	var upvotes int
	e = tx.QueryRow(ctx, `SELECT upvotes-downvotes,upvotes FROM comments WHERE id=$1`, c.id).Scan(&result.Score, &upvotes)
	if e == nil && value == 1 && c.authorID != u.User.ID {
		e = notifyUpvoteMilestone(ctx, tx, "comment_upvotes", c.authorID, c.chartID, c.id, upvotes)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		internal(w, e)
		return
	}
	respond(w, 200, result)
}
