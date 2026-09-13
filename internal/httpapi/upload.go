package httpapi

import (
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

	"github.com/jackc/pgx/v5"
	"ourtaiko.dev/fanmade/api/internal/audio"
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
	r.Body = http.MaxBytesReader(w, r.Body, 105*1024*1024)
	mr, e := r.MultipartReader()
	if e != nil {
		problem(w, 400, "UPLOAD_FILES_INVALID", "请选择 TJA 和 OGG 文件")
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
		if field == "tja" || field == "audio" {
			ext := ".tja"
			maxBytes := int64(tja.MaxTJA)
			if field == "audio" {
				ext = ".ogg"
				maxBytes = tja.MaxAudio
			}
			if files[field] != nil || !tja.SafeFilename(name) || !strings.EqualFold(filepath.Ext(name), ext) {
				problem(w, 400, "UPLOAD_FILES_INVALID", "必须各上传一个 TJA 和 OGG，文件名不能包含路径")
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
			if (field != "encoding" && field != "description") || name != "" {
				problem(w, 400, "UPLOAD_FILES_INVALID", "包含不支持的上传字段")
				return
			}
			if _, exists := fields[field]; exists {
				problem(w, 400, "UPLOAD_FILES_INVALID", "上传字段重复")
				return
			}
			data, e := io.ReadAll(io.LimitReader(part, 4001))
			if e != nil {
				uploadReadError(w, e)
				return
			}
			if len(data) > 4000 {
				problem(w, 400, "DESCRIPTION_TOO_LONG", "说明不能超过 4000 字节")
				return
			}
			fields[field] = string(data)
		}
		part.Close()
	}
	tf, af := files["tja"], files["audio"]
	if tf == nil || af == nil {
		problem(w, 400, "UPLOAD_FILES_INVALID", "必须同时选择 TJA 和 OGG")
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
	meta, issue := tja.Parse(data, encoding, af.name)
	if issue != nil {
		respond(w, 422, map[string]any{"code": issue.Code, "message": issue.Message, "errors": []*tja.Issue{issue}, "requestId": w.Header().Get("X-Request-ID"), "validationVersion": tja.Version})
		return
	}
	duration, e := audio.Validate(r.Context(), af.path)
	if e != nil {
		problem(w, 422, "AUDIO_INVALID", "音频未通过完整性检查，请使用完整的单音轨 Ogg Vorbis 文件（最长 20 分钟）")
		return
	}
	digestData, _ := json.Marshal([]string{tf.name, tf.sha, af.name, af.sha, encoding, fields["description"]})
	digest := hash(string(digestData))
	tx, e := s.DB.Begin(r.Context())
	if e != nil {
		internal(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, u.User.ID+":"+key); e != nil {
		internal(w, e)
		return
	}
	var previousID, previousDigest string
	e = tx.QueryRow(r.Context(), `SELECT chart_id,payload_digest FROM upload_requests WHERE user_id=$1 AND idempotency_key=$2`, u.User.ID, key).Scan(&previousID, &previousDigest)
	if e == nil {
		if previousDigest != digest {
			problem(w, 409, "IDEMPOTENCY_CONFLICT", "此请求标识已用于不同的文件，请重新选择后提交")
			return
		}
		tx.Rollback(r.Context())
		c, e := s.chart(r.Context(), previousID)
		if errors.Is(e, pgx.ErrNoRows) {
			problem(w, 409, "CHART_REMOVED", "该次上传的作品已删除，请重新选择文件")
			return
		}
		if e != nil {
			internal(w, e)
			return
		}
		respond(w, 200, c)
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		internal(w, e)
		return
	}
	chartID, versionID := ID(), ID()
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(filepath.Join(s.Config.Storage, "objects", versionID))
		}
	}()
	for field, f := range files {
		f.key = filepath.Join("objects", versionID, field)
		target := filepath.Join(s.Config.Storage, f.key)
		if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
			internal(w, e)
			return
		}
		if e = os.Rename(f.path, target); e != nil {
			internal(w, e)
			return
		}
	}
	for field, f := range files {
		media := "application/octet-stream"
		if field == "audio" {
			media = "audio/ogg"
		}
		if _, e = tx.Exec(r.Context(), `INSERT INTO files(id,storage_key,original_filename,sha256,byte_size,media_type) VALUES($1,$2,$3,$4,$5,$6)`, f.id, f.key, f.name, f.sha, f.size, media); e != nil {
			internal(w, e)
			return
		}
	}
	if _, e = tx.Exec(r.Context(), `INSERT INTO charts(id,owner_id,description,current_version_id) VALUES($1,$2,$3,$4)`, chartID, u.User.ID, fields["description"], versionID); e != nil {
		internal(w, e)
		return
	}
	if _, e = tx.Exec(r.Context(), `INSERT INTO chart_versions(id,chart_id,version_number,title,subtitle,maker,bpm,offset_seconds,demo_start,duration,encoding,wave_filename,tja_file_id,audio_file_id,validation_version) VALUES($1,$2,1,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, versionID, chartID, meta.Title, meta.Subtitle, meta.Maker, meta.BPM, meta.Offset, meta.DemoStart, duration, encoding, meta.Wave, tf.id, af.id, tja.Version); e != nil {
		internal(w, e)
		return
	}
	if _, e = tx.Exec(r.Context(), `UPDATE chart_versions SET title_translations=$2,subtitle_translations=$3 WHERE id=$1`, versionID, meta.TitleTranslations, meta.SubtitleTranslations); e != nil {
		internal(w, e)
		return
	}
	for _, d := range meta.Difficulties {
		if _, e = tx.Exec(r.Context(), `INSERT INTO difficulties(version_id,block_index,course,level,player,style) VALUES($1,$2,$3,$4,$5,$6)`, versionID, d.BlockIndex, d.Course, d.Level, d.Player, d.Style); e != nil {
			internal(w, e)
			return
		}
	}
	if _, e = tx.Exec(r.Context(), `INSERT INTO upload_requests(user_id,idempotency_key,payload_digest,chart_id) VALUES($1,$2,$3,$4)`, u.User.ID, key, digest, chartID); e != nil {
		internal(w, e)
		return
	}
	// A lost connection during COMMIT may have committed. Keep objects for reconciliation.
	committed = true
	if e = tx.Commit(r.Context()); e != nil {
		internal(w, e)
		return
	}
	committed = true
	c, e := s.chart(r.Context(), chartID)
	if e != nil {
		internal(w, e)
		return
	}
	respond(w, 201, c)
}
func uploadReadError(w http.ResponseWriter, e error) {
	var max *http.MaxBytesError
	if errors.As(e, &max) {
		problem(w, 413, "FILE_TOO_LARGE", "上传超过请求大小限制")
	} else {
		problem(w, 400, "UPLOAD_INTERRUPTED", "上传中断或请求格式错误，请重试")
	}
}
