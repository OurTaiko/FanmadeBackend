// Package backup creates validated, private PostgreSQL archives independently of the API.
package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var archiveID = regexp.MustCompile(`^\d{8}T\d{6}Z-[a-f0-9]{16}$`)
var databaseName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

const DumpName = "database.dump"
const ManifestName = "manifest.json"

type Config struct {
	Database      string
	Directory     string
	RetentionDays int
}
type Manifest struct {
	Format        int       `json:"format"`
	ID            string    `json:"id"`
	Database      string    `json:"database"`
	CreatedAt     time.Time `json:"createdAt"`
	SHA256        string    `json:"sha256"`
	Bytes         int64     `json:"bytes"`
	PostgresImage string    `json:"postgresImage"`
}
type Status struct {
	LastAttempt time.Time  `json:"lastAttempt"`
	LastSuccess *time.Time `json:"lastSuccess,omitempty"`
	BackupID    string     `json:"backupId,omitempty"`
	Error       string     `json:"error,omitempty"`
}
type Dumper interface {
	Dump(context.Context, string, string) (string, error)
	Validate(context.Context, string, string) error
}
type Remote interface {
	// Publish uploads and reads back the dump before publishing the manifest marker.
	Publish(context.Context, string, Manifest) error
	Prune(context.Context, time.Time) error
}
type Runner struct {
	Config Config
	Dumper Dumper
	Remote Remote
	Now    func() time.Time
}

func (r Runner) Run(ctx context.Context) (m Manifest, result error) {
	c := r.Config
	if !databaseName.MatchString(c.Database) || !filepath.IsAbs(c.Directory) || strings.ContainsAny(c.Directory, ",\n\r") || filepath.Clean(c.Directory) == string(filepath.Separator) || c.RetentionDays < 1 || c.RetentionDays > 3650 || r.Dumper == nil {
		return m, errors.New("invalid backup configuration")
	}
	if err := os.MkdirAll(c.Directory, 0700); err != nil {
		return m, err
	}
	if err := os.Chmod(c.Directory, 0700); err != nil {
		return m, err
	}
	lock, err := os.OpenFile(filepath.Join(c.Directory, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return m, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return m, errors.New("another database backup is running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	var state Status
	raw, _ := os.ReadFile(filepath.Join(c.Directory, "status.json"))
	_ = json.Unmarshal(raw, &state)
	state.LastAttempt = now
	state.Error = ""
	defer func() {
		if result != nil {
			state.Error = result.Error()
		}
		if err := writeJSON(filepath.Join(c.Directory, "status.json"), state); err != nil {
			result = errors.Join(result, fmt.Errorf("write backup status: %w", err))
		}
	}()
	nonce := make([]byte, 8)
	if _, err = rand.Read(nonce); err != nil {
		return m, err
	}
	id := now.Format("20060102T150405Z") + "-" + hex.EncodeToString(nonce)
	stage := filepath.Join(c.Directory, ".partial-"+id)
	if err = os.Mkdir(stage, 0700); err != nil {
		return m, err
	}
	defer os.RemoveAll(stage)
	dump := filepath.Join(stage, DumpName)
	image, err := r.Dumper.Dump(ctx, c.Database, dump)
	if err != nil {
		return m, err
	}
	if err = r.Dumper.Validate(ctx, stage, image); err != nil {
		return m, err
	}
	digest, size, err := HashFile(dump)
	if err != nil {
		return m, err
	}
	if size == 0 {
		return m, errors.New("empty database archive")
	}
	m = Manifest{Format: 1, ID: id, Database: c.Database, CreatedAt: now, SHA256: digest, Bytes: size, PostgresImage: image}
	if err = writeJSON(filepath.Join(stage, ManifestName), m); err != nil {
		return m, err
	}
	// Retain a verified local copy even if S3 is unavailable. Never prune on failure.
	final := filepath.Join(c.Directory, id)
	if err = os.Rename(stage, final); err != nil {
		return m, err
	}
	if err = syncDir(c.Directory); err != nil {
		return m, err
	}
	if r.Remote != nil {
		if err = r.Remote.Publish(ctx, final, m); err != nil {
			return m, err
		}
	}
	state.LastSuccess = &now
	state.BackupID = id
	cutoff := now.AddDate(0, 0, -c.RetentionDays)
	if r.Remote != nil {
		if err = r.Remote.Prune(ctx, cutoff); err != nil {
			return m, err
		}
	}
	if err = pruneLocal(c.Directory, c.Database, cutoff); err != nil {
		return m, err
	}
	return m, nil
}
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}
func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".metadata-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func pruneLocal(root, database string, cutoff time.Time) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || !oldID(e.Name(), cutoff) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
		if err != nil {
			continue
		}
		var m Manifest
		if json.Unmarshal(raw, &m) != nil || m.Format != 1 || m.ID != e.Name() || m.Database != database {
			continue
		}
		if err = os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}
func oldID(id string, cutoff time.Time) bool {
	if !archiveID.MatchString(id) {
		return false
	}
	t, err := time.Parse("20060102T150405Z", strings.SplitN(id, "-", 2)[0])
	return err == nil && t.Before(cutoff)
}
