package outbound

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoBareHTTPClient(t *testing.T) {
	root := ".."
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata", "outbound":
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
		if strings.Contains(text, "http.Client{") || strings.Contains(text, "http.DefaultClient") {
			t.Errorf("%s constructs an HTTP client outside outbound", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
