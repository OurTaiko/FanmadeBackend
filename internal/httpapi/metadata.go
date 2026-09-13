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
	if len(p.Title)+len(p.Subtitle)+len(p.TitleTranslations)+len(p.SubtitleTranslations) == 0 {
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
	err = tx.QueryRow(r.Context(), `SELECT owner_id,title_override,subtitle_override,title_translation_overrides,subtitle_translation_overrides FROM charts WHERE id=$1 AND status='published' FOR UPDATE`, r.PathValue("id")).Scan(&owner, &overrides.Title, &overrides.Subtitle, &overrides.Titles, &overrides.Subtitles)
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "CHART_NOT_FOUND", "作品不存在或已下架")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	if owner != u.User.ID {
		problem(w, 403, "FORBIDDEN", "只能修改自己的作品")
		return
	}
	if !patch.apply(&overrides) {
		problem(w, 422, "METADATA_INVALID", "需要有效的名称／副标题；每项最多 500 字节，名称不能为空，多语言仅支持 ja、zh、ko，null 恢复原值")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE charts SET title_override=$2,subtitle_override=$3,title_translation_overrides=$4,subtitle_translation_overrides=$5,metadata_updated_at=now() WHERE id=$1`, r.PathValue("id"), overrides.Title, overrides.Subtitle, overrides.Titles, overrides.Subtitles)
	if err != nil {
		internal(w, err)
		return
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
	respond(w, 200, chart)
}
