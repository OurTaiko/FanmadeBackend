package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

type interactionClient struct {
	t       *testing.T
	handler http.Handler
	tokens  map[string]string
}

// do sends a request as the given user ("" for a guest) and decodes the JSON reply.
func (c interactionClient) do(user, method, path, body string, out any) int {
	c.t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if method != "GET" {
		r.Header.Set("Origin", "http://localhost")
		r.Header.Set("X-CSRF-Token", "csrf")
	}
	if user != "" {
		r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: c.tokens[user]})
	}
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	if out != nil && w.Code < 300 {
		if e := json.Unmarshal(w.Body.Bytes(), out); e != nil {
			c.t.Fatal(path, e, w.Body.String())
		}
	}
	return w.Code
}

func interactionFixture(t *testing.T) (interactionClient, *Server) {
	t.Helper()
	pool := scoreTestDB(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash,is_admin) VALUES
	 ('d46774d30dd13b92d9e536808da468a4','owner','unused',false),
	 ('9b893bc6d9422c93536ff0df503b81e9','alice','unused',false),
	 ('5f0c4a0b0e7b4d3c9a6f1e2d3c4b5a69','bob','unused',false),
	 ('0a1b2c3d4e5f60718293a4b5c6d7e8f9','admin','unused',true)`); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `BEGIN;
	 INSERT INTO charts(id,owner_id,title,bpm,duration,wave_filename,difficulties,created_at) VALUES
	  ('song','d46774d30dd13b92d9e536808da468a4','Song',120,10,'a.ogg','[{"course":"Oni","level":5,"maker":""}]',now()-interval '2 days'),
	  ('other','d46774d30dd13b92d9e536808da468a4','Other',120,10,'a.ogg','[{"course":"Oni","level":5,"maker":""}]',now());
	 INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) VALUES
	  ('song','tja','song/tja','a.tja',repeat('a',64),14,'text/plain'),('song','audio','song/audio','a.ogg',repeat('a',64),14,'audio/ogg'),
	  ('other','tja','other/tja','a.tja',repeat('a',64),14,'text/plain'),('other','audio','other/audio','a.ogg',repeat('a',64),14,'audio/ogg');
	 COMMIT;`)
	if err != nil {
		t.Fatal(err)
	}
	s := testServer(t, pool, Config{Storage: t.TempDir(), Origin: "http://localhost"})
	tokens := map[string]string{}
	for name, id := range map[string]string{"owner": "d46774d30dd13b92d9e536808da468a4", "alice": "9b893bc6d9422c93536ff0df503b81e9", "bob": "5f0c4a0b0e7b4d3c9a6f1e2d3c4b5a69", "admin": "0a1b2c3d4e5f60718293a4b5c6d7e8f9"} {
		tokens[name] = seedWebSession(t, s, id)
	}
	return interactionClient{t, s.Handler(), tokens}, s
}

func TestChartVotes(t *testing.T) {
	c, _ := interactionFixture(t)
	var v VoteResult
	if code := c.do("", "PUT", "/api/v1/charts/song/vote", `{"value":1}`, nil); code != 401 {
		t.Fatal("guest vote", code)
	}
	for _, body := range []string{`{"value":2}`, `{}`, `{"value":"1"}`} {
		if code := c.do("alice", "PUT", "/api/v1/charts/song/vote", body, nil); code != 400 {
			t.Fatal("invalid vote accepted", body, code)
		}
	}
	if code := c.do("alice", "PUT", "/api/v1/charts/missing/vote", `{"value":1}`, nil); code != 404 {
		t.Fatal("missing chart", code)
	}
	c.do("alice", "PUT", "/api/v1/charts/song/vote", `{"value":1}`, &v)
	c.do("alice", "PUT", "/api/v1/charts/song/vote", `{"value":1}`, &v)
	c.do("bob", "PUT", "/api/v1/charts/song/vote", `{"value":1}`, &v)
	if v.Score != 2 || *v.Upvotes != 2 || v.MyVote != 1 {
		t.Fatalf("repeated vote counted twice: %+v", v)
	}
	c.do("bob", "PUT", "/api/v1/charts/song/vote", `{"value":-1}`, &v)
	if v.Score != 0 || *v.Upvotes != 1 || *v.Downvotes != 1 || v.MyVote != -1 {
		t.Fatalf("changed vote: %+v", v)
	}
	c.do("bob", "PUT", "/api/v1/charts/song/vote", `{"value":0}`, &v)
	if v.Score != 1 || *v.Downvotes != 0 || v.MyVote != 0 {
		t.Fatalf("withdrawn vote: %+v", v)
	}
	var chart Chart
	c.do("alice", "GET", "/api/v1/charts/song", "", &chart)
	if chart.Score != 1 || chart.MyVote != 1 || chart.Upvotes != 1 {
		t.Fatalf("detail: %+v %+v", chart.Score, chart.MyVote)
	}
	c.do("", "GET", "/api/v1/charts/song", "", &chart)
	if chart.MyVote != 0 || chart.Score != 1 {
		t.Fatal("guest detail", chart.MyVote)
	}
	// "top" puts the voted older song first; the default order is newest first.
	for order, first := range map[string]string{"": "other", "top": "song", "hot": "other", "comments": "other"} {
		var list struct{ Items []Chart }
		if code := c.do("", "GET", "/api/v1/charts?order="+order, "", &list); code != 200 || len(list.Items) != 2 || list.Items[0].ID != first {
			t.Fatal("order", order, code, list.Items)
		}
	}
}

func TestCommentThreads(t *testing.T) {
	c, s := interactionFixture(t)
	ctx := context.Background()
	post := func(user, chart, body string, parent string) Comment {
		t.Helper()
		payload := map[string]any{"body": body}
		if parent != "" {
			payload["parentId"] = parent
		}
		raw, _ := json.Marshal(payload)
		var out Comment
		if code := c.do(user, "POST", "/api/v1/charts/"+chart+"/comments", string(raw), &out); code != 201 {
			t.Fatal("post", body, code)
		}
		return out
	}
	if code := c.do("", "POST", "/api/v1/charts/song/comments", `{"body":"hi"}`, nil); code != 401 {
		t.Fatal("guest comment", code)
	}
	for _, body := range []string{`{"body":"   "}`, `{"body":"a\u0000b"}`, `{"body":"` + strings.Repeat("长", 10001) + `"}`} {
		if code := c.do("alice", "POST", "/api/v1/charts/song/comments", body, nil); code != 400 {
			t.Fatal("invalid body accepted", len(body), code)
		}
	}
	long := post("alice", "song", strings.Repeat("长", 10000), "")
	if long.Score != 1 || long.MyVote != 1 || long.Author != "alice" {
		t.Fatalf("own comment not self-upvoted: %+v", long)
	}
	top := post("alice", "song", " First\r\nline ", "")
	if top.Body != "First\nline" || top.Depth != 0 {
		t.Fatalf("body not normalized: %q", top.Body)
	}
	reply := post("bob", "song", "reply", top.ID)
	deep := post("owner", "song", "deeper", reply.ID)
	if deep.Depth != 2 || *deep.ParentID != reply.ID {
		t.Fatalf("nested reply: %+v", deep)
	}
	if code := c.do("bob", "POST", "/api/v1/charts/other/comments", `{"body":"x","parentId":"`+top.ID+`"}`, nil); code != 404 {
		t.Fatal("reply across songs", code)
	}
	// Bob downvotes the long comment so "best" ranks the first comment ahead.
	var v VoteResult
	c.do("bob", "PUT", "/api/v1/comments/"+long.ID+"/vote", `{"value":-1}`, &v)
	if v.Upvotes != nil {
		t.Fatal("comment vote published separate counts")
	}
	c.do("owner", "PUT", "/api/v1/comments/"+top.ID+"/vote", `{"value":1}`, &v)
	if v.Score != 2 || v.MyVote != 1 {
		t.Fatalf("comment vote: %+v", v)
	}
	var page CommentPage
	c.do("bob", "GET", "/api/v1/charts/song/comments", "", &page)
	if page.Total != 2 || page.CommentCount != 4 || len(page.Items) != 2 || page.Items[0].ID != top.ID {
		t.Fatalf("best page: %+v", page)
	}
	if got := page.Items[0]; len(got.Replies) != 1 || got.Replies[0].ID != reply.ID || len(got.Replies[0].Replies) != 1 || got.Replies[0].Replies[0].Author != "owner" || got.Replies[0].MyVote != 1 {
		t.Fatalf("tree: %+v", got.Replies)
	}
	if page.Items[1].MyVote != -1 {
		t.Fatal("viewer's downvote not reported")
	}
	for sort, first := range map[string]string{"new": top.ID, "old": long.ID, "top": top.ID, "controversial": long.ID} {
		c.do("", "GET", "/api/v1/charts/song/comments?sort="+sort, "", &page)
		if page.Items[0].ID != first {
			t.Fatal("sort", sort)
		}
	}
	if code := c.do("", "GET", "/api/v1/charts/song/comments?sort=hot", "", nil); code != 400 {
		t.Fatal("unknown sort", code)
	}
	var chart Chart
	c.do("", "GET", "/api/v1/charts/song", "", &chart)
	if chart.CommentCount != 4 {
		t.Fatal("chart comment count", chart.CommentCount)
	}

	// Permalink of the middle reply contains its subtree only.
	var thread struct{ Item Comment }
	c.do("", "GET", "/api/v1/comments/"+reply.ID, "", &thread)
	if thread.Item.ID != reply.ID || thread.Item.ChartTitle != "Song" || len(thread.Item.Replies) != 1 || thread.Item.Replies[0].ID != deep.ID {
		t.Fatalf("permalink: %+v", thread.Item)
	}

	// Editing: only the author, and the edit is marked.
	if code := c.do("alice", "PATCH", "/api/v1/comments/"+reply.ID, `{"body":"hijack"}`, nil); code != 403 {
		t.Fatal("edit by another user", code)
	}
	var edited Comment
	c.do("bob", "PATCH", "/api/v1/comments/"+reply.ID, `{"body":"reply, edited"}`, &edited)
	if edited.Body != "reply, edited" || edited.EditedAt == nil {
		t.Fatalf("edit: %+v", edited)
	}

	// Notifications: alice got bob's reply; owner got alice's top-level comments.
	var unread map[string]int
	c.do("alice", "GET", "/api/v1/me/notifications/unread", "", &unread)
	if unread["count"] != 1 {
		t.Fatal("alice unread", unread)
	}
	var notes NotificationPage
	c.do("owner", "GET", "/api/v1/me/notifications", "", &notes)
	if notes.Total != 2 || notes.Unread != 2 || notes.Items[0].Kind != "chart_comment" || notes.Items[0].Read ||
		notes.Items[0].Actor == nil || notes.Items[0].Actor.Nickname != "alice" || notes.Items[0].Chart.Title != "Song" || notes.Items[0].Comment.ID != top.ID {
		t.Fatalf("owner notifications: %+v", notes)
	}
	if code := c.do("owner", "POST", "/api/v1/me/notifications/read", `{}`, nil); code != 200 {
		t.Fatal("mark all read", code)
	}
	c.do("owner", "GET", "/api/v1/me/notifications/unread", "", &unread)
	if unread["count"] != 0 {
		t.Fatal("still unread", unread)
	}

	// Deleting a comment with replies keeps an anonymous placeholder.
	if code := c.do("alice", "DELETE", "/api/v1/comments/"+reply.ID, "", nil); code != 403 {
		t.Fatal("delete by another user", code)
	}
	c.do("bob", "DELETE", "/api/v1/comments/"+reply.ID, "", nil)
	c.do("", "GET", "/api/v1/charts/song/comments?sort=new", "", &page)
	placeholder := page.Items[0].Replies[0]
	if !placeholder.Deleted || placeholder.Body != "" || placeholder.AuthorID != "" || placeholder.Author != "" || len(placeholder.Replies) != 1 {
		t.Fatalf("placeholder: %+v", placeholder)
	}
	if code := c.do("alice", "POST", "/api/v1/charts/song/comments", `{"body":"x","parentId":"`+reply.ID+`"}`, nil); code != 409 {
		t.Fatal("reply to deleted comment", code)
	}
	if code := c.do("alice", "PUT", "/api/v1/comments/"+reply.ID+"/vote", `{"value":1}`, nil); code != 409 {
		t.Fatal("vote on deleted comment", code)
	}
	// The deleted reply no longer notifies alice.
	c.do("alice", "GET", "/api/v1/me/notifications/unread", "", &unread)
	if unread["count"] != 0 {
		t.Fatal("notice of a deleted reply", unread)
	}
	// An administrator removes the last reply; the empty placeholder above goes too.
	var removed map[string]bool
	c.do("admin", "DELETE", "/api/v1/comments/"+deep.ID, "", &removed)
	if !removed["purged"] {
		t.Fatal("leaf not purged")
	}
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM comments WHERE id=ANY($1)`, []string{reply.ID, deep.ID}).Scan(&n); err != nil || n != 0 {
		t.Fatal("placeholder chain kept", n, err)
	}
	c.do("", "GET", "/api/v1/charts/song/comments", "", &page)
	if page.CommentCount != 2 || page.Items[0].ReplyCount != 0 {
		t.Fatalf("counters after purge: %+v", page)
	}
	// Removal by an administrator keeps replies under a "removed" placeholder.
	post("bob", "song", "under", long.ID)
	c.do("admin", "DELETE", "/api/v1/comments/"+long.ID, "", nil)
	c.do("alice", "GET", "/api/v1/me/notifications?filter=unread", "", &notes)
	// Newest first: the removal, then bob's reply that preceded it.
	if notes.Total != 2 || notes.Items[0].Kind != "comment_removed" || notes.Items[0].Actor != nil || notes.Items[0].Chart.ID != "song" || notes.Items[1].Kind != "comment_reply" {
		t.Fatalf("removal notice: %+v", notes)
	}
	c.do("", "GET", "/api/v1/charts/song/comments?sort=old", "", &page)
	if !page.Items[0].Removed || page.Items[0].Body != "" {
		t.Fatalf("removed placeholder: %+v", page.Items[0])
	}

	// History lists live comments only. Karma keeps every vote from others: the
	// owner's upvote on the live comment and bob's downvote on the removed one.
	var history CommentList
	c.do("", "GET", "/api/v1/users/9b893bc6d9422c93536ff0df503b81e9/comments", "", &history)
	if history.Total != 1 || history.Items[0].ID != top.ID {
		t.Fatalf("history: %+v", history)
	}
	var space UserSpace
	c.do("", "GET", "/api/v1/users/9b893bc6d9422c93536ff0df503b81e9", "", &space)
	if space.User.CommentCount != 1 || space.User.Karma != 0 {
		t.Fatalf("profile: %+v", space.User)
	}

	// Deleting the song cascades through comments and votes.
	if code := c.do("owner", "DELETE", "/api/v1/charts/song", "", nil); code != 200 {
		t.Fatal("delete song", code)
	}
	for _, table := range []string{"comments", "comment_votes", "chart_votes", "chart_stats"} {
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatal(table, "left after song deletion", n, err)
		}
	}
}

