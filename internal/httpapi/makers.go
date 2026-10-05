package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"ourtaiko.dev/fanmade/api/internal/tja"
)

// Keep the first occurrence in block order, so credits never shuffle between reads.
func difficultyMakers(difficulties []tja.Difficulty) string {
	seen := map[string]bool{}
	names := []string{}
	for _, d := range difficulties {
		name := strings.TrimSpace(d.Maker)
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return strings.Join(names, " | ")
}

// The client identifies blocks, but cannot change their server-parsed difficulty.
func applyDifficultyMakers(raw string, difficulties []tja.Difficulty) error {
	if raw == "" {
		return nil
	}
	var entries []struct {
		Course string  `json:"course"`
		Maker  *string `json:"maker"`
	}
	if !utf8.ValidString(raw) || json.Unmarshal([]byte(raw), &entries) != nil || len(entries) != len(difficulties) {
		return fmt.Errorf("请为每个谱面块提供对应的制作者")
	}
	names := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.Course == "" || entry.Maker == nil {
			return fmt.Errorf("制作者对应的谱面块无效")
		}
		if _, exists := names[entry.Course]; exists {
			return fmt.Errorf("制作者对应的谱面块重复")
		}
		name := strings.TrimSpace(*entry.Maker)
		if len(name) > 500 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return fmt.Errorf("制作者名不能超过 500 字节或包含控制字符")
		}
		names[entry.Course] = name
	}
	for i := range difficulties {
		name, exists := names[difficulties[i].Course]
		if !exists {
			return fmt.Errorf("制作者对应的难度无效")
		}
		difficulties[i].Maker = name
	}
	return nil
}
