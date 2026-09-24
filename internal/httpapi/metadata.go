package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

type metadataPatch struct {
	CategoryIDs          json.RawMessage `json:"categoryIds"`
	Title                json.RawMessage `json:"title"`
	Subtitle             json.RawMessage `json:"subtitle"`
	TitleTranslations    json.RawMessage `json:"titleTranslations"`
	SubtitleTranslations json.RawMessage `json:"subtitleTranslations"`
}
type metadataOverrides struct {
	Title, Subtitle   *string
	Titles, Subtitles map[string]string
}

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

func patchText(raw json.RawMessage, dst **string, title bool) bool {
	if len(raw) == 0 {
		return true
	}
	var value *string
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	if value != nil {
		trimmed := strings.TrimSpace(*value)
		value = &trimmed
		if !validDisplayText(*value, title) {
			return false
		}
	}
	*dst = value
	return true
}

func patchTranslations(raw json.RawMessage, dst *map[string]string, title bool) bool {
	if len(raw) == 0 {
		return true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		*dst = map[string]string{}
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
		if locale != "ja" && locale != "zh" && locale != "ko" {
			return false
		}
		if value == nil {
			delete(*dst, locale)
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

func (p metadataPatch) apply(o *metadataOverrides) bool {
	if len(p.Title)+len(p.Subtitle)+len(p.TitleTranslations)+len(p.SubtitleTranslations)+len(p.CategoryIDs) == 0 {
		return false
	}
	return patchText(p.Title, &o.Title, true) && patchText(p.Subtitle, &o.Subtitle, false) && patchTranslations(p.TitleTranslations, &o.Titles, true) && patchTranslations(p.SubtitleTranslations, &o.Subtitles, false)
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
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
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
	var overrides metadataOverrides
	err = tx.QueryRow(r.Context(), `SELECT owner_id,title_override,subtitle_override,title_translation_overrides,subtitle_translation_overrides FROM charts c WHERE c.id=$1 AND `+publishedChart+` FOR UPDATE`, r.PathValue("id")).Scan(&owner, &overrides.Title, &overrides.Subtitle, &overrides.Titles, &overrides.Subtitles)
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
	if !patch.apply(&overrides) {
		problem(w, 422, "METADATA_INVALID", "需要有效的名称／副标题；每项最多 500 字节，名称不能为空，多语言仅支持 ja、zh、ko，null 恢复原值")
		return
	}
	if len(patch.CategoryIDs) > 0 {
		ids, e := categorySelection(patch.CategoryIDs)
		if e != nil {
			problem(w, 422, "CATEGORIES_INVALID", "分类必须为分类 ID 数组")
			return
		}
		valid, e := validCategories(r.Context(), tx, ids)
		if e != nil {
			internal(w, e)
			return
		}
		if !valid {
			problem(w, 422, "CATEGORIES_INVALID", "包含不存在的分类，请刷新后重试")
			return
		}
		if e = setCategories(r.Context(), tx, r.PathValue("id"), ids); e != nil {
			internal(w, e)
			return
		}
	}
	if len(patch.Title)+len(patch.Subtitle)+len(patch.TitleTranslations)+len(patch.SubtitleTranslations) > 0 {
		_, err = tx.Exec(r.Context(), `UPDATE charts SET title_override=$2,subtitle_override=$3,title_translation_overrides=$4,subtitle_translation_overrides=$5,metadata_updated_at=now() WHERE id=$1`, r.PathValue("id"), overrides.Title, overrides.Subtitle, overrides.Titles, overrides.Subtitles)
		if err != nil {
			internal(w, err)
			return
		}
	}
	chart, err := readChart(tx.QueryRow(r.Context(), chartSelect+` WHERE c.id=$1`, r.PathValue("id")))
	if err != nil {
		internal(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		internal(w, err)
		return
	}
	chart.Uploader = s.publicNames(r.Context(), []string{chart.OwnerID})[chart.OwnerID]
	items := []Chart{chart}
	if err = s.coverHashes(r.Context(), items); err != nil {
		internal(w, err)
		return
	}
	respond(w, 200, items[0])
}
