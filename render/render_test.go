package render

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestHostileHTML(t *testing.T) {
	raw, err := os.ReadFile("testdata/hostile.html")
	if err != nil {
		t.Fatal(err)
	}
	out := RenderBody(string(raw), Options{
		ReadMoreURL: "/~alice/1.html",
		LocalUsers:  map[string]string{"alice": "/~alice/profile"},
		SignImage: func(src string) string {
			if src == "https://cdn.example/pic.png" {
				return "/img?u=signed"
			}
			return ""
		},
	})
	for _, bad := range []string{"<script", "onclick", "javascript:", "evil.example", "position"} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(bad)) {
			t.Fatalf("output contains %q:\n%s", bad, out)
		}
	}
	for _, good := range []string{"hello", "youtube.com/embed/abc123", "player.vimeo.com/video/99", "Read more", "secret cut text", `<details class="cut">`, "/~alice/profile", "/static/userhead.svg", "/img?u=signed", "overlay"} {
		if !strings.Contains(out, good) {
			t.Fatalf("missing %q in %s", good, out)
		}
	}
	full := RenderBody(string(raw), Options{Full: true, LocalUsers: map[string]string{}})
	if !strings.Contains(full, "secret cut text") {
		t.Fatal(full)
	}
	again := Sanitize(Sanitize(string(raw)))
	if !strings.Contains(again, "lj-cut") && !strings.Contains(again, "secret cut text") {
		t.Fatal("cut did not survive sanitizer")
	}
}

func TestHasText(t *testing.T) {
	if HasText("<script>x</script>") {
		t.Fatal("script text should be skipped")
	}
	if !HasText("<p>hello</p>") {
		t.Fatal()
	}
	if HasText("   ") {
		t.Fatal()
	}
}

func TestCommentsThread(t *testing.T) {
	nodes := Thread([]Comment{
		{ID: 1, Author: "a"},
		{ID: 2, ParentID: 1, Author: "b"},
		{ID: 3, ParentID: 2, Author: "c"},
		{ID: 4, ParentID: 99, Author: "orphan"},
		{ID: 5, ParentID: 6, Author: "cyc"},
		{ID: 6, ParentID: 5, Author: "cyc2"},
	})
	if len(nodes) < 2 {
		t.Fatalf("%d roots", len(nodes))
	}
	var depth3 bool
	var walk func([]*CommentNode)
	walk = func(ns []*CommentNode) {
		for _, n := range ns {
			if n.ID == 3 && n.Depth == 3 {
				depth3 = true
			}
			walk(n.Children)
		}
	}
	walk(nodes)
	if !depth3 {
		t.Fatal("depth")
	}
	if !ShouldCollapse(5) || ShouldCollapse(4) {
		t.Fatal("collapse threshold")
	}
}

func TestPublicIP(t *testing.T) {
	bad := []string{"127.0.0.1", "10.1.2.3", "192.168.0.1", "172.16.0.1", "169.254.169.254", "100.64.0.1", "::1", "0.0.0.0"}
	for _, s := range bad {
		if PublicIP(net.ParseIP(s)) {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1"} {
		if !PublicIP(net.ParseIP(s)) {
			t.Fatal(s)
		}
	}
}

func TestProxySignAndCache(t *testing.T) {
	gif := []byte{
		0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00,
		0xff, 0xff, 0xff, 0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00,
		0x01, 0x00, 0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3b,
	}
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/gif")
		w.Write(gif)
	}))
	defer srv.Close()
	key := make([]byte, 32)
	key[0] = 7
	p := &Proxy{Key: key, Dir: t.TempDir(), AllowPrivate: true, Client: srv.Client()}
	t.Cleanup(func() { p.Close() })
	if _, err := p.Sign("http://127.0.0.1/x.png"); err == nil {
		// AllowPrivate permits signing loopback.
	}
	signed, err := p.Sign(srv.URL + "/a.gif")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Sign("javascript:alert(1)"); err == nil {
		t.Fatal("signed javascript")
	}
	req := httptest.NewRequest(http.MethodGet, signed+"tampered", nil)
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatal(rr.Code)
	}
	req = httptest.NewRequest(http.MethodGet, signed, nil)
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Header().Get("Content-Type"), "gif") && len(rr.Body.Bytes()) != len(gif) {
		t.Fatalf("%d %s %d", rr.Code, rr.Header().Get("Content-Type"), rr.Body.Len())
	}
	req = httptest.NewRequest(http.MethodGet, signed, nil)
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if hits != 1 {
		t.Fatalf("cache miss hits=%d", hits)
	}
	local, err := p.Download(context.Background(), srv.URL+"/a.gif")
	if err != nil || !strings.HasPrefix(local, "/userpics/") {
		t.Fatal(local, err)
	}
}

func TestRejectPrivateSign(t *testing.T) {
	p := &Proxy{Key: make([]byte, 32), Dir: t.TempDir()}
	if _, err := p.Sign("http://127.0.0.1/a.png"); err == nil {
		t.Fatal("signed loopback")
	}
	if _, err := p.Sign("http://169.254.169.254/latest"); err == nil {
		t.Fatal("signed metadata")
	}
}