func TestCommentBurstLimit(t *testing.T) {
	c, _ := interactionFixture(t)
	for i := 0; i < commentBurst; i++ {
		if code := c.do("alice", "POST", "/api/v1/charts/song/comments", `{"body":"spam"}`, nil); code != 201 {
			t.Fatal("comment", i, code)
		}
	}
	if code := c.do("alice", "POST", "/api/v1/charts/song/comments", `{"body":"spam"}`, nil); code != 429 {
		t.Fatal("burst not limited", code)
	}
}

// Concurrent votes on the same song and comment neither deadlock nor drift.
func TestConcurrentVotes(t *testing.T) {
	c, s := interactionFixture(t)
	var comment Comment
	if code := c.do("owner", "POST", "/api/v1/charts/song/comments", `{"body":"vote here"}`, &comment); code != 201 {
		t.Fatal("comment", code)
	}
	users := []string{"owner", "alice", "bob", "admin"}
	var wg sync.WaitGroup
	codes := make(chan int, 64)
	for _, user := range users {
		for _, path := range []string{"/api/v1/charts/song/vote", "/api/v1/comments/" + comment.ID + "/vote"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for _, value := range []string{"1", "-1", "0", "-1"} {
					codes <- c.do(user, "PUT", path, `{"value":`+value+`}`, nil)
				}
			}()
		}
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatal("concurrent vote", code)
		}
	}
	var up, down, cup, cdown int
	ctx := context.Background()
	if err := s.DB.QueryRow(ctx, `SELECT upvotes,downvotes FROM chart_stats WHERE chart_id='song'`).Scan(&up, &down); err != nil || up != 0 || down != 4 {
		t.Fatal("song tally", up, down, err)
	}
	if err := s.DB.QueryRow(ctx, `SELECT upvotes,downvotes FROM comments WHERE id=$1`, comment.ID).Scan(&cup, &cdown); err != nil || cup != 0 || cdown != 4 {
		t.Fatal("comment tally", cup, cdown, err)
	}
}

