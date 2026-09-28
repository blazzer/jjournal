package render

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEvictOldestFirst(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old")
	mid := filepath.Join(dir, "mid")
	neu := filepath.Join(dir, "new")
	for _, name := range []string{old, mid, neu} {
		if err := os.WriteFile(name, []byte("0123456789"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(mid, base.Add(time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(neu, base.Add(2*time.Minute), base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := Evict([]string{dir}, 15); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old file kept", err)
	}
	if _, err := os.Stat(mid); !os.IsNotExist(err) {
		t.Fatal("mid file kept", err)
	}
	if _, err := os.Stat(neu); err != nil {
		t.Fatal(err)
	}
}
