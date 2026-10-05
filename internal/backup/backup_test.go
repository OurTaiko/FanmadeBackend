package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeDump struct {
	dumpErr, validateErr error
	started, release     chan struct{}
}

func (d fakeDump) Dump(_ context.Context, _, path string) (string, error) {
	if d.started != nil {
		close(d.started)
		<-d.release
	}
	if d.dumpErr != nil {
		return "", d.dumpErr
	}
	return "sha256:test", os.WriteFile(path, []byte("complete archive"), 0600)
}
func (d fakeDump) Validate(context.Context, string, string) error { return d.validateErr }

type fakeRemote struct {
	publishErr        error
	published, pruned bool
}

func (s *fakeRemote) Publish(context.Context, string, Manifest) error {
	s.published = true
	return s.publishErr
}
func (s *fakeRemote) Prune(context.Context, time.Time) error { s.pruned = true; return nil }

func testRunner(t *testing.T) Runner {
	t.Helper()
	return Runner{Config: Config{"ourtaiko_fanmade", t.TempDir(), 30}, Dumper: fakeDump{}, Now: func() time.Time { return time.Date(2026, 10, 5, 19, 0, 0, 0, time.UTC) }}
}
func oldArchive(t *testing.T, r Runner, id, database string) string {
	t.Helper()
	p := filepath.Join(r.Config.Directory, id)
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(p, ManifestName), Manifest{Format: 1, ID: id, Database: database}); err != nil {
		t.Fatal(err)
	}
	return p
}
func readStatus(t *testing.T, r Runner) Status {
	t.Helper()
	var s Status
	b, err := os.ReadFile(filepath.Join(r.Config.Directory, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestSuccessfulBackupAndScopedRetention(t *testing.T) {
	r := testRunner(t)
	remote := &fakeRemote{}
	r.Remote = remote
	old := oldArchive(t, r, "20260101T000000Z-0000000000000000", r.Config.Database)
	other := oldArchive(t, r, "20260101T000000Z-0000000000000001", "another_database")
	boundary := oldArchive(t, r, "20260905T190000Z-0000000000000000", r.Config.Database)
	manual := oldArchive(t, r, "pre-migration", r.Config.Database)
	m, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !remote.published || !remote.pruned {
		t.Fatal("remote backup and retention required")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("expired backup not removed")
	}
	for _, p := range []string{other, boundary, manual} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("unrelated or retained backup removed: %s", p)
		}
	}
	dir := filepath.Join(r.Config.Directory, m.ID)
	digest, size, err := HashFile(filepath.Join(dir, DumpName))
	if err != nil {
		t.Fatal(err)
	}
	if digest != m.SHA256 || size != m.Bytes || size == 0 {
		t.Fatal("incorrect manifest")
	}
	for p, mode := range map[string]os.FileMode{r.Config.Directory: 0700, dir: 0700, filepath.Join(dir, DumpName): 0600, filepath.Join(dir, ManifestName): 0600, filepath.Join(r.Config.Directory, "status.json"): 0600} {
		s, err := os.Stat(p)
		if err != nil || s.Mode().Perm() != mode {
			t.Fatalf("unsafe permissions: %s", p)
		}
	}
	s := readStatus(t, r)
	if s.LastSuccess == nil || s.BackupID != m.ID || s.Error != "" {
		t.Fatalf("invalid status: %+v", s)
	}
}
func TestFailureNeverPrunesAndPreservesLastSuccess(t *testing.T) {
	for _, step := range []string{"dump", "validate", "upload"} {
		t.Run(step, func(t *testing.T) {
			r := testRunner(t)
			remote := &fakeRemote{}
			r.Remote = remote
			prior := r.Now().Add(-time.Hour)
			if err := writeJSON(filepath.Join(r.Config.Directory, "status.json"), Status{LastSuccess: &prior, BackupID: "previous"}); err != nil {
				t.Fatal(err)
			}
			old := oldArchive(t, r, "20260101T000000Z-0000000000000000", r.Config.Database)
			failure := errors.New("simulated failure")
			switch step {
			case "dump":
				r.Dumper = fakeDump{dumpErr: failure}
			case "validate":
				r.Dumper = fakeDump{validateErr: failure}
			case "upload":
				remote.publishErr = failure
			}
			m, err := r.Run(context.Background())
			if !errors.Is(err, failure) {
				t.Fatalf("expected failure, got %v", err)
			}
			if remote.pruned {
				t.Fatal("pruned after failure")
			}
			if _, err := os.Stat(old); err != nil {
				t.Fatal("removed old backup after failure")
			}
			entries, _ := os.ReadDir(r.Config.Directory)
			for _, e := range entries {
				if len(e.Name()) > 9 && e.Name()[:9] == ".partial-" {
					t.Fatal("left incomplete local backup")
				}
			}
			if step == "upload" {
				if _, err := os.Stat(filepath.Join(r.Config.Directory, m.ID, DumpName)); err != nil {
					t.Fatal("lost verified local copy")
				}
			} else if remote.published {
				t.Fatal("published invalid archive")
			}
			s := readStatus(t, r)
			if s.LastSuccess == nil || !s.LastSuccess.Equal(prior) || s.BackupID != "previous" || s.Error == "" {
				t.Fatalf("lost status history: %+v", s)
			}
		})
	}
}
func TestConcurrentBackupRejected(t *testing.T) {
	r := testRunner(t)
	d := fakeDump{started: make(chan struct{}), release: make(chan struct{})}
	r.Dumper = d
	finished := make(chan error, 1)
	go func() { _, err := r.Run(context.Background()); finished <- err }()
	<-d.started
	_, err := r.Run(context.Background())
	close(d.release)
	if err == nil {
		t.Error("concurrent backup was accepted")
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
