package tja

import (
	"bytes"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/unicode/norm"
)

const Version = "tja-upload-v3"
const MaxTJA = 2 * 1024 * 1024
const MaxAudio = 100 * 1024 * 1024

type Issue struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Line     int    `json:"line,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
}

func (e *Issue) Error() string { return e.Message }
func fail(code, message string, line int) *Issue {
	return &Issue{Code: code, Message: message, Line: line}
}

type Difficulty struct {
	Course             string `json:"course"`
	Level              int    `json:"level"`
	BlockIndex         int    `json:"blockIndex"`
	Player             string `json:"player"`
	Style              string `json:"style"`
	CloudScoreEligible bool   `json:"cloudScoreEligible"`
}
type Metadata struct {
	Title        string       `json:"title"`
	Subtitle     string       `json:"subtitle"`
	Maker        string       `json:"maker"`
	BPM          float64      `json:"bpm"`
	Offset       float64      `json:"offset"`
	DemoStart    float64      `json:"demoStart"`
	Wave         string       `json:"wave"`
	Difficulties []Difficulty `json:"difficulties"`
}

func SafeFilename(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 240 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:"<>|?*`, r) {
			return false
		}
	}
	return true
}

var number = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)$`)

func numeric(s string) (float64, bool) {
	n, e := strconv.ParseFloat(s, 64)
	return n, e == nil && number.MatchString(s) && !math.IsNaN(n) && !math.IsInf(n, 0)
}

func Parse(data []byte, encoding, audioName string) (Metadata, *Issue) {
	m := Metadata{Difficulties: []Difficulty{}}
	if len(data) == 0 || len(data) > MaxTJA {
		return m, fail("FILE_SIZE_INVALID", "TJA 不能为空且不能超过 2 MiB", 0)
	}
	var err error
	switch encoding {
	case "utf-8":
		if !utf8.Valid(data) {
			return m, fail("TJA_ENCODING_INVALID", "无法按 UTF-8 解码，请选择 Shift-JIS 或转存为 UTF-8", 0)
		}
	case "shift-jis":
		data, err = japanese.ShiftJIS.NewDecoder().Bytes(data)
	default:
		return m, fail("TJA_ENCODING_INVALID", "不支持此文本编码", 0)
	}
	if err != nil || bytes.Contains(data, []byte("\uFFFD")) {
		return m, fail("TJA_ENCODING_INVALID", "文本包含无法识别的字符", 0)
	}
	text := strings.TrimPrefix(string(data), "\uFEFF")
	if strings.ContainsRune(text, 0) {
		return m, fail("TJA_ENCODING_INVALID", "TJA 包含非法空字符", 0)
	}
	waveLine, waves := 0, 0
	started, inBlock, hasNotes := false, false, false
	course, level := "Oni", 0
	style := "Single"
	singleCourses := map[string]int{}
	seen := map[string]bool{}
	courses := map[string]string{"0": "Easy", "1": "Normal", "2": "Hard", "3": "Oni", "4": "Edit", "easy": "Easy", "normal": "Normal", "hard": "Hard", "oni": "Oni", "edit": "Edit", "tower": "Tower", "dan": "Dan", "5": "Tower", "6": "Dan"}
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := i + 1
		if len(raw) > 65536 {
			return m, fail("TJA_STRUCTURE_INVALID", "单行内容超过 64 KiB", line)
		}
		s := strings.TrimSpace(strings.SplitN(raw, "//", 2)[0])
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "#NEXTSONG") {
			return m, fail("TJA_RESOURCE_UNSUPPORTED", "示范版暂不支持切歌或额外资源", line)
		}
		if strings.HasPrefix(s, "#START") {
			if (s != "#START" && s != "#START P1" && s != "#START P2") || inBlock || level == 0 {
				return m, fail("TJA_STRUCTURE_INVALID", "每个谱面需要 LEVEL:1–10 和独立的 #START / #END", line)
			}
			started, inBlock, hasNotes = true, true, false
			player := strings.TrimSpace(strings.TrimPrefix(s, "#START"))
			blockStyle := style
			// Player-labelled blocks never qualify, even without STYLE:Double.
			if player != "" {
				blockStyle = "Double"
			}
			if blockStyle == "Single" {
				if firstLine, exists := singleCourses[course]; exists {
					return m, fail("TJA_DIFFICULTY_DUPLICATE", fmt.Sprintf("%s 难度存在重复单人谱面，第 %d 行已声明；每个难度只允许一个单人谱面块", course, firstLine), line)
				}
				singleCourses[course] = line
			}
			m.Difficulties = append(m.Difficulties, Difficulty{
				Course: course, Level: level, BlockIndex: len(m.Difficulties), Player: player,
				Style: blockStyle, CloudScoreEligible: blockStyle == "Single" && player == "",
			})
			continue
		}
		if s == "#END" {
			if !inBlock || !hasNotes {
				return m, fail("TJA_STRUCTURE_INVALID", "谱面块为空或 #END 没有对应的 #START", line)
			}
			inBlock = false
			continue
		}
		key, value, ok := strings.Cut(s, ":")
		if !ok {
			if inBlock && !strings.HasPrefix(s, "#") && strings.ContainsAny(s, "0123456789") {
				hasNotes = true
			}
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		upper := strings.ToUpper(key)
		if upper == "STYLE" {
			if inBlock {
				return m, fail("TJA_STRUCTURE_INVALID", "STYLE 必须位于谱面块之外", line)
			}
			switch strings.ToLower(value) {
			case "single", "0":
				style = "Single"
			case "double", "duet", "1":
				style = "Double"
			default:
				return m, fail("TJA_STRUCTURE_INVALID", "STYLE 需要 Single / Double（或 0 / 1）", line)
			}
			continue
		}
		if upper == "WAVE" {
			if key != "WAVE" {
				return m, fail("TJA_WAVE_INVALID", "请使用大写 WAVE:", line)
			}
			waves++
			if waves > 1 {
				return m, fail("TJA_WAVE_DUPLICATE", "只能声明一次 WAVE", line)
			}
			if started {
				return m, fail("TJA_WAVE_SCOPE_INVALID", "WAVE 必须位于第一个 #START 之前", line)
			}
			waveLine = line
			m.Wave = value
			continue
		}
		if upper == "LYRICS" || upper == "BGIMAGE" || upper == "BGMOVIE" {
			if value != "" {
				return m, fail("TJA_RESOURCE_UNSUPPORTED", "示范版仅接受 TJA 与单个 OGG", line)
			}
		}
		if key == "COURSE" {
			c, ok := courses[strings.ToLower(value)]
			if !ok || inBlock {
				return m, fail("TJA_STRUCTURE_INVALID", "COURSE 需要 Easy / Normal / Hard / Oni / Edit / Tower / Dan（或 0–6）", line)
			}
			course, level = c, 0
			// STYLE belongs to the course header. A new course defaults to Single.
			style = "Single"
			continue
		}
		if key == "LEVEL" {
			n, e := strconv.Atoi(value)
			if e != nil || n < 1 || n > 10 || inBlock {
				return m, fail("TJA_STRUCTURE_INVALID", "LEVEL 必须是 1–10 的整数", line)
			}
			level = n
			continue
		}
		switch key {
		case "TITLE", "SUBTITLE", "MAKER", "BPM", "OFFSET", "DEMOSTART":
			if started || seen[key] {
				return m, fail("TJA_STRUCTURE_INVALID", key+" 必须在谱面开始前且只声明一次", line)
			}
			seen[key] = true
			if len(value) > 500 {
				return m, fail("TJA_STRUCTURE_INVALID", "元数据字段过长", line)
			}
			switch key {
			case "TITLE":
				m.Title = value
			case "SUBTITLE":
				m.Subtitle = value
			case "MAKER":
				m.Maker = value
			default:
				n, valid := numeric(value)
				if !valid {
					return m, fail("TJA_STRUCTURE_INVALID", key+" 必须为有限数字", line)
				}
				switch key {
				case "BPM":
					m.BPM = n
				case "OFFSET":
					m.Offset = n
				case "DEMOSTART":
					m.DemoStart = n
				}
			}
		}
	}
	if waves == 0 || m.Wave == "" {
		return m, fail("TJA_WAVE_MISSING", "TJA 缺少非空的 WAVE 音频引用", waveLine)
	}
	if !SafeFilename(m.Wave) || !strings.EqualFold(filepath.Ext(m.Wave), ".ogg") {
		return m, fail("TJA_WAVE_PATH_INVALID", "WAVE 必须为不带路径、引号的 .ogg 文件名", waveLine)
	}
	if !SafeFilename(audioName) {
		return m, fail("UPLOAD_FILENAME_INVALID", "上传文件名不能包含路径或特殊字符", 0)
	}
	if norm.NFC.String(m.Wave) != norm.NFC.String(audioName) {
		return m, &Issue{Code: "TJA_AUDIO_MISMATCH", Message: fmt.Sprintf("第 %d 行引用了「%s」，但你选择的是「%s」。请选择对应文件，或修改 TJA 的 WAVE 后重新选择。", waveLine, m.Wave, audioName), Line: waveLine, Expected: m.Wave, Actual: audioName}
	}
	if m.Title == "" || m.BPM <= 0 || inBlock || len(m.Difficulties) == 0 {
		return m, fail("TJA_STRUCTURE_INVALID", "需要 TITLE、正数 BPM 和完整谱面块", 0)
	}
	return m, nil
}
