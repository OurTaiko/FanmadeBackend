package objectstore

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

func Archive(dir, tjaPath, audioPath, tjaName, audioName string) (string, int64, string, error) {
	out, e := os.CreateTemp(dir, "archive-*.zip")
	if e != nil {
		return "", 0, "", e
	}
	good := false
	defer func() {
		out.Close()
		if !good {
			os.Remove(out.Name())
		}
	}()
	h := sha256.New()
	z := zip.NewWriter(io.MultiWriter(out, h))
	for _, v := range []struct{ path, name string }{{tjaPath, tjaName}, {audioPath, audioName}} {
		f, e := os.Open(v.path)
		if e != nil {
			return "", 0, "", e
		}
		dst, e := z.CreateHeader(&zip.FileHeader{Name: v.name, Method: zip.Store})
		if e == nil {
			_, e = io.Copy(dst, f)
		}
		e = errors.Join(e, f.Close())
		if e != nil {
			return "", 0, "", e
		}
	}
	if e = z.Close(); e != nil {
		return "", 0, "", e
	}
	st, e := out.Stat()
	if e != nil {
		return "", 0, "", e
	}
	if e = out.Close(); e != nil {
		return "", 0, "", e
	}
	good = true
	return out.Name(), st.Size(), hex.EncodeToString(h.Sum(nil)), nil
}
