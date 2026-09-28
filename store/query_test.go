package store

import (
	"strings"
	"testing"
)

func TestQueryPlaceholders(t *testing.T) {
	check := func(name, q string, args []any) {
		t.Helper()
		if strings.Count(q, "?") != len(args) {
			t.Fatalf("%s placeholders %d args %d", name, strings.Count(q, "?"), len(args))
		}
	}
	q, args := friendsQuery(1, 0, 0, 20)
	check("friends", q, args)
	q, args = journalQuery(1, "ada", 0, 20)
	check("journal", q, args)
}
