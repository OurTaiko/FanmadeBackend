// Package teststore supplies an HTTP S3 fixture for storage integration tests.
package teststore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"ourtaiko.dev/fanmade/api/internal/objectstore"
)

func New(t *testing.T) *objectstore.S3 {
	t.Helper()
	var mu sync.Mutex
	type item struct {
		data        []byte
		media, etag string
	}
	objects := map[string]item{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		v, exists := objects[r.URL.Path]
		fail := func(status int, code string) {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(status)
			fmt.Fprintf(w, "<Error><Code>%s</Code></Error>", code)
		}
		switch r.Method {
		case "PUT":
			if r.Header.Get("If-None-Match") != "*" {
				fail(400, "UnsafeWrite")
				return
			}
			if exists {
				fail(412, "PreconditionFailed")
				return
			}
			data, err := io.ReadAll(r.Body)
			if err != nil {
				fail(500, "ReadFailure")
				return
			}
			sum := sha256.Sum256(data)
			if base64.StdEncoding.EncodeToString(sum[:]) != r.Header.Get("X-Amz-Checksum-Sha256") {
				fail(400, "BadDigest")
				return
			}
			objects[r.URL.Path] = item{data, r.Header.Get("Content-Type"), fmt.Sprintf("\"%x\"", sum)}
			w.WriteHeader(200)
		case "GET", "HEAD":
			if !exists {
				fail(404, "NoSuchKey")
				return
			}
			w.Header().Set("Content-Type", v.media)
			w.Header().Set("ETag", v.etag)
			http.ServeContent(w, r, "object", time.Unix(1700000000, 0), bytes.NewReader(v.data))
		case "DELETE":
			delete(objects, r.URL.Path)
			w.WriteHeader(204)
		default:
			fail(405, "MethodNotAllowed")
		}
	}))
	t.Cleanup(server.Close)
	client := s3.New(s3.Options{Region: "ap-northeast-1", BaseEndpoint: aws.String(server.URL), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")})
	return &objectstore.S3{Client: client, Bucket: "test", Prefix: "fanmade/test/"}
}
func Seed(t *testing.T, s *objectstore.S3, key string, data []byte) {
	t.Helper()
	sum := sha256.Sum256(data)
	if err := s.Put(context.Background(), key, bytes.NewReader(data), int64(len(data)), "application/octet-stream", fmt.Sprintf("%x", sum)); err != nil {
		t.Fatal(err)
	}
}
