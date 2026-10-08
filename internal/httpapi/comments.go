package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	maxCommentRunes   = 10000
	maxCommentDepth   = 999
	commentPageSize   = 20
	maxThreadComments = 1500
	// Comments one user may post per minute, against reply floods.
	commentBurst = 8
)

// Comment is one node of a thread. A comment deleted by its author or removed
// by an administrator keeps its place while replies remain, without author or text.
type Comment struct {
	ID              string     `json:"id"`
	ChartID         string     `json:"chartId"`
	ChartTitle      string     `json:"chartTitle,omitempty"`
	ParentID        *string    `json:"parentId"`
	Depth           int        `json:"depth"`
	AuthorID        string     `json:"authorId"`
	Author          string     `json:"author"`
	AuthorAvatarURL string     `json:"authorAvatarUrl"`
	Body            string     `json:"body"`
	CreatedAt       time.Time  `json:"createdAt"`
	EditedAt        *time.Time `json:"editedAt"`
	Deleted         bool       `json:"deleted"`
	Removed         bool       `json:"removed"`
	Score           int        `json:"score"`
	MyVote          int        `json:"myVote"`
	ReplyCount      int        `json:"replyCount"`
	Replies         []*Comment `json:"replies"`
}

// The viewer is always $1 so every query can report the viewer's own vote.
const commentColumns = `m.id,m.chart_id,m.parent_id,m.depth,m.author_id,m.body,m.created_at,m.edited_at,
 m.deleted_at IS NOT NULL,m.removed_at IS NOT NULL,m.upvotes-m.downvotes,m.reply_count,
 COALESCE((SELECT value FROM comment_votes v WHERE v.comment_id=m.id AND v.user_id=$1),0)`

var commentOrders = map[string]string{
	"best":          `comment_confidence(m.upvotes,m.downvotes) DESC,m.created_at DESC,m.id`,
	"top":           `m.upvotes-m.downvotes DESC,m.created_at DESC,m.id`,
	"new":           `m.created_at DESC,m.id`,
	"old":           `m.created_at,m.id`,
	"controversial": `comment_controversy(m.upvotes,m.downvotes) DESC,m.created_at DESC,m.id`,
}

func commentOrder(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	sort := r.URL.Query().Get("sort")
	if sort == "" {
		sort = "best"
	}
	order, ok := commentOrders[sort]
	if !ok {
		problem(w, 400, "QUERY_INVALID", "排序方式必须为 best / top / new / old / controversial")
	}
	return sort, order, ok
}

func queryPage(w http.ResponseWriter, r *http.Request) (int, bool) {
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		var e error
		page, e = strconv.Atoi(raw)
		if e != nil || page < 1 || page > 10000 {
			problem(w, 400, "QUERY_INVALID", "页码需要为 1–10000 的整数")
			return 0, false
		}
	}
	return page, true
}

type rowQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// readComments scans rows of commentColumns, optionally followed by the song title.
func readComments(ctx context.Context, db rowQuerier, withTitle bool, sql string, args ...any) ([]*Comment, error) {
	rows, e := db.Query(ctx, sql, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	items := []*Comment{}
	for rows.Next() {
		c := &Comment{Replies: []*Comment{}}
		dest := []any{&c.ID, &c.ChartID, &c.ParentID, &c.Depth, &c.AuthorID, &c.Body, &c.CreatedAt, &c.EditedAt, &c.Deleted, &c.Removed, &c.Score, &c.ReplyCount, &c.MyVote}
		if withTitle {
			dest = append(dest, &c.ChartTitle)
		}
		if e = rows.Scan(dest...); e != nil {
			return nil, e
		}
		if c.Deleted || c.Removed {
			c.AuthorID, c.Body = "", ""
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

// attach hangs descendants (ordered by depth, then rank) below the given nodes.
// A reply whose parent was cut off by the thread limit is left for its permalink.
func attach(roots []*Comment, descendants []*Comment) {
	nodes := map[string]*Comment{}
	for _, c := range roots {
		nodes[c.ID] = c
	}
	for _, c := range descendants {
		if parent := nodes[*c.ParentID]; parent != nil {
			parent.Replies = append(parent.Replies, c)
			nodes[c.ID] = c
		}
	}
}

// fillAuthors adds public nicknames and avatars to every node of the trees.
func (s *Server) fillAuthors(ctx context.Context, items []*Comment) {
	all := []*Comment{}
	var walk func([]*Comment)
	walk = func(list []*Comment) {
		for _, c := range list {
			all = append(all, c)
			walk(c.Replies)
		}
	}
	walk(items)
	ids := []string{}
	for _, c := range all {
		if c.AuthorID != "" {
			ids = append(ids, c.AuthorID)
		}
	}
	if len(ids) == 0 {
		return
	}
	profiles := s.publicProfiles(ctx, ids)
	for _, c := range all {
		if c.AuthorID != "" {
			p := profiles[c.AuthorID]
			c.Author, c.AuthorAvatarURL = p.Nickname, p.AvatarURL
		}
	}
}

// commentBody normalizes line endings and surrounding space and rejects empty,
// overlong or control-character text.
func commentBody(w http.ResponseWriter, raw string) (string, bool) {
	body := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n"))
	valid := body != "" && utf8.ValidString(body) && utf8.RuneCountInString(body) <= maxCommentRunes
	for _, c := range body {
		if valid && unicode.IsControl(c) && c != '\n' && c != '\t' {
			valid = false
		}
	}
	if !valid {
		problem(w, 400, "COMMENT_INVALID", "评论需为 1–10000 字的文本")
	}
	return body, valid
}

type commentState struct {
	id, chartID, authorID string
	parentID              *string
	depth, replies        int
	gone                  bool
}

// lockComment loads a comment of a published song with the given row lock.
func lockComment(w http.ResponseWriter, r *http.Request, tx pgx.Tx, lock string) (commentState, bool) {
	var c commentState
	e := tx.QueryRow(r.Context(), `SELECT m.id,m.chart_id,m.author_id,m.parent_id,m.depth,m.reply_count,m.deleted_at IS NOT NULL OR m.removed_at IS NOT NULL
	 FROM comments m JOIN charts c ON c.id=m.chart_id WHERE m.id=$1 AND `+publishedChart+` `+lock, r.PathValue("id")).
		Scan(&c.id, &c.chartID, &c.authorID, &c.parentID, &c.depth, &c.replies, &c.gone)
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, 404, "COMMENT_NOT_FOUND", "评论不存在或已删除")
		return c, false
	}
	if e != nil {
		internal(w, e)
		return c, false
	}
	return c, true
}

type CommentPage struct {
	Items        []*Comment `json:"items"`
	Total        int        `json:"total"`
	CommentCount int        `json:"commentCount"`
	Page         int        `json:"page"`
	PageSize     int        `json:"pageSize"`
	Sort         string     `json:"sort"`
}

// chartComments returns one page of top-level comments with their reply trees.
func (s *Server) chartComments(w http.ResponseWriter, r *http.Request) {
	sort, order, ok := commentOrder(w, r)
	if !ok {
		return
	}
	page, ok := queryPage(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	viewer := s.viewer(r)
	// One snapshot keeps the counts, the page and its replies consistent.
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		internal(w, e)
		return
	}
	defer tx.Rollback(ctx)
	result := CommentPage{Page: page, PageSize: commentPageSize, Sort: sort}
	var id string
	e = tx.QueryRow(ctx, `SELECT c.id,COALESCE((SELECT comment_count FROM chart_stats WHERE chart_id=c.id),0),
	 (SELECT count(*) FROM comments WHERE chart_id=c.id AND parent_id IS NULL)
	 FROM charts c WHERE c.id=$1 AND `+publishedChart, r.PathValue("id")).Scan(&id, &result.CommentCount, &result.Total)
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在或已删除")
		return
	}
	if e != nil {
		internal(w, e)
		return
	}
	result.Items, e = readComments(ctx, tx, false, `SELECT `+commentColumns+` FROM comments m
	 WHERE m.chart_id=$2 AND m.parent_id IS NULL ORDER BY `+order+` LIMIT $3 OFFSET $4`, viewer, id, commentPageSize, (page-1)*commentPageSize)
	if e != nil {
		internal(w, e)
		return
	}
	if len(result.Items) > 0 {
		roots := make([]string, len(result.Items))
		for i, c := range result.Items {
			roots[i] = c.ID
		}
		replies, e := readComments(ctx, tx, false, `SELECT `+commentColumns+` FROM comments m
		 WHERE m.root_id=ANY($2) AND m.parent_id IS NOT NULL ORDER BY m.depth,`+order+` LIMIT $3`, viewer, roots, maxThreadComments)
		if e != nil {
			internal(w, e)
			return
		}
		attach(result.Items, replies)
	}
	if e = tx.Commit(ctx); e != nil {
		internal(w, e)
		return
	}
	s.fillAuthors(ctx, result.Items)
	respond(w, 200, result)
}

// commentThread is a comment's permalink: the comment and all replies below it.
func (s *Server) commentThread(w http.ResponseWriter, r *http.Request) {
	sort, order, ok := commentOrder(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	viewer := s.viewer(r)
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		internal(w, e)
		return
	}
	defer tx.Rollback(ctx)
	items, e := readComments(ctx, tx, true, `SELECT `+commentColumns+`,c.title FROM comments m JOIN charts c ON c.id=m.chart_id
	 WHERE m.id=$2 AND `+publishedChart, viewer, r.PathValue("id"))
	if e != nil {
		internal(w, e)
		return
	}
	if len(items) == 0 {
		problem(w, 404, "COMMENT_NOT_FOUND", "评论不存在或已删除")
		return
	}
	replies, e := readComments(ctx, tx, false, `WITH RECURSIVE below(id) AS (
	 SELECT id FROM comments WHERE parent_id=$2
	 UNION ALL SELECT c.id FROM comments c JOIN below b ON c.parent_id=b.id)
	 SELECT `+commentColumns+` FROM comments m WHERE m.id IN (SELECT id FROM below) ORDER BY m.depth,`+order+` LIMIT $3`, viewer, items[0].ID, maxThreadComments)
	if e != nil {
		internal(w, e)
		return
	}
	if e = tx.Commit(ctx); e != nil {
		internal(w, e)
		return
	}
	attach(items, replies)
	s.fillAuthors(ctx, items)
	respond(w, 200, map[string]any{"item": items[0], "sort": sort})
}

type commentRequest struct {
	Body     string  `json:"body"`
	ParentID *string `json:"parentId"`
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
	if !ok {
		return
	}
	var req commentRequest
	if !decodeLimit(w, r, &req, 65536) {
		return
	}
	body, ok := commentBody(w, req.Body)
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
	var chartID, chartOwner string
	// FOR SHARE serializes with a song deletion, which locks the row FOR UPDATE.
	e = tx.QueryRow(ctx, `SELECT c.id,c.owner_id FROM charts c WHERE c.id=$1 AND `+publishedChart+` FOR SHARE`, r.PathValue("id")).Scan(&chartID, &chartOwner)
	if errors.Is(e, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在或已删除")
		return
	}
	if e != nil {
		internal(w, e)
		return
	}
	// Serialize each author's posts so the burst limit cannot be raced.
	if _, e = tx.Exec(ctx, `SELECT 1 FROM users WHERE id=$1 FOR UPDATE`, u.User.ID); e != nil {
		internal(w, e)
		return
	}
	var recent int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM comments WHERE author_id=$1 AND created_at>now()-interval '1 minute'`, u.User.ID).Scan(&recent); e != nil {
		internal(w, e)
		return
	}
	if recent >= commentBurst {
		w.Header().Set("Retry-After", "60")
		problem(w, 429, "RATE_LIMITED", "评论过于频繁，请稍后再试")
		return
	}
	id, depth, root := ID(), 0, ""
	// Top-level comments notify the song's uploader, replies the parent's author.
	notice := Notice{Recipient: chartOwner, Kind: "chart_comment", Actor: u.User.ID, ChartID: chartID, CommentID: id}
	if req.ParentID != nil {
		var parentChart, parentAuthor string
		var gone bool
		// The parent row is updated by the reply counter, so lock it exclusively.
		e = tx.QueryRow(ctx, `SELECT chart_id,author_id,root_id,depth,deleted_at IS NOT NULL OR removed_at IS NOT NULL FROM comments WHERE id=$1 FOR UPDATE`, *req.ParentID).Scan(&parentChart, &parentAuthor, &root, &depth, &gone)
		notice.Recipient, notice.Kind = parentAuthor, "comment_reply"
		if errors.Is(e, pgx.ErrNoRows) || (e == nil && parentChart != chartID) {
			problem(w, 404, "COMMENT_NOT_FOUND", "回复的评论不存在或已删除")
			return
		}
		if e != nil {
			internal(w, e)
			return
		}
		if gone {
			problem(w, 409, "COMMENT_DELETED", "不能回复已删除的评论")
			return
		}
		if depth >= maxCommentDepth {
			problem(w, 400, "COMMENT_TOO_DEEP", "回复层级过深")
			return
		}
		depth++
	} else {
		root = id
	}
	_, e = tx.Exec(ctx, `INSERT INTO comments(id,chart_id,parent_id,root_id,depth,author_id,body) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, chartID, req.ParentID, root, depth, u.User.ID, body)
	if e != nil {
		internal(w, e)
		return
	}
	// As on Reddit, an author's own comment starts upvoted by them.
	if _, e = tx.Exec(ctx, `INSERT INTO comment_votes(comment_id,user_id,value) VALUES($1,$2,1)`, id, u.User.ID); e != nil {
		internal(w, e)
		return
	}
	if e = notify(ctx, tx, notice); e != nil {
		internal(w, e)
		return
	}
	items, e := readComments(ctx, tx, false, `SELECT `+commentColumns+` FROM comments m WHERE m.id=$2`, u.User.ID, id)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		internal(w, e)
		return
	}
	items[0].Author, items[0].AuthorAvatarURL = u.User.Nickname, s.Config.SSO.avatarURL(u.User)
	respond(w, 201, items[0])
}