func TestNotificationCenter(t *testing.T) {
	c, s := interactionFixture(t)
	ctx := context.Background()
	var comment Comment
	c.do("alice", "POST", "/api/v1/charts/song/comments", `{"body":"hello"}`, &comment)
	var notes NotificationPage
	c.do("owner", "GET", "/api/v1/me/notifications", "", &notes)
	if len(notes.Items) != 1 {
		t.Fatal("no notice", notes)
	}
	id := notes.Items[0].ID
	path := "/api/v1/me/notifications/" + id
	// Only the recipient can see or change a notice.
	if code := c.do("bob", "PATCH", path, `{"read":true}`, nil); code != 404 {
		t.Fatal("foreign mark", code)
	}
	if code := c.do("bob", "DELETE", path, "", nil); code != 404 {
		t.Fatal("foreign dismiss", code)
	}
	c.do("owner", "PATCH", path, `{"read":true}`, nil)
	c.do("owner", "GET", "/api/v1/me/notifications?filter=unread", "", &notes)
	if notes.Total != 0 || notes.Unread != 0 {
		t.Fatal("read notice listed as unread", notes)
	}
	c.do("owner", "PATCH", path, `{"read":false}`, nil)
	c.do("owner", "GET", "/api/v1/me/notifications?filter=unread", "", &notes)
	if notes.Total != 1 {
		t.Fatal("marked unread again", notes)
	}
	if code := c.do("owner", "DELETE", path, "", nil); code != 200 {
		t.Fatal("dismiss", code)
	}
	c.do("owner", "GET", "/api/v1/me/notifications", "", &notes)
	if notes.Total != 0 {
		t.Fatal("dismissed notice listed", notes)
	}

	// Muting a kind stops new notices of that kind only.
	if code := c.do("owner", "PUT", "/api/v1/me/notification-settings", `{"nope":false}`, nil); code != 400 {
		t.Fatal("unknown kind accepted", code)
	}
	var settings struct{ Items []notificationSetting }
	c.do("owner", "PUT", "/api/v1/me/notification-settings", `{"chart_comment":false}`, &settings)
	if len(settings.Items) != len(notificationKinds) || settings.Items[1].Kind != "chart_comment" || settings.Items[1].Enabled || !settings.Items[0].Enabled {
		t.Fatalf("settings: %+v", settings)
	}
	c.do("bob", "POST", "/api/v1/charts/song/comments", `{"body":"muted"}`, nil)
	c.do("owner", "GET", "/api/v1/me/notifications", "", &notes)
	if notes.Total != 0 {
		t.Fatal("muted kind delivered", notes)
	}
	c.do("owner", "PUT", "/api/v1/me/notification-settings", `{"chart_comment":true}`, &settings)
	if !settings.Items[1].Enabled {
		t.Fatal("not re-enabled")
	}

	// The fifth upvote (the author's own counts as on Reddit) notifies once,
	// even when a vote is withdrawn and cast again.
	if _, err := s.DB.Exec(ctx, `INSERT INTO users(id) VALUES('77777777777777777777777777777777')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO comment_votes(comment_id,user_id,value) VALUES($1,'77777777777777777777777777777777',1)`, comment.ID); err != nil {
		t.Fatal(err)
	}
	// alice (self) and the extra user give 2; owner, bob and admin reach 5;
	// bob then withdraws and recasts, touching 5 again.
	for _, vote := range []struct{ user, value string }{{"owner", "1"}, {"bob", "1"}, {"admin", "1"}, {"bob", "0"}, {"bob", "1"}} {
		if code := c.do(vote.user, "PUT", "/api/v1/comments/"+comment.ID+"/vote", `{"value":`+vote.value+`}`, nil); code != 200 {
			t.Fatal("vote", code)
		}
	}
	c.do("alice", "GET", "/api/v1/me/notifications", "", &notes)
	if notes.Total != 1 || notes.Items[0].Kind != "comment_upvotes" || notes.Items[0].Data["upvotes"] != float64(5) || notes.Items[0].Comment.Excerpt != "hello" {
		t.Fatalf("milestone: %+v", notes)
	}

	// Hiding the song hides its notices; deleting the song removes them.
	if _, err := s.DB.Exec(ctx, `UPDATE charts SET status='hidden' WHERE id='song'`); err != nil {
		t.Fatal(err)
	}
	var unread map[string]int
	c.do("alice", "GET", "/api/v1/me/notifications/unread", "", &unread)
	if unread["count"] != 0 {
		t.Fatal("notice of a hidden song", unread)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE charts SET status='published' WHERE id='song'`); err != nil {
		t.Fatal(err)
	}
	c.do("owner", "DELETE", "/api/v1/charts/song", "", nil)
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&n); err != nil || n != 0 {
		t.Fatal("notices left after song deletion", n, err)
	}
}

func TestNotificationPrune(t *testing.T) {
	_, s := interactionFixture(t)
	ctx := context.Background()
	_, err := s.DB.Exec(ctx, `INSERT INTO notifications(id,recipient_id,kind,created_at,read_at) VALUES
	 ('old-read','9b893bc6d9422c93536ff0df503b81e9','chart_upvotes',now()-interval '100 days',now()-interval '91 days'),
	 ('recent-read','9b893bc6d9422c93536ff0df503b81e9','chart_upvotes',now()-interval '100 days',now()-interval '10 days'),
	 ('old-unread','9b893bc6d9422c93536ff0df503b81e9','chart_upvotes',now()-interval '400 days',NULL),
	 ('unread','9b893bc6d9422c93536ff0df503b81e9','chart_upvotes',now()-interval '200 days',NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PruneNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.DB.Query(ctx, `SELECT id FROM notifications ORDER BY id`)
	kept, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || strings.Join(kept, ",") != "recent-read,unread" {
		t.Fatal("pruned", kept, err)
	}
}
