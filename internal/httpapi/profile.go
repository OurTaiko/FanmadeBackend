package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validNickname(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 40 || len(value) > 160 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

func (s *Server) editProfile(w http.ResponseWriter, r *http.Request) {
	current, ok := s.required(w, r, true)
	if !ok {
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		problem(w, 415, "CONTENT_TYPE_INVALID", "请使用 JSON 请求")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Nickname *string `json:"nickname"`
	}
	if err := decoder.Decode(&input); err != nil {
		problem(w, 400, "REQUEST_INVALID", "仅支持修改昵称，请提交 nickname 字段")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		problem(w, 400, "REQUEST_INVALID", "请求只能包含一个 JSON 对象")
		return
	}
	if input.Nickname == nil {
		problem(w, 422, "NICKNAME_INVALID", "昵称不能为空")
		return
	}
	nickname := strings.TrimSpace(*input.Nickname)
	if !validNickname(nickname) {
		problem(w, 422, "NICKNAME_INVALID", "昵称需为 1–40 个字符，不能包含换行或控制字符")
		return
	}
	// Only the authenticated account can change its display name.
	err := s.DB.QueryRow(r.Context(), `UPDATE users SET nickname=$2 WHERE id=$1 RETURNING nickname`, current.User.ID, nickname).Scan(&current.User.Nickname)
	if err != nil {
		internal(w, err)
		return
	}
	respond(w, 200, map[string]any{"user": current.User, "csrfToken": current.CSRF})
}
