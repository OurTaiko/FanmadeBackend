package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"ourtaiko.dev/fanmade/api/internal/audio"
	"ourtaiko.dev/fanmade/api/internal/cover"
	"ourtaiko.dev/fanmade/api/internal/tja"
)

type stagedFile struct {
	name, path, sha, id, key string
	size                     int64
}

var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9-]{16,80}$`)

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	u, ok := s.required(w, r, true)
	if !ok {
		return
	}
	var existing *Chart
	if id := r.PathValue("id"); id != "" {
		c, err := s.chart(r.Context(), id)
		if errors.Is(err, pgx.ErrNoRows) {
			problem(w, 404, "CHART_NOT_FOUND", "歌曲不存在或已下架")
			return
		}
		if err != nil {
			internal(w, err)
			return
		}
		if c.OwnerID != u.User.ID && !u.User.IsAdmin {
			problem(w, 403, "FORBIDDEN", "只有上传者或管理员可以替换歌曲")
			return
		}
		existing = &c
	}
	key := r.Header.Get("Idempotency-Key")
	if !keyPattern.MatchString(key) {
		problem(w, 400, "IDEMPOTENCY_KEY_INVALID", "需要有效的上传请求标识")
		return
	}
	select {
	case s.uploads <- struct{}{}:
		defer func() { <-s.uploads }()
	default:
		problem(w, 503, "UPLOAD_BUSY", "当前正在处理其他上传，请稍后重试")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 112*1024*1024)
	mr, e := r.MultipartReader()
	if e != nil {
		problem(w, 400, "UPLOAD_FILES_INVALID", "请选择 TJA 和 OGG 或 MP3 音频文件")
		return
	}
	dir, e := os.MkdirTemp(s.Config.Storage, "staging-")
	if e != nil {
		internal(w, e)
		return
	}
	defer os.RemoveAll(dir)
	files := map[string]*stagedFile{}
	fields := map[string]string{}
	for {
		part, e := mr.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			uploadReadError(w, e)
			return
		}
		disposition, params, e := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if e != nil || disposition != "form-data" {
			problem(w, 400, "UPLOAD_FILES_INVALID", "上传字段格式错误")
			return
		}
		field, name := params["name"], params["filename"]
		if field == "tja" || field == "audio" || (field == "cover" && existing == nil) {
			validExtension := strings.EqualFold(filepath.Ext(name), ".tja")
			maxBytes := int64(tja.MaxTJA)
			if field == "audio" {
				validExtension = audio.MediaType(name) != ""
				maxBytes = tja.MaxAudio
			}
			if field == "cover" {
				validExtension = cover.ValidExtension(name)
				maxBytes = cover.MaxBytes
			}
			if files[field] != nil || !tja.SafeFilename(name) || !validExtension {
				problem(w, 400, "UPLOAD_FILES_INVALID", "文件类型、数量或文件名无效；封面仅支持 JPG / PNG / WebP，文件名不能包含路径")
				return
			}
			f := &stagedFile{name: name, path: filepath.Join(dir, field), id: ID()}
			out, e := os.OpenFile(f.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				internal(w, e)
				return
			}
			h := sha256.New()
			n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(part, maxBytes+1))
			closeErr := out.Close()
			if copyErr != nil {
				uploadReadError(w, copyErr)
				return
			}
			if closeErr != nil {
				internal(w, closeErr)
				return
			}
			if n == 0 || n > maxBytes {
				problem(w, 413, "FILE_TOO_LARGE", "文件为空或超过上传大小限制")
				return
			}
			f.size = n
			f.sha = hex.EncodeToString(h.Sum(nil))
			files[field] = f
		} else {
			if (field != "encoding" && field != "description" && field != "categoryIds" && field != "difficultyMakers" && !(existing != nil && field == "confirmReset")) || name != "" {
				problem(w, 400, "UPLOAD_FILES_INVALID", "包含不支持的上传字段")
				return
			}
			if _, exists := fields[field]; exists {
				problem(w, 400, "UPLOAD_FILES_INVALID", "上传字段重复")
				return
			}
			limit := int64(4000)
			if field == "difficultyMakers" {
				limit = 64 * 1024
			}
			data, e := io.ReadAll(io.LimitReader(part, limit+1))
			if e != nil {
				uploadReadError(w, e)
				return
			}
			if int64(len(data)) > limit {
				if field == "difficultyMakers" {
					problem(w, 400, "UPLOAD_FIELD_TOO_LONG", "制作者表格不能超过 64 KiB")
				} else {
					problem(w, 400, "DESCRIPTION_TOO_LONG", "说明不能超过 4000 字节")
				}
				return
			}
			fields[field] = string(data)
		}
		part.Close()
	}
	categoryIDs, categoryErr := categorySelection(json.RawMessage(fields["categoryIds"]))
	if categoryErr != nil {
		problem(w, 422, "CATEGORIES_INVALID", "分类必须为分类 ID 数组")
		return
	}
	if !validCategories(categoryIDs) {
		problem(w, 422, "CATEGORIES_INVALID", "包含不存在的分类，请刷新后重试")
		return
	}
	cf := files["cover"]
	delete(files, "cover") // Covers belong to charts, independently of TJA/audio replacements.
	var coverData []byte
	if cf != nil {
		raw, err := os.ReadFile(cf.path)
		if err != nil {
			internal(w, err)
			return
		}
		var ok bool
		coverData, ok = encodeCover(w, r, cf.name, raw)
		if !ok {
			return
		}
	}
	tf, af := files["tja"], files["audio"]
	if tf == nil || (af == nil && existing == nil) {
		message := "必须同时选择 TJA 和一个 OGG 或 MP3 音频"
		if existing != nil {
			message = "必须选择新的 TJA，音频可沿用当前文件"
		}
		problem(w, 400, "UPLOAD_FILES_INVALID", message)
		return
	}
	encoding := fields["encoding"]
	if encoding == "" {
		encoding = "utf-8"
	}
	data, e := os.ReadFile(tf.path)
	if e != nil {
		internal(w, e)
		return
	}
	normalized, issue := tja.NormalizeUTF8(data, encoding)
	if issue != nil {
		respond(w, 422, map[string]any{"code": issue.Code, "message": issue.Message, "errors": []*tja.Issue{issue}, "requestId": w.Header().Get("X-Request-ID"), "validationVersion": tja.Version})
		return
	}
	if !bytes.Equal(data, normalized) {
		if e = os.WriteFile(tf.path, normalized, 0600); e != nil {
			internal(w, e)
			return
		}
	}
	encoding = "utf-8"
	tf.size = int64(len(normalized))
	tjaHash := sha256.Sum256(normalized)
	tf.sha = hex.EncodeToString(tjaHash[:])
	var tx pgx.Tx
	var replacementDigest string
	if existing != nil {
		if fields["confirmReset"] != "true" {
			problem(w, 400, "REPLACEMENT_CONFIRMATION_REQUIRED", "替换需要确认删除旧文件和旧成绩")
			return
		}
		// Hash only submitted data: a retry must work after the old audio was deleted.
		parts := []string{existing.ID, tf.name, tf.sha}
		if af != nil {
			parts = append(parts, af.name, af.sha)
		}
		encoded, _ := json.Marshal(struct {
			Parts  []string
			Fields map[string]string
		}{parts, fields})
		replacementDigest = hash("replace:" + string(encoded))
		var proceed bool
		tx, proceed = s.beginUpload(w, r, u.User.ID, key, replacementDigest)
		if !proceed {
			return
		}
		defer tx.Rollback(r.Context())
		// Reload while holding the song lock so retained audio always belongs to
		// the files currently being replaced, even after a concurrent upload.
		var lockedID string
		err := tx.QueryRow(r.Context(), `SELECT c.id FROM charts c WHERE c.id=$1 AND `+publishedChart+` FOR UPDATE OF c`, existing.ID).Scan(&lockedID)
		if errors.Is(err, pgx.ErrNoRows) {
			problem(w, 404, "CHART_NOT_FOUND", "歌曲不存在或已下架")
			return
		}
		if err != nil {
			internal(w, err)
			return
		}
		// Read resource rows in a fresh snapshot after any lock wait.
		current, err := readChart(tx.QueryRow(r.Context(), chartSelect+` WHERE c.id=$1`, lockedID))
		if err != nil {
			internal(w, err)
			return
		}
		existing = &current
		if af == nil {
			af, e = s.copyAudio(r.Context(), existing, dir)
			if e != nil {
				internal(w, e)
				return
			}
			files["audio"] = af
		}
	}
	meta, issue := tja.Parse(normalized, encoding, af.name)
	if issue != nil {
		respond(w, 422, map[string]any{"code": issue.Code, "message": issue.Message, "errors": []*tja.Issue{issue}, "requestId": w.Header().Get("X-Request-ID"), "validationVersion": tja.Version})
		return
	}
	if e = applyDifficultyMakers(fields["difficultyMakers"], meta.Difficulties); e != nil {
		problem(w, 422, "DIFFICULTY_MAKERS_INVALID", e.Error())
		return
	}
	duration, e := audio.Validate(r.Context(), af.path, af.name)
	if e != nil {
		problem(w, 422, "AUDIO_INVALID", "音频未通过完整性检查，请使用完整的单音轨 Ogg Vorbis 或 MP3 文件（最长 20 分钟）")
		return
	}
	digestData, _ := json.Marshal([]string{tf.name, tf.sha, af.name, af.sha, encoding, fields["description"]})
	if len(categoryIDs) != 1 || categoryIDs[0] != "variety" {
		encoded, _ := json.Marshal(categoryIDs)
		digestData = append(digestData, encoded...)
	}
	for _, d := range meta.Difficulties {
		if d.Maker != meta.Maker {
			encoded, _ := json.Marshal(meta.Difficulties)
			digestData = append(digestData, encoded...)
			break
		}
	}
	if cf != nil {
		encoded, _ := json.Marshal([]string{"cover", cf.name, cf.sha})
		digestData = append(digestData, encoded...)
	}
	digest := hash(string(digestData))
	if existing != nil {
		digest = replacementDigest
	}
	if tx == nil {
		var proceed bool
		tx, proceed = s.beginUpload(w, r, u.User.ID, key, digest)
		if !proceed {
			return
		}
		defer tx.Rollback(r.Context())
	}
	chartID := ID()
	if existing != nil {
		chartID = existing.ID
	}
	committed := false
	defer func() {
		if !committed {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			for _, f := range files {
				if f.key != "" {
					_ = s.Config.Objects.Delete(ctx, f.key)
				}
			}
		}
	}()
	for field, f := range files {
		f.key = filepath.ToSlash(filepath.Join("objects", chartID, f.id, field))
		media := "application/octet-stream"
		if field == "audio" {
			media = audio.MediaType(f.name)
		}
		if e = s.storeFile(r.Context(), f.key, f.path, f.size, media, f.sha); e != nil {
			internal(w, e)
			return
		}
	}
	if existing != nil {
		if e = retireChart(r.Context(), tx, chartID, fields["description"]); e != nil {
			internal(w, e)
			return
		}
	}
	if _, e = tx.Exec(r.Context(), `INSERT INTO charts(id,owner_id,description,title,subtitle,bpm,offset_seconds,demo_start,duration,encoding,wave_filename,title_translations,subtitle_translations,is_single,difficulties)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
 ON CONFLICT(id) DO UPDATE SET title=EXCLUDED.title,subtitle=EXCLUDED.subtitle,bpm=EXCLUDED.bpm,offset_seconds=EXCLUDED.offset_seconds,demo_start=EXCLUDED.demo_start,duration=EXCLUDED.duration,encoding=EXCLUDED.encoding,wave_filename=EXCLUDED.wave_filename,title_translations=EXCLUDED.title_translations,subtitle_translations=EXCLUDED.subtitle_translations,is_single=EXCLUDED.is_single,difficulties=EXCLUDED.difficulties`, chartID, u.User.ID, fields["description"], meta.Title, meta.Subtitle, meta.BPM, meta.Offset, meta.DemoStart, duration, encoding, meta.Wave, meta.TitleTranslations, meta.SubtitleTranslations, meta.IsSingle, meta.Difficulties); e != nil {
		internal(w, e)
		return
	}
	for field, f := range files {
		media := "application/octet-stream"
		if field == "audio" {
			media = audio.MediaType(f.name)
		}
		if _, e = tx.Exec(r.Context(), `INSERT INTO chart_resources(chart_id,kind,storage_key,original_filename,sha256,byte_size,media_type) VALUES($1,$2,$3,$4,$5,$6,$7)`, chartID, field, f.key, f.name, f.sha, f.size, media); e != nil {
			internal(w, e)
			return
		}
	}
	if cf != nil {
		if e = s.saveCover(r.Context(), tx, chartID, coverData); e != nil {
			internal(w, e)
			return
		}
	}
	if e = setCategories(r.Context(), tx, chartID, categoryIDs); e != nil {
		internal(w, e)
		return
	}
	if s.remoteStorage() {
		if e = s.saveArchive(r.Context(), tx, chartID, tf.path, af.path, tf.name, meta.Wave, dir); e != nil {
			internal(w, e)
			return
		}
	}
	if _, e = tx.Exec(r.Context(), `INSERT INTO upload_requests(user_id,idempotency_key,payload_digest,chart_id,tja_sha256,audio_sha256) VALUES($1,$2,$3,$4,$5,$6)`, u.User.ID, key, digest, chartID, tf.sha, af.sha); e != nil {
		internal(w, e)
		return
	}
	// A lost connection during COMMIT may have committed. Keep objects for reconciliation.
	committed = true
	if e = tx.Commit(r.Context()); e != nil {
		internal(w, e)
		return
	}
	c, e := s.chart(r.Context(), chartID)
	if e != nil {
		internal(w, e)
		return
	}
	if existing != nil {
		s.cleanupReplacedFiles()
		respond(w, 200, c)
	} else {
		respond(w, 201, c)
	}
}
func uploadReadError(w http.ResponseWriter, e error) {
	var max *http.MaxBytesError
	if errors.As(e, &max) {
		problem(w, 413, "FILE_TOO_LARGE", "上传超过请求大小限制")
	} else {
		problem(w, 400, "UPLOAD_INTERRUPTED", "上传中断或请求格式错误，请重试")
	}
}
