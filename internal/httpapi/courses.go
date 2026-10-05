package httpapi

import "strings"

func normalizeCourse(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	suffix := ""
	if strings.HasSuffix(value, "_1p") || strings.HasSuffix(value, "_2p") {
		suffix = value[len(value)-3:]
		value = value[:len(value)-3]
	}
	base, ok := map[string]string{"easy": "Easy", "normal": "Normal", "hard": "Hard", "oni": "Oni", "edit": "Edit", "ura": "Edit"}[value]
	if !ok {
		return "", false
	}
	return base + suffix, true
}
