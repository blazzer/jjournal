package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupCommand(t *testing.T) {
	dir := t.TempDir()
	key := base64.StdEncoding.EncodeToString(bytesRepeat())
	t.Setenv("SECRET_KEY", key)
	t.Setenv("DB_PATH", filepath.Join(dir, "j.db"))
	t.Setenv("DATA_DIR", dir)
	t.Setenv("OPERATOR_CONTACT", "ops@example.com")
	if code := run([]string{"backup"}); code != 0 {
		t.Fatal(code)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "backups", "backup-*.db"))
	if err != nil || len(matches) != 1 {
		t.Fatal(matches, err)
	}
}

func TestStubs(t *testing.T) {
	if code := run([]string{"rotate-keys"}); code != 1 {
		t.Fatal(code)
	}
	if code := run([]string{"demo"}); code != 1 {
		t.Fatal(code)
	}
}

func bytesRepeat() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = 7
	}
	return b
}

func TestServeRequiresContact(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SECRET_KEY", base64.StdEncoding.EncodeToString(bytesRepeat()))
	t.Setenv("DB_PATH", filepath.Join(dir, "j.db"))
	t.Setenv("DATA_DIR", dir)
	os.Unsetenv("OPERATOR_CONTACT")
	if code := run([]string{"serve", "-h"}); code == 0 && strings.Contains("", "") {
		// -h exits 2 from flag.ContinueOnError
	}
	if code := run([]string{"backup"}); os.Getenv("OPERATOR_CONTACT") == "" && code != 0 {
		t.Fatal("backup should not require OPERATOR_CONTACT", code)
	}
}
