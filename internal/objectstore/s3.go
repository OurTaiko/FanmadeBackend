package objectstore

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type S3 struct {
	Client         *s3.Client
	Bucket, Prefix string
}

func NewS3(ctx context.Context, bucket, region, prefix string) (*S3, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	if bucket == "" || region == "" || !ValidKey(prefix) {
		return nil, errors.New("S3 bucket, region and nonempty object prefix required")
	}
	cfg, e := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if e != nil {
		return nil, e
	}
	return &S3{Client: s3.NewFromConfig(cfg), Bucket: bucket, Prefix: prefix + "/"}, nil
}
func (s *S3) key(key string) (*string, error) {
	if !ValidKey(key) {
		return nil, ErrKey
	}
	return aws.String(s.Prefix + key), nil
}
func (s *S3) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, media, digest string) error {
	k, e := s.key(key)
	if e != nil {
		return e
	}
	sum, e := hex.DecodeString(digest)
	if e != nil || len(sum) != 32 {
		return errors.New("SHA-256 required")
	}
	_, e = s.Client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.Bucket), Key: k, Body: body, ContentLength: aws.Int64(size), ContentType: aws.String(media), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum)), Metadata: map[string]string{"sha256": digest}, IfNoneMatch: aws.String("*")})
	return e
}
func (s *S3) Delete(ctx context.Context, key string) error {
	k, e := s.key(key)
	if e != nil {
		return e
	}
	_, e = s.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.Bucket), Key: k})
	return e
}
func (s *S3) Open(ctx context.Context, key string) (Object, error) {
	k, e := s.key(key)
	if e != nil {
		return nil, e
	}
	h, e := s.Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.Bucket), Key: k})
	if e != nil {
		var api smithy.APIError
		if errors.As(e, &api) && (api.ErrorCode() == "NotFound" || api.ErrorCode() == "NoSuchKey") {
			return nil, os.ErrNotExist
		}
		return nil, e
	}
	return &remoteObject{ctx: ctx, store: s, key: k, size: aws.ToInt64(h.ContentLength), modified: aws.ToTime(h.LastModified), etag: h.ETag}, nil
}

// Each seek opens a new ranged GET lazily; no whole-file memory or disk cache.
// Pin the observed ETag so replacing an object cannot mix bytes across reads.
type remoteObject struct {
	ctx       context.Context
	store     *S3
	key, etag *string
	size, pos int64
	modified  time.Time
	body      io.ReadCloser
	closed    bool
}

func (f *remoteObject) Size() int64        { return f.size }
func (f *remoteObject) ModTime() time.Time { return f.modified }
func (f *remoteObject) Close() error {
	f.closed = true
	if f.body != nil {
		return f.body.Close()
	}
	return nil
}
func (f *remoteObject) Seek(offset int64, whence int) (int64, error) {
	if f.closed {
		return 0, os.ErrClosed
	}
	next := offset
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		next += f.pos
	case io.SeekEnd:
		next += f.size
	default:
		return f.pos, errors.New("invalid seek origin")
	}
	if next < 0 {
		return f.pos, errors.New("negative seek")
	}
	if next != f.pos && f.body != nil {
		_ = f.body.Close()
		f.body = nil
	}
	f.pos = next
	return next, nil
}
func (f *remoteObject) Read(p []byte) (int, error) {
	if f.closed {
		return 0, os.ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if f.pos >= f.size {
		return 0, io.EOF
	}
	if f.body == nil {
		o, e := f.store.Client.GetObject(f.ctx, &s3.GetObjectInput{Bucket: aws.String(f.store.Bucket), Key: f.key, Range: aws.String(fmt.Sprintf("bytes=%d-", f.pos)), IfMatch: f.etag})
		if e != nil {
			return 0, e
		}
		f.body = o.Body
	}
	n, e := f.body.Read(p)
	f.pos += int64(n)
	return n, e
}
func (s *S3) Sign(ctx context.Context, key, method, name, media string, lifetime time.Duration) (string, error) {
	k, e := s.key(key)
	if e != nil {
		return "", e
	}
	if lifetime <= 0 || lifetime > time.Hour {
		return "", errors.New("invalid URL lifetime")
	}
	signer := s3.NewPresignClient(s.Client)
	options := func(o *s3.PresignOptions) { o.Expires = lifetime }
	if method == "HEAD" {
		v, e := signer.PresignHeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.Bucket), Key: k}, options)
		if e != nil {
			return "", e
		}
		return v.URL, nil
	}
	if method != "GET" {
		return "", errors.New("unsupported download method")
	}
	v, e := signer.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: k, ResponseContentType: aws.String(media), ResponseContentDisposition: aws.String(mime.FormatMediaType("inline", map[string]string{"filename": name})), ResponseCacheControl: aws.String("private, max-age=0")}, options)
	if e != nil {
		return "", e
	}
	return v.URL, nil
}
