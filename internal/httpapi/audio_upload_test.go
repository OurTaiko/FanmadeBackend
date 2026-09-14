package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAudioUploadAndDownload(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	cookie := strings.Repeat("a", 64)
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES('u','tester','unused')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,'u','csrf',now()+interval '1 day')`, hash(cookie)); err != nil {
		t.Fatal(err)
	}
	handler := New(pool, Config{Origin: "http://localhost", Storage: t.TempDir()}).Handler()
	for _, name := range []string{"cbr.mp3", "vorbis.ogg"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("../audio/testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			post := func(wave, filename string, content []byte) *httptest.ResponseRecorder {
				var body bytes.Buffer
				form := multipart.NewWriter(&body)
				tf, _ := form.CreateFormFile("tja", "chart.tja")
				fmt.Fprintf(tf, "TITLE:Audio Test\nBPM:120\nWAVE:%s\nCOURSE:Oni\nLEVEL:5\n#START\n1000,\n#END\n", wave)
				af, _ := form.CreateFormFile("audio", filename)
				af.Write(content)
				form.Close()
				r := httptest.NewRequest("POST", "/api/v1/charts", &body)
				r.Header.Set("Content-Type", form.FormDataContentType())
				r.Header.Set("Origin", "http://localhost")
				r.Header.Set("X-CSRF-Token", "csrf")
				r.Header.Set("Idempotency-Key", ID())
				r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: cookie})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			w := post(name, name, data)
			var chart Chart
			if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &chart) != nil {
				t.Fatal(w.Code, w.Body.String())
			}
			digest := sha256.Sum256(data)
			if chart.AudioName != name || chart.Wave != name || chart.AudioHash != hex.EncodeToString(digest[:]) {
				t.Fatal("original filename/hash lost")
			}
			media := "audio/mpeg"
			if strings.HasSuffix(name, ".ogg") {
				media = "audio/ogg"
			}
			var storedMedia string
			if err := pool.QueryRow(ctx, `SELECT f.media_type FROM files f JOIN chart_versions v ON v.audio_file_id=f.id WHERE v.id=$1`, chart.VersionID).Scan(&storedMedia); err != nil || storedMedia != media {
				t.Fatal("incorrect stored MIME", storedMedia, err)
			}
			base := "/api/v1/charts/" + chart.ID + "/versions/" + chart.VersionID + "/"
			get := func(kind, rangeValue string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("GET", base+kind, nil)
				r.Header.Set("Range", rangeValue)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			w = get("audio", "")
			if w.Code != 200 || w.Header().Get("Content-Type") != media || !bytes.Equal(w.Body.Bytes(), data) {
				t.Fatal("audio download did not preserve bytes and MIME", w.Code)
			}
			w = get("audio", "bytes=0-3")
			if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), data[:4]) || w.Header().Get("Content-Type") != media {
				t.Fatal("broken audio range request")
			}
			w = get("download", "")
			z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
			if err != nil || len(z.File) != 2 {
				t.Fatal("invalid zip", err)
			}
			f, err := z.Open(name)
			if err != nil {
				t.Fatal("MP3 filename not preserved in zip", err)
			}
			zipped, err := io.ReadAll(f)
			f.Close()
			if err != nil || !bytes.Equal(zipped, data) {
				t.Fatal("ZIP audio changed")
			}
			wrong := "fake.mp3"
			if media == "audio/mpeg" {
				wrong = "fake.ogg"
			}
			for _, invalid := range []struct {
				wave, filename, code string
				data                 []byte
			}{
				{name, "wrong" + name, "TJA_AUDIO_MISMATCH", data},
				{wrong, wrong, "AUDIO_INVALID", data},
				{name, name, "AUDIO_INVALID", data[:len(data)-11]},
			} {
				w = post(invalid.wave, invalid.filename, invalid.data)
				if w.Code != 422 || !strings.Contains(w.Body.String(), invalid.code) {
					t.Fatal("invalid upload accepted", w.Code, w.Body.String())
				}
			}
		})
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM charts`).Scan(&count); err != nil || count != 2 {
		t.Fatal("failed uploads created records", count, err)
	}
}
