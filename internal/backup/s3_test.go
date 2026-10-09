package backup

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func testS3(t *testing.T, handler http.HandlerFunc) *S3 {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := s3.NewFromConfig(aws.Config{Region: "ap-northeast-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(server.URL)
		o.UsePathStyle = true
		o.RetryMaxAttempts = 1
	})
	return &S3{client: client, bucket: "test", prefix: "fanmade/backups/postgresql/ourtaiko_fanmade/"}
}
func TestS3PublishVerifiesBytesBeforeManifest(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			var uploaded []byte
			var calls []string
			s := testS3(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+filepath.Base(r.URL.Path))
				if r.Method == "PUT" {
					body, _ := io.ReadAll(r.Body)
					sum := sha256.Sum256(body)
					if r.Header.Get("x-amz-server-side-encryption") != "AES256" || r.Header.Get("If-None-Match") != "*" || r.Header.Get("x-amz-checksum-sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
						t.Error("missing encryption, immutability or checksum")
					}
					if strings.HasSuffix(r.URL.Path, DumpName) {
						uploaded = body
					}
				} else if r.Method == "GET" {
					if corrupt {
						_, _ = w.Write([]byte("corrupt"))
					} else {
						_, _ = w.Write(uploaded)
					}
				} else {
					t.Error("unexpected request")
				}
			})
			dir := t.TempDir()
			content := []byte("archive contents")
			digest := sha256.Sum256(content)
			m := Manifest{Format: 1, ID: "20261005T190000Z-0000000000000000", Database: "ourtaiko_fanmade", Bytes: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
			if err := os.WriteFile(filepath.Join(dir, DumpName), content, 0600); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(filepath.Join(dir, ManifestName), m); err != nil {
				t.Fatal(err)
			}
			err := s.Publish(context.Background(), dir, m)
			if corrupt {
				if err == nil || len(calls) != 2 {
					t.Fatalf("corrupt backup published: %v %v", err, calls)
				}
			} else if err != nil || strings.Join(calls, ",") != "PUT database.dump,GET database.dump,PUT manifest.json" {
				t.Fatalf("wrong publish sequence: %v %v", err, calls)
			}
		})
	}
}
func TestS3RetentionOnlyDeletesExpiredBackupFiles(t *testing.T) {
	prefix := "fanmade/backups/postgresql/ourtaiko_fanmade/"
	old := "20260101T000000Z-0000000000000000/"
	var deleted []string
	listed := 0
	s := testS3(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/test/"))
			return
		}
		if r.Method != "GET" || r.URL.Query().Get("prefix") != prefix {
			t.Error("unscoped listing")
			w.WriteHeader(400)
			return
		}
		listed++
		if listed == 1 {
			fmt.Fprintf(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken><Contents><Key>%s</Key></Contents></ListBucketResult>`, prefix+old+DumpName)
			return
		}
		if len(deleted) != 0 || r.URL.Query().Get("continuation-token") != "next" {
			t.Error("deletion before pagination finished")
		}
		keys := []string{prefix + old + ManifestName, prefix + old + "manual.txt", prefix + "20261005T190000Z-0000000000000000/" + DumpName, "fanmade/production/" + old + DumpName, prefix + "notes/database.dump"}
		_, _ = io.WriteString(w, "<ListBucketResult><IsTruncated>false</IsTruncated>")
		for _, key := range keys {
			fmt.Fprintf(w, "<Contents><Key>%s</Key></Contents>", key)
		}
		_, _ = io.WriteString(w, "</ListBucketResult>")
	})
	if err := s.Prune(context.Background(), time.Date(2026, 9, 5, 19, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 2 || deleted[0] != prefix+old+DumpName || deleted[1] != prefix+old+ManifestName {
		t.Fatalf("unsafe retention: %v", deleted)
	}
}

func TestS3BackupPrefixesExcludePublicResources(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	for _, tc := range []struct {
		prefix, database string
		allowed          bool
	}{
		{"fanmade/backups/postgresql", "ourtaiko_fanmade", true},
		{"sso/backups/postgresql/", "ourtaiko_sso", true},
		{"sso/avatars", "ourtaiko_sso", false},
		{"fanmade/objects", "ourtaiko_fanmade", false},
		{"fanmade/production", "ourtaiko_fanmade", false},
		{"sso/backups/../avatars", "ourtaiko_sso", false},
		{"sso/backups-copy/postgresql", "ourtaiko_sso", false},
		{"sso/backups/postgresql", "../ourtaiko_sso", false},
	} {
		t.Run(tc.prefix+"_"+tc.database, func(t *testing.T) {
			s, err := NewS3(context.Background(), "private-test", "ap-northeast-1", tc.prefix, tc.database)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%t, error=%v", tc.allowed, err)
			}
			if err == nil && s.prefix != strings.TrimSuffix(tc.prefix, "/")+"/"+tc.database+"/" {
				t.Fatalf("wrong database scope: %s", s.prefix)
			}
		})
	}
}
