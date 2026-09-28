package metrics_test

import (
	"bytes"
	"os"
	"testing"

	"journal/metrics"
)

func TestExpositionGolden(t *testing.T) {
	r := metrics.New()
	r.AddCounter("http_requests_total", "HTTP requests.", 2, "route", "/login", "status", "200")
	r.AddCounter("sync_runs_total", "Sync runs.", 1, "service", "livejournal", "result", "ok")
	r.SetGauge("sync_freshness_seconds", "Age of the last good sync.", 30, "account", "1")
	r.AddCounter("outbound_requests_total", "Outbound requests.", 4, "host_class", "api", "status", "200")
	r.SetGauge("paused_hosts", "Hosts currently paused.", 0)
	r.SetGauge("cache_bytes", "Image cache size.", 1024)
	r.SetGauge("database_bytes", "SQLite file size.", 4096)
	r.SetGauge("backup_last_success_timestamp", "Unix time of the last successful backup.", 10)
	r.Observe("http_request_duration_seconds", "HTTP request latency.", 0.02, "route", "/login")
	var buf bytes.Buffer
	if _, err := r.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	path := "testdata/exposition.golden"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(want) {
		t.Fatalf("golden mismatch\n%s", buf.String())
	}
}
