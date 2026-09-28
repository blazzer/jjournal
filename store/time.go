package store

import (
	"database/sql"
	"strings"
	"time"
)

// FormatTime formats t as fixed-width UTC text: YYYY-MM-DDTHH:MM:SS.sssZ.
// The trailing Z is a literal in the Go layout, not a zone placeholder.
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func parseTime(v sql.NullString) time.Time {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return time.Time{}
	}
	s := strings.TrimSpace(v.String)
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}
		}
	}
	return t.UTC()
}
