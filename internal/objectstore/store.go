// Package objectstore keeps permanent resources independent of the API host.
package objectstore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Object interface {
	io.ReadSeekCloser
	Size() int64
	ModTime() time.Time
}
type Store interface {
	Open(context.Context, string) (Object, error)
	Put(context.Context, string, io.ReadSeeker, int64, string, string) error
	Delete(context.Context, string) error
}

func ValidKey(key string) bool {
	return key != "" && filepath.IsLocal(key) && !strings.Contains(key, "\\") && filepath.ToSlash(filepath.Clean(key)) == key && key != "."
}

var ErrKey = errors.New("invalid object key")

type Local struct{ Root string }
type localObject struct {
	*os.File
	info os.FileInfo
}

func (f *localObject) Size() int64        { return f.info.Size() }
func (f *localObject) ModTime() time.Time { return f.info.ModTime() }
func (s Local) Open(_ context.Context, key string) (Object, error) {
	if !ValidKey(key) {
		return nil, ErrKey
	}
	root, e := os.OpenRoot(s.Root)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	f, e := root.Open(key)
	if e != nil {
		return nil, e
	}
	st, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("object is not a regular file")
	}
	return &localObject{f, st}, nil
}
func (s Local) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, media, digest string) error {
	if !ValidKey(key) {
		return ErrKey
	}
	root, e := os.OpenRoot(s.Root)
	if e != nil {
		return e
	}
	defer root.Close()
	if e = root.MkdirAll(filepath.Dir(key), 0700); e != nil {
		return e
	}
	f, e := root.OpenFile(key, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	n, e := io.Copy(f, io.LimitReader(body, size+1))
	e = errors.Join(e, f.Close())
	if e == nil && n != size {
		e = errors.New("object size mismatch")
	}
	if e != nil {
		_ = root.Remove(key)
	}
	return e
}
func (s Local) Delete(_ context.Context, key string) error {
	if !ValidKey(key) {
		return ErrKey
	}
	root, e := os.OpenRoot(s.Root)
	if e != nil {
		return e
	}
	defer root.Close()
	e = root.Remove(key)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if dir := filepath.Dir(key); strings.HasPrefix(dir, "objects/") {
		_ = root.Remove(dir)
	}
	return nil
}
