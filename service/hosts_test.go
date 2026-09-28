package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostsStayInSpokes(t *testing.T) {
	needles := []string{"livejournal.com", "dreamwidth.org", "rossia.org"}
	root := ".."
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata", "lj", "service":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(b)
		for _, n := range needles {
			if strings.Contains(text, n) {
				t.Errorf("%s contains %s", path, n)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
