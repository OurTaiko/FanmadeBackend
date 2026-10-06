package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"ourtaiko.dev/fanmade/api/internal/audio"
	"ourtaiko.dev/fanmade/api/internal/tja"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

type metadataPatch struct {
	Description          json.RawMessage `json:"description"`
	DifficultyMakers     json.RawMessage `json:"difficultyMakers"`
	DemoStart            json.RawMessage `json:"demoStart"`
	DemoEnd              json.RawMessage `json:"demoEnd"`
	CategoryIDs          json.RawMessage `json:"categoryIds"`
	Title                json.RawMessage `json:"title"`
	Subtitle             json.RawMessage `json:"subtitle"`
	TitleTranslations    json.RawMessage `json:"titleTranslations"`
	SubtitleTranslations json.RawMessage `json:"subtitleTranslations"`
}
type metadataTranslations struct{ Titles, Subtitles map[string]string }

func validDisplayText(s string, title bool) bool {
	if len(s) > 500 || (title && strings.TrimSpace(s) == "") {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// null restores the corresponding value from the original TJA. Dictionaries
// themselves contain authoritative translations, not an overlay layer.
func patchTranslations(raw json.RawMessage, dst *map[string]string, original map[string]string, title bool) bool {
	if len(raw) == 0 {
		return true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		*dst = maps.Clone(original)
		return true
	}
	var patch map[string]*string
	if json.Unmarshal(raw, &patch) != nil {
		return false
	}
	if *dst == nil {
		*dst = map[string]string{}
	}
	for locale, value := range patch {
		if locale != "en" && locale != "ja" && locale != "zh" && locale != "ko" {
			return false
		}
		if value == nil {
			if text, ok := original[locale]; ok {
				(*dst)[locale] = text
			} else {
				delete(*dst, locale)
			}
		} else {
			text := strings.TrimSpace(*value)
			if !validDisplayText(text, title) {
				return false
			}
			(*dst)[locale] = text
		}
	}
	return true
}

func (p metadataPatch) apply(o *metadataTranslations, original metadataTranslations) bool {
	if len(p.Title)+len(p.Subtitle)+len(p.TitleTranslations)+len(p.SubtitleTranslations)+len(p.CategoryIDs)+len(p.DemoStart)+len(p.DemoEnd)+len(p.Description)+len(p.DifficultyMakers) == 0 {
		return false
	}
	// Legacy scalar writes are aliases for English only. Reads remain raw source
	// text plus complete dictionaries, independent of request locale.
	for _, pair := range [][2]json.RawMessage{{p.Title, p.TitleTranslations}, {p.Subtitle, p.SubtitleTranslations}} {
		if len(pair[0]) > 0 {
			var dict map[string]json.RawMessage
			_ = json.Unmarshal(pair[1], &dict)
			if _, exists := dict["en"]; exists {
				return false
			}
		}
	}
	if !patchTranslations(p.TitleTranslations, &o.Titles, original.Titles, true) || !patchTranslations(p.SubtitleTranslations, &o.Subtitles, original.Subtitles, false) {
		return false
	}
	for _, field := range []struct {
		raw      json.RawMessage
		dst      *map[string]string
		original map[string]string
		title    bool
	}{{p.Title, &o.Titles, original.Titles, true}, {p.Subtitle, &o.Subtitles, original.Subtitles, false}} {
		if len(field.raw) > 0 {
			raw := append([]byte(`{"en":`), field.raw...)
			raw = append(raw, '}')
			if !patchTranslations(raw, field.dst, field.original, field.title) {
				return false
			}
		}
	}
	return true
}
func translationReset(raw json.RawMessage) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var dict map[string]json.RawMessage
	if json.Unmarshal(raw, &dict) != nil {
		return false
	}
	for _, v := range dict {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return true
		}
	}
	return false
}
func (s *Server) sourceTranslations(ctx context.Context, tx pgx.Tx, id string) (metadataTranslations, error) {
	var key, encoding, wave string
	if err := tx.QueryRow(ctx, `SELECT r.storage_key,c.encoding,c.wave_filename FROM charts c JOIN chart_resources r ON r.chart_id=c.id AND r.kind='tja' WHERE c.id=$1`, id).Scan(&key, &encoding, &wave); err != nil {
		return metadataTranslations{}, err
	}
	f, err := s.Config.Objects.Open(ctx, key)
	if err != nil {
		return metadataTranslations{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, tja.MaxTJA+1))
	if err != nil {
		return metadataTranslations{}, err
	}
	if len(data) > tja.MaxTJA {
		return metadataTranslations{}, fmt.Errorf("source TJA exceeds limit")
	}
	parsed, issue := tja.Parse(data, encoding, wave)
	if issue != nil {
		return metadataTranslations{}, issue
	}
	return metadataTranslations{parsed.TitleTranslations, parsed.SubtitleTranslations}, nil
}