func (s *Server) editComment(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
	if !ok {
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if !decodeLimit(w, r, &req, 65536) {
		return
	}
	body, ok := commentBody(w, req.Body)
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
	if c.gone {
		problem(w, 409, "COMMENT_DELETED", "评论已删除")
		return
	}
	if c.authorID != u.User.ID {
		problem(w, 403, "FORBIDDEN", "只能编辑自己的评论")
		return
	}
	if _, e = tx.Exec(ctx, `UPDATE comments SET body=$2,edited_at=now() WHERE id=$1 AND body<>$2`, c.id, body); e != nil {
		internal(w, e)
		return
	}
	items, e := readComments(ctx, tx, false, `SELECT `+commentColumns+` FROM comments m WHERE m.id=$2`, u.User.ID, c.id)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		internal(w, e)
		return
	}
	items[0].Author, items[0].AuthorAvatarURL = u.User.Nickname, s.Config.SSO.avatarURL(u.User)
	respond(w, 200, items[0])
}

// deleteComment lets an author delete, or an administrator remove, a comment.
// Without replies the row disappears, together with any placeholder ancestors
// that only remained for this branch; otherwise it becomes a placeholder.
func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
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
	column := "deleted_at"
	if c.authorID != u.User.ID {
		if !u.User.IsAdmin {
			problem(w, 403, "FORBIDDEN", "只能删除自己的评论")
			return
		}
		column = "removed_at"
	}
	purged := false
	if c.gone {
		// Already a placeholder: repeating the request changes nothing.
	} else if c.replies > 0 {
		_, e = tx.Exec(ctx, `UPDATE comments SET body='',`+column+`=now() WHERE id=$1`, c.id)
		// A purged row takes its notices along; a placeholder drops them here.
		if e == nil {
			_, e = tx.Exec(ctx, `DELETE FROM notifications WHERE comment_id=$1`, c.id)
		}
	} else {
		purged = true
		_, e = tx.Exec(ctx, `DELETE FROM comments WHERE id=$1`, c.id)
		for parent := c.parentID; e == nil && parent != nil; {
			var next *string
			e = tx.QueryRow(ctx, `DELETE FROM comments WHERE id=$1 AND reply_count=0
			 AND (deleted_at IS NOT NULL OR removed_at IS NOT NULL) RETURNING parent_id`, *parent).Scan(&next)
			if errors.Is(e, pgx.ErrNoRows) {
				e = nil
				break
			}
			parent = next
		}
	}
	// The author learns of a removal, but not which administrator removed it.
	if e == nil && !c.gone && column == "removed_at" {
		e = notify(ctx, tx, Notice{Recipient: c.authorID, Kind: "comment_removed", ChartID: c.chartID, DedupeKey: "comment_removed:" + c.id})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		internal(w, e)
		return
	}
	respond(w, 200, map[string]bool{"ok": true, "purged": purged})
}

