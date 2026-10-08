package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Notification kinds the server emits, in the order the settings page lists
// them. A new event registers its kind here and calls notify.
var notificationKinds = []string{
	"comment_reply",   // someone replied to the recipient's comment
	"chart_comment",   // someone commented on the recipient's song
	"chart_upvotes",   // the recipient's song reached an upvote milestone
	"comment_upvotes", // the recipient's comment reached an upvote milestone
	"comment_removed", // an administrator removed the recipient's comment
}

// Upvote counts that notify the author, as Reddit does, instead of every vote.
var upvoteMilestones = []int{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

// Notice is one event for one recipient. Empty IDs mean "not linked".
type Notice struct {
	Recipient, Kind, Actor, ChartID, CommentID string
	// DedupeKey makes the event notify the recipient at most once.
	DedupeKey string
	Data      map[string]any
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// notify records a notice in the caller's transaction. Nobody is notified
// about their own actions, and muted kinds are skipped.
func notify(ctx context.Context, db activityWriter, n Notice) error {
	if n.Recipient == "" || n.Recipient == n.Actor {
		return nil
	}
	if !slices.Contains(notificationKinds, n.Kind) {
		return errors.New("unregistered notification kind " + n.Kind)
	}
	data := n.Data
	if data == nil {
		data = map[string]any{}
	}
	raw, e := json.Marshal(data)
	if e != nil {
		return e
	}
	_, e = db.Exec(ctx, `INSERT INTO notifications(id,recipient_id,kind,actor_id,chart_id,comment_id,data,dedupe_key)
	 SELECT $1,$2,$3,$4,$5,$6,$7,$8 WHERE NOT EXISTS(SELECT 1 FROM notification_mutes WHERE user_id=$2 AND kind=$3)
	 ON CONFLICT(recipient_id,dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING`,
		ID(), n.Recipient, n.Kind, nullable(n.Actor), nullable(n.ChartID), nullable(n.CommentID), raw, nullable(n.DedupeKey))
	return e
}

// notifyUpvoteMilestone notifies an author when an upvote count lands exactly
// on a milestone. The dedupe key keeps a withdrawn and recast vote quiet.
func notifyUpvoteMilestone(ctx context.Context, db activityWriter, kind, recipient, chartID, commentID string, upvotes int) error {
	if !slices.Contains(upvoteMilestones, upvotes) {
		return nil
	}
	target := chartID
	if commentID != "" {
		target = commentID
	}
	return notify(ctx, db, Notice{Recipient: recipient, Kind: kind, ChartID: chartID, CommentID: commentID,
		DedupeKey: kind + ":" + target + ":" + strconv.Itoa(upvotes), Data: map[string]any{"upvotes": upvotes}})
}

type NotificationActor struct {
	ID        string `json:"id"`
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatarUrl"`
}
type NotificationChart struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type NotificationComment struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parentId"`
	Excerpt  string  `json:"excerpt"`
}
type Notification struct {
	ID        string               `json:"id"`
	Kind      string               `json:"kind"`
	CreatedAt time.Time            `json:"createdAt"`
	Read      bool                 `json:"read"`
	Actor     *NotificationActor   `json:"actor"`
	Chart     *NotificationChart   `json:"chart"`
	Comment   *NotificationComment `json:"comment"`
	Data      map[string]any       `json:"data"`
}
type NotificationPage struct {
	Items    []Notification `json:"items"`
	Total    int            `json:"total"`
	Unread   int            `json:"unread"`
	Page     int            `json:"page"`
	PageSize int            `json:"pageSize"`
}

const notificationPageSize = 20

// Notices about songs that are no longer public or comments that were deleted
// stay stored but are not shown; n is the notice, $1 the recipient.
const visibleNotification = `n.recipient_id=$1
 AND (n.chart_id IS NULL OR EXISTS(SELECT 1 FROM charts c WHERE c.id=n.chart_id AND ` + publishedChart + `))
 AND (n.comment_id IS NULL OR EXISTS(SELECT 1 FROM comments m WHERE m.id=n.comment_id AND m.deleted_at IS NULL AND m.removed_at IS NULL))`

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, false)
	if !ok {
		return
	}
	page, ok := queryPage(w, r)
	if !ok {
		return
	}
	filter := visibleNotification
	switch r.URL.Query().Get("filter") {
	case "", "all":
	case "unread":
		filter += ` AND n.read_at IS NULL`
	default:
		problem(w, 400, "QUERY_INVALID", "筛选必须为 all / unread")
		return
	}
	ctx := r.Context()
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		internal(w, e)
		return
	}
	defer tx.Rollback(ctx)
	result := NotificationPage{Items: []Notification{}, Page: page, PageSize: notificationPageSize}
	e = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE n.read_at IS NULL) FROM notifications n WHERE `+visibleNotification, u.User.ID).Scan(&result.Total, &result.Unread)
	if e == nil && filter != visibleNotification {
		result.Total = result.Unread
	}
	if e != nil {
		internal(w, e)
		return
	}
	rows, e := tx.Query(ctx, `SELECT n.id,n.kind,n.created_at,n.read_at IS NOT NULL,n.actor_id,n.chart_id,c.title,n.comment_id,m.parent_id,left(m.body,200),n.data
	 FROM notifications n LEFT JOIN charts c ON c.id=n.chart_id LEFT JOIN comments m ON m.id=n.comment_id
	 WHERE `+filter+` ORDER BY n.created_at DESC,n.id LIMIT $2 OFFSET $3`, u.User.ID, notificationPageSize, (page-1)*notificationPageSize)
	if e != nil {
		internal(w, e)
		return
	}
	actors := []string{}
	for rows.Next() {
		var n Notification
		var actor, chartID, chartTitle, commentID, parentID, excerpt *string
		if e = rows.Scan(&n.ID, &n.Kind, &n.CreatedAt, &n.Read, &actor, &chartID, &chartTitle, &commentID, &parentID, &excerpt, &n.Data); e != nil {
			rows.Close()
			internal(w, e)
			return
		}
		if actor != nil {
			n.Actor = &NotificationActor{ID: *actor}
			actors = append(actors, *actor)
		}
		if chartID != nil && chartTitle != nil {
			n.Chart = &NotificationChart{ID: *chartID, Title: *chartTitle}
		}
		if commentID != nil && excerpt != nil {
			n.Comment = &NotificationComment{ID: *commentID, ParentID: parentID, Excerpt: *excerpt}
		}
		result.Items = append(result.Items, n)
	}
	rows.Close()
	if e = rows.Err(); e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		internal(w, e)
		return
	}
	if len(actors) > 0 {
		profiles := s.publicProfiles(ctx, actors)
		for _, n := range result.Items {
			if n.Actor != nil {
				p := profiles[n.Actor.ID]
				n.Actor.Nickname, n.Actor.AvatarURL = p.Nickname, p.AvatarURL
			}
		}
	}
	respond(w, 200, result)
}

func (s *Server) unreadNotifications(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, false)
	if !ok {
		return
	}
	var count int
	if e := s.DB.QueryRow(r.Context(), `SELECT count(*) FROM notifications n WHERE `+visibleNotification+` AND n.read_at IS NULL`, u.User.ID).Scan(&count); e != nil {
		internal(w, e)
		return
	}
	respond(w, 200, map[string]int{"count": count})
}

// readAllNotifications marks the listed notices read, or all of them when the
// request names none.
func (s *Server) readAllNotifications(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
	if !ok {
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.IDs) > 100 {
		problem(w, 400, "REQUEST_INVALID", "一次最多标记 100 条通知")
		return
	}
	var e error
	if req.IDs == nil {
		_, e = s.DB.Exec(r.Context(), `UPDATE notifications SET read_at=now() WHERE recipient_id=$1 AND read_at IS NULL`, u.User.ID)
	} else {
		_, e = s.DB.Exec(r.Context(), `UPDATE notifications SET read_at=now() WHERE recipient_id=$1 AND read_at IS NULL AND id=ANY($2)`, u.User.ID, req.IDs)
	}
	if e != nil {
		internal(w, e)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (s *Server) markNotification(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
	if !ok {
		return
	}
	var req struct {
		Read *bool `json:"read"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Read == nil {
		problem(w, 400, "REQUEST_INVALID", "缺少 read")
		return
	}
	tag, e := s.DB.Exec(r.Context(), `UPDATE notifications SET read_at=CASE WHEN $3 THEN COALESCE(read_at,now()) END WHERE id=$1 AND recipient_id=$2`, r.PathValue("id"), u.User.ID, *req.Read)
	if e != nil {
		internal(w, e)
		return
	}
	if tag.RowsAffected() == 0 {
		problem(w, 404, "NOTIFICATION_NOT_FOUND", "通知不存在")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (s *Server) dismissNotification(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
	if !ok {
		return
	}
	tag, e := s.DB.Exec(r.Context(), `DELETE FROM notifications WHERE id=$1 AND recipient_id=$2`, r.PathValue("id"), u.User.ID)
	if e != nil {
		internal(w, e)
		return
	}
	if tag.RowsAffected() == 0 {
		problem(w, 404, "NOTIFICATION_NOT_FOUND", "通知不存在")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

type notificationSetting struct {
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
}

func (s *Server) writeNotificationSettings(w http.ResponseWriter, r *http.Request, user string) {
	rows, e := s.DB.Query(r.Context(), `SELECT kind FROM notification_mutes WHERE user_id=$1`, user)
	if e != nil {
		internal(w, e)
		return
	}
	muted, e := pgx.CollectRows(rows, pgx.RowTo[string])
	if e != nil {
		internal(w, e)
		return
	}
	items := make([]notificationSetting, len(notificationKinds))
	for i, kind := range notificationKinds {
		items[i] = notificationSetting{kind, !slices.Contains(muted, kind)}
	}
	respond(w, 200, map[string]any{"items": items})
}

func (s *Server) notificationSettings(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, false)
	if ok {
		s.writeNotificationSettings(w, r, u.User.ID)
	}
}

// updateNotificationSettings takes {"<kind>": true|false, ...}; omitted kinds keep
// their setting. Turning a kind off stops new notices; existing ones remain.
func (s *Server) updateNotificationSettings(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
	if !ok {
		return
	}
	var req map[string]bool
	if !decode(w, r, &req) {
		return
	}
	for kind := range req {
		if !slices.Contains(notificationKinds, kind) {
			problem(w, 400, "NOTIFICATION_KIND_INVALID", "未知的通知类型")
			return
		}
	}
	ctx := r.Context()
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		internal(w, e)
		return
	}
	defer tx.Rollback(ctx)
	for kind, enabled := range req {
		if enabled {
			_, e = tx.Exec(ctx, `DELETE FROM notification_mutes WHERE user_id=$1 AND kind=$2`, u.User.ID, kind)
		} else {
			_, e = tx.Exec(ctx, `INSERT INTO notification_mutes(user_id,kind) VALUES($1,$2) ON CONFLICT DO NOTHING`, u.User.ID, kind)
		}
		if e != nil {
			internal(w, e)
			return
		}
	}
	if e = tx.Commit(ctx); e != nil {
		internal(w, e)
		return
	}
	s.writeNotificationSettings(w, r, u.User.ID)
}

// PruneNotifications drops read notices after 90 days and unread ones after a year.
func (s *Server) PruneNotifications(ctx context.Context) error {
	_, e := s.DB.Exec(ctx, `DELETE FROM notifications WHERE (read_at IS NOT NULL AND read_at<now()-interval '90 days') OR created_at<now()-interval '365 days'`)
	return e
}

func (s *Server) RunNotificationPrune(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		if err := s.PruneNotifications(ctx); err != nil && ctx.Err() == nil {
			log.Printf("notification prune pending: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
