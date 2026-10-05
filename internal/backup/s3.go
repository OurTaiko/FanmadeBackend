package backup

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type S3 struct {
	client         *s3.Client
	bucket, prefix string
}

func NewS3(ctx context.Context, bucket, region, prefix, database string) (*S3, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	// Keep backup retention outside production resource objects, even after misconfiguration.
	if bucket == "" || region == "" || !strings.HasPrefix(prefix, "fanmade/backups/") || strings.Contains(prefix, "..") || strings.ContainsAny(prefix, "\\,\n\r") || !databaseName.MatchString(database) {
		return nil, errors.New("S3 backup requires bucket, region and a prefix under fanmade/backups/")
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, errors.New("load S3 backup configuration failed")
	}
	return &S3{s3.NewFromConfig(cfg), bucket, prefix + "/" + database + "/"}, nil
}
func (s *S3) Publish(ctx context.Context, dir string, m Manifest) error {
	if err := s.put(ctx, filepath.Join(dir, DumpName), m.ID+"/"+DumpName, m.SHA256, m.Bytes, "application/octet-stream"); err != nil {
		return err
	}
	obj, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.prefix + m.ID + "/" + DumpName)})
	if err != nil {
		return errors.New("S3 backup readback failed")
	}
	h := sha256.New()
	n, err := io.Copy(h, obj.Body)
	closeErr := obj.Body.Close()
	if err != nil || closeErr != nil || n != m.Bytes || hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		return errors.New("S3 backup readback checksum mismatch")
	}
	manifest := filepath.Join(dir, ManifestName)
	digest, size, err := HashFile(manifest)
	if err != nil {
		return err
	}
	return s.put(ctx, manifest, m.ID+"/"+ManifestName, digest, size, "application/json")
}
func (s *S3) put(ctx context.Context, path, key, digest string, size int64, media string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sum, err := hex.DecodeString(digest)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.prefix + key), Body: f, ContentLength: aws.Int64(size), ContentType: aws.String(media), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum)), ServerSideEncryption: types.ServerSideEncryptionAes256, Metadata: map[string]string{"sha256": digest}, IfNoneMatch: aws.String("*")})
	if err != nil {
		return errors.New("S3 backup upload failed")
	}
	return nil
}
func (s *S3) Prune(ctx context.Context, cutoff time.Time) error {
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(s.prefix)})
	// List before deleting: do not mutate the collection while paginating it.
	keys := []string{}
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return errors.New("S3 backup retention listing failed")
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			relative := strings.TrimPrefix(key, s.prefix)
			parts := strings.Split(relative, "/")
			if len(parts) == 2 && oldID(parts[0], cutoff) && (parts[1] == DumpName || parts[1] == ManifestName) && strings.HasPrefix(key, s.prefix) {
				keys = append(keys, key)
			}
		}
	}
	for _, key := range keys {
		if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil {
			return errors.New("S3 backup retention deletion failed")
		}
	}
	return nil
}