type CommentList struct {
	Items    []*Comment `json:"items"`
	Total    int        `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

// visibleComment selects live comments on published songs; m is the comment
// and c its song.
const visibleComment = `m.deleted_at IS NULL AND m.removed_at IS NULL AND ` + publishedChart

// userComments is a user's public comment history, newest or top first.
func (s *Server) userComments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validSubject(id) {
		problem(w, 404, "USER_NOT_FOUND", "用户不存在")
		return
	}
	order := "m.created_at DESC,m.id"
	switch r.URL.Query().Get("sort") {
	case "", "new":
	case "top":
		order = "m.upvotes-m.downvotes DESC,m.created_at DESC,m.id"
	default:
		problem(w, 400, "QUERY_INVALID", "排序方式必须为 new / top")
		return
	}
	page, ok := queryPage(w, r)
	if !ok {
		return
	}
	s.commentList(w, r, `m.author_id=$2`, order, page, id)
}

// commentList pages flat comments with their song titles; $2 is the filter's argument.
func (s *Server) commentList(w http.ResponseWriter, r *http.Request, filter, order string, page int, arg string) {
	ctx := r.Context()
	viewer := s.viewer(r)
	from := ` FROM comments m JOIN charts c ON c.id=m.chart_id LEFT JOIN comments p ON p.id=m.parent_id WHERE ` + filter + ` AND ` + visibleComment
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		internal(w, e)
		return
	}
	defer tx.Rollback(ctx)
	result := CommentList{Page: page, PageSize: commentPageSize}
	// The count does not report the viewer's votes, so the filter argument becomes $1.
	if e = tx.QueryRow(ctx, `SELECT count(*)`+strings.ReplaceAll(from, "$2", "$1"), arg).Scan(&result.Total); e != nil {
		internal(w, e)
		return
	}
	rows, e := tx.Query(ctx, `SELECT `+commentColumns+`,c.title`+from+` ORDER BY `+order+` LIMIT $3 OFFSET $4`, viewer, arg, commentPageSize, (page-1)*commentPageSize)
	if e != nil {
		internal(w, e)
		return
	}
	result.Items = []*Comment{}
	for rows.Next() {
		c := &Comment{Replies: []*Comment{}}
		if e = rows.Scan(&c.ID, &c.ChartID, &c.ParentID, &c.Depth, &c.AuthorID, &c.Body, &c.CreatedAt, &c.EditedAt, &c.Deleted, &c.Removed, &c.Score, &c.ReplyCount, &c.MyVote, &c.ChartTitle); e != nil {
			rows.Close()
			internal(w, e)
			return
		}
		result.Items = append(result.Items, c)
	}
	rows.Close()
	if e = rows.Err(); e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		internal(w, e)
		return
	}
	s.fillAuthors(ctx, result.Items)
	respond(w, 200, result)
}
