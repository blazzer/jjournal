package store

import (
	"database/sql"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFormatTimeLiteralZ(t *testing.T) {
	in := time.Date(2024, 1, 2, 3, 4, 5, 6_000_000, time.FixedZone("CET", 3600))
	got := FormatTime(in)
	if got != "2024-01-02T02:04:05.006Z" {
		t.Fatalf("FormatTime = %q", got)
	}
	if strings.Contains(got, "+") || !strings.HasSuffix(got, "Z") {
		t.Fatal(got)
	}
	if FormatTime(time.Time{}) != "" {
		t.Fatal("zero")
	}
}

func TestParseTimeLegacyForms(t *testing.T) {
	nano := parseTime(sql.NullString{String: "2024-01-02T03:04:05.123456789Z", Valid: true})
	plain := parseTime(sql.NullString{String: "2024-01-02T03:04:05Z", Valid: true})
	milli := parseTime(sql.NullString{String: "2024-01-02T03:04:05.123Z", Valid: true})
	if nano.IsZero() || plain.IsZero() || milli.IsZero() {
		t.Fatalf("%v %v %v", nano, plain, milli)
	}
	if nano.Nanosecond() != 123456789 || milli.Nanosecond() != 123000000 {
		t.Fatalf("nano %d milli %d", nano.Nanosecond(), milli.Nanosecond())
	}
	if !plain.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatal(plain)
	}
}

func TestFormatTimeLexicalOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	times := make([]time.Time, 10000)
	span := 200 * 365 * 24 * int64(time.Hour/time.Millisecond)
	for i := range times {
		times[i] = time.UnixMilli(r.Int64N(span)).UTC()
	}
	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	for i := 1; i < len(times); i++ {
		cs := strings.Compare(FormatTime(times[i-1]), FormatTime(times[i]))
		ct := times[i-1].Compare(times[i])
		if cs != ct {
			t.Fatalf("%s vs %s (time %d string %d)", FormatTime(times[i-1]), FormatTime(times[i]), ct, cs)
		}
	}
}