func (s *Server) editMetadata(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		problem(w, 415, "CONTENT_TYPE_INVALID", "请使用 JSON 请求")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32768)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var patch metadataPatch
	if decoder.Decode(&patch) != nil || decoder.Decode(new(any)) != io.EOF {
		problem(w, 400, "REQUEST_INVALID", "请求只能包含支持的展示字段和一个 JSON 对象")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var owner string
	var translations metadataTranslations
	var sourceTitle, sourceSubtitle string
	err = tx.QueryRow(r.Context(), `SELECT owner_id,title,subtitle,title_translations,subtitle_translations FROM charts c WHERE c.id=$1 AND `+publishedChart+` FOR UPDATE`, r.PathValue("id")).Scan(&owner, &sourceTitle, &sourceSubtitle, &translations.Titles, &translations.Subtitles)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在或已下架")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if owner != u.User.ID && !u.User.IsAdmin {
		problem(w, 403, "FORBIDDEN", "只有作品作者或网站管理员可以修改信息")
		return
	}
	original := metadataTranslations{map[string]string{"en": sourceTitle}, map[string]string{"en": sourceSubtitle}}
	if translationReset(patch.TitleTranslations) || translationReset(patch.SubtitleTranslations) {
		original, err = s.sourceTranslations(r.Context(), tx, r.PathValue("id"))
		if err != nil {
			internal(w, err)
			return
		}
	}
	if !patch.apply(&translations, original) {
		problem(w, 422, "METADATA_INVALID", "需要有效的名称／副标题；每项最多 500 字节，名称不能为空，多语言仅支持 en、ja、zh、ko，null 恢复原值")
		return
	}
	if len(patch.Description) > 0 {
		var description string
		if bytes.Equal(bytes.TrimSpace(patch.Description), []byte("null")) || json.Unmarshal(patch.Description, &description) != nil || len([]rune(description)) > 1000 || strings.ContainsRune(description, 0) {
			problem(w, 422, "DESCRIPTION_INVALID", "谱面介绍最多 1000 字符，不能包含空字符")
			return
		}
		if _, err = tx.Exec(r.Context(), `UPDATE charts SET description=$2,metadata_updated_at=now() WHERE id=$1`, r.PathValue("id"), description); err != nil {
			internal(w, err)
			return
		}
	}
	if len(patch.DifficultyMakers) > 0 {
		var difficulties []tja.Difficulty
		if err = tx.QueryRow(r.Context(), `SELECT difficulties FROM charts WHERE id=$1`, r.PathValue("id")).Scan(&difficulties); err != nil {
			internal(w, err)
			return
		}
		if err = applyDifficultyMakers(string(patch.DifficultyMakers), difficulties); err != nil {
			problem(w, 422, "MAKERS_INVALID", err.Error())
			return
		}
		if _, err = tx.Exec(r.Context(), `UPDATE charts SET difficulties=$2,metadata_updated_at=now() WHERE id=$1`, r.PathValue("id"), difficulties); err != nil {
			internal(w, err)
			return
		}
	}
	if len(patch.DemoStart)+len(patch.DemoEnd) > 0 {
		c, e := s.readChart(tx.QueryRow(r.Context(), chartSelect+` WHERE c.id=$1`, r.PathValue("id")))
		if e != nil {
			internal(w, e)
			return
		}
		start, end := c.DemoStart, c.DemoEnd
		valid := true
		for _, field := range []struct {
			raw   json.RawMessage
			value *float64
		}{{patch.DemoStart, &start}, {patch.DemoEnd, &end}} {
			if len(field.raw) > 0 && (bytes.Equal(bytes.TrimSpace(field.raw), []byte("null")) || json.Unmarshal(field.raw, field.value) != nil) {
				valid = false
			}
		}
		if !valid || !audio.ValidPreviewRange(start, end, c.Duration) {
			problem(w, 422, "PREVIEW_RANGE_INVALID", "试听起点必须位于音频内，终点必须晚于起点且不超过 1215 秒；超出音频的部分会截到结尾")
			return
		}
		if start != c.DemoStart || end != c.DemoEnd || c.PreviewPath == "" {
			c.DemoStart, c.DemoEnd = start, end
			if e = s.previewFromStoredAudio(r.Context(), tx, c); e != nil {
				internal(w, e)
				return
			}
			if _, e = tx.Exec(r.Context(), `UPDATE charts SET demo_start=$2,demo_end=$3,metadata_updated_at=now() WHERE id=$1`, c.ID, start, end); e != nil {
				internal(w, e)
				return
			}
		}
	}

	if len(patch.CategoryIDs) > 0 {
		ids, e := categorySelection(patch.CategoryIDs)
		if e != nil {
			problem(w, 422, "CATEGORIES_INVALID", "分类必须为分类 ID 数组")
			return
		}
		if !validCategories(ids) {
			problem(w, 422, "CATEGORIES_INVALID", "包含不存在的分类，请刷新后重试")
			return
		}
		if e = setCategories(r.Context(), tx, r.PathValue("id"), ids); e != nil {
			internal(w, e)
			return
		}
	}
	if len(patch.Title)+len(patch.Subtitle)+len(patch.TitleTranslations)+len(patch.SubtitleTranslations) > 0 {
		_, err = tx.Exec(r.Context(), `UPDATE charts SET title_translations=$2,subtitle_translations=$3,metadata_updated_at=now() WHERE id=$1`, r.PathValue("id"), translations.Titles, translations.Subtitles)
		if err != nil {
			internal(w, err)
			return
		}
	}
	chart, err := s.readChart(tx.QueryRow(r.Context(), chartSelect+` WHERE c.id=$1`, r.PathValue("id")))
	if err != nil {
		internal(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		internal(w, err)
		return
	}
	chart.setUploader(s.publicProfiles(r.Context(), []string{chart.OwnerID})[chart.OwnerID])
	items := []Chart{chart}
	if err = s.coverHashes(r.Context(), items); err != nil {
		internal(w, err)
		return
	}
	respond(w, 200, items[0])
}
