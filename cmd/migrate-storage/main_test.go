package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"ourtaiko.dev/fanmade/api/internal/teststore"
	"strings"
	"testing"
)

func TestCopyIsVerifiedAndNeverOverwrites(t *testing.T) {
	store := teststore.New(t)
	m := migrator{ctx: context.Background(), store: store, apply: true}
	data := []byte("original")
	v := entry{Key: "objects/test", Size: int64(len(data)), SHA: fmt.Sprintf("%x", sha256.Sum256(data)), Media: "application/octet-stream"}
	for range 2 {
		if e := m.copy(v, bytes.NewReader(data)); e != nil {
			t.Fatal(e)
		}
	}
	if m.copied != 1 || m.verified != 2 {
		t.Fatal("retry duplicated writes")
	}
	bad := v
	bad.SHA = strings.Repeat("0", 64)
	if e := m.copy(bad, bytes.NewReader(data)); e == nil {
		t.Fatal("source corruption accepted")
	}
	different := []byte("changed!")
	v.SHA = fmt.Sprintf("%x", sha256.Sum256(different))
	if e := m.copy(v, bytes.NewReader(different)); e == nil {
		t.Fatal("destination mismatch accepted")
	}
	f, e := store.Open(m.ctx, v.Key)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	actual, e := io.ReadAll(f)
	if e != nil || !bytes.Equal(actual, data) {
		t.Fatal("existing object overwritten", e)
	}
}
