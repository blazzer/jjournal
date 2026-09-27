package journal_test

import (
	"os"
	"strings"
	"testing"
)

func TestMilestone6Files(t *testing.T) {
	for _, name := range []string{"Dockerfile", "docker-compose.yml", "Caddyfile", "README.md"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(name, err)
		}
		if len(b) < 20 {
			t.Fatal(name, "too small")
		}
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(readme)
	for _, needle := range []string{"SECRET_KEY", "docker compose", "SITE_NAME"} {
		if !strings.Contains(text, needle) {
			t.Fatal("readme missing", needle)
		}
	}
}
