package tja

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Reads the user's reference corpus in place; never copies or modifies it.
func TestESECorpus(t *testing.T) {
	root := os.Getenv("ESE_ROOT")
	if root == "" {
		t.Skip("set ESE_ROOT to test local reference charts")
	}
	count, supported, multi := 0, 0, 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".tja") {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		count++
		wave := ""
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "WAVE:") {
				wave = strings.TrimSpace(strings.TrimPrefix(line, "WAVE:"))
				break
			}
		}
		_, issue := Parse(b, "utf-8", wave)
		if strings.Contains(string(b), "#NEXTSONG") {
			multi++
			if issue == nil || issue.Code != "TJA_RESOURCE_UNSUPPORTED" {
				t.Errorf("expected multi-audio rejection: %s: %v", path, issue)
			}
			return nil
		}
		if strings.HasSuffix(filepath.ToSlash(path), "02 Anime/Together/Together.tja") {
			if issue == nil || issue.Code != "TJA_STRUCTURE_INVALID" {
				t.Errorf("expected known missing #START: %v", issue)
			}
			return nil
		}
		if issue != nil {
			t.Errorf("reference rejected: %s: %v", path, issue)
		} else {
			supported++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("no ESE TJA files found")
	}
	t.Logf("ESE: %d total, %d single-audio accepted, %d multi-audio deferred", count, supported, multi)
}
