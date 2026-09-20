package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"golang.org/x/text/encoding/japanese"
)

func TestUploadStoresCanonicalUTF8(t *testing.T) {
	pool := scoreTestDB(t)
	ctx := context.Background()
	const cookie = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,password_hash) VALUES('89b6ef3a5cb57b6e04f74711d15a8a5f','tester','unused')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES($1,'89b6ef3a5cb57b6e04f74711d15a8a5f','csrf',now()+interval '1 day')`, hash(cookie)); err != nil {
		t.Fatal(err)
	}
	handler := testServer(t, pool, Config{Origin: "http://localhost", Storage: t.TempDir()}).Handler()
	audio, err := os.ReadFile("../audio/testdata/cbr.mp3")
	if err != nil {
		t.Fatal(err)
	}
	canonical := []byte("TITLE:初音ミク\r\nSUBTITLEJA:日本語の副題\r\nBPM:120\r\nWAVE:cbr.mp3\r\nCOURSE:Oni\r\nLEVEL:5\r\n#START\r\n1000,\r\n#END\r\n")
	sjis, err := japanese.ShiftJIS.NewEncoder().Bytes(canonical)
	if err != nil {
		t.Fatal(err)
	}
	post := func(data []byte, encoding, key string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		tf, _ := form.CreateFormFile("tja", "chart.tja")
		tf.Write(data)
		af, _ := form.CreateFormFile("audio", "cbr.mp3")
		af.Write(audio)
		form.WriteField("encoding", encoding)
		form.Close()
		r := httptest.NewRequest("POST", "/api/v1/charts", &body)
		r.Header.Set("Content-Type", form.FormDataContentType())
		r.Header.Set("Origin", "http://localhost")
		r.Header.Set("X-CSRF-Token", "csrf")
		r.Header.Set("Idempotency-Key", key)
		r.AddCookie(&http.Cookie{Name: "ourtaiko_session", Value: cookie})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		name, encoding string
		data           []byte
	}{
		{"UTF-8", "utf-8", canonical},
		{"UTF-8 BOM", "utf-8", append([]byte("\uFEFF"), canonical...)},
		{"Shift-JIS", "shift-jis", sjis},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := ID()
			w := post(tc.data, tc.encoding, key)
			var chart Chart
			if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &chart) != nil {
				t.Fatal(w.Code, w.Body.String())
			}
			digest := sha256.Sum256(canonical)
			if chart.Encoding != "utf-8" || chart.TJAHash != hex.EncodeToString(digest[:]) || chart.Title != "初音ミク" {
				t.Fatal("noncanonical metadata", chart.Encoding, chart.Title)
			}
			var size int64
			if err := pool.QueryRow(ctx, `SELECT f.byte_size FROM files f JOIN chart_versions v ON v.tja_file_id=f.id WHERE v.id=$1`, chart.VersionID).Scan(&size); err != nil || size != int64(len(canonical)) {
				t.Fatal("incorrect normalized byte size", size, err)
			}
			for _, kind := range []string{"tja", "download"} {
				w = httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/charts/"+chart.ID+"/versions/"+chart.VersionID+"/"+kind, nil))
				if w.Code != 200 {
					t.Fatal(w.Code)
				}
				data := w.Body.Bytes()
				if kind == "download" {
					archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
					if err != nil {
						t.Fatal(err)
					}
					data = nil
					for _, file := range archive.File {
						if strings.HasSuffix(file.Name, ".tja") {
							r, _ := file.Open()
							data, err = io.ReadAll(r)
							r.Close()
							if err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				if !bytes.Equal(data, canonical) {
					t.Fatal("download differs from normalized stored bytes", kind)
				}
			}
			w = post(canonical, "utf-8", key)
			if w.Code != 200 {
				t.Fatal("canonical idempotent retry", w.Code, w.Body.String())
			}
		})
	}
	w := post(sjis, "utf-8", ID())
	if w.Code != 422 || !strings.Contains(w.Body.String(), "TJA_ENCODING_INVALID") {
		t.Fatal("invalid UTF-8 must not be trusted", w.Code, w.Body.String())
	}
}
