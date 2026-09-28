package classic_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"journal/lj"
	"journal/store"
	"journal/web"
	"journal/web/classic"
)

func TestConfigFrom(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	env := map[string]string{"SECRET_KEY": key}
	cfg, err := web.ConfigFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SiteName != "Journal" || cfg.LJSource != "xmlrpc" || cfg.ListenAddr != ":8080" {
		t.Fatalf("%+v", cfg)
	}
	if _, err := web.ConfigFrom(func(string) string { return "" }); err == nil {
		t.Fatal("missing key")
	}
	env["LJ_SOURCE"] = "nope"
	if _, err := web.ConfigFrom(func(k string) string { return env[k] }); err == nil {
		t.Fatal("bad source")
	}
}

func TestParseUserPath(t *testing.T) {
	cases := []struct {
		path string
		user string
		kind string
		id   int64
		ok   bool
	}{
		{"/~bob/friends", "bob", "friends", 0, true},
		{"/~bob/", "bob", "journal", 0, true},
		{"/~bob", "bob", "journal", 0, true},
		{"/~Bob/15.html", "bob", "entry", 15, true},
		{"/~bob/profile", "bob", "profile", 0, true},
		{"/bob/friends", "", "", 0, false},
		{"/~bob/../x", "", "", 0, false},
		{"/~bob/nope", "", "", 0, false},
	}
	for _, tc := range cases {
		user, kind, id, ok := web.ParseUserPath(tc.path)
		if user != tc.user || kind != tc.kind || id != tc.id || ok != tc.ok {
			t.Fatalf("%s -> %s %s %d %v", tc.path, user, kind, id, ok)
		}
	}
}

func TestSiteFlow(t *testing.T) {
	st, srv, ts, client := testApp(t)
	defer ts.Close()
	ctx := context.Background()
	pw := lj.PasswordMD5("hello-password")

	res := get(t, client, ts.URL+"/login")
	token := csrfToken(t, readBody(t, res))
	bad := post(t, client, ts.URL+"/login", url.Values{"username": {"ada"}, "password": {"hello-password"}})
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatal(bad.StatusCode)
	}
	res = post(t, client, ts.URL+"/login", url.Values{
		"csrf": {token}, "username": {"ada"}, "password": {"hello-password"},
	})
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "/~ada/friends") {
		t.Fatal(res.StatusCode, res.Header.Get("Location"))
	}
	page := readBody(t, get(t, client, ts.URL+"/~ada/friends"))
	if !strings.Contains(page, "No entries.") {
		t.Fatal(page)
	}
	base := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	ada, err := st.UserByUsername(ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 21; i++ {
		_, err := st.UpsertLJEntry(ctx, ada.ID, store.LJEntryIn{
			ItemID: int64(100 + i), Journal: "carol", Author: "carol",
			Subject: "Item", BodyHTML: "<p>hello</p><script>alert(1)</script>",
			Security: "public", EventTime: base.Add(time.Duration(i) * time.Minute),
			URL: "https://carol.livejournal.com/1.html", CommentCount: 4,
			JournalType: "P",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = st.UpsertLJEntry(ctx, ada.ID, store.LJEntryIn{
		ItemID: 9, Journal: "comm", Author: "carol", Subject: "Group post",
		BodyHTML: "<p>in group</p>", Security: "public", JournalType: "C",
		EventTime: base.Add(2 * time.Hour), URL: "https://comm.livejournal.com/9.html",
	})
	if err != nil {
		t.Fatal(err)
	}
	page = readBody(t, get(t, client, ts.URL+"/~ada/friends"))
	for _, want := range []string{"via LJ", "4 comments on LJ", "Next 20", "in community", "comm"} {
		if !strings.Contains(page, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(page, "<script") {
		t.Fatal("script leaked")
	}
	page = readBody(t, get(t, client, ts.URL+"/~ada/friends?skip=20"))
	if !strings.Contains(page, "Previous 20") {
		t.Fatal("missing previous")
	}

	if err := st.MarkAuthFailed(ctx, ada.ID, "authentication failed"); err != nil {
		t.Fatal(err)
	}
	page = readBody(t, get(t, client, ts.URL+"/~ada/friends"))
	if !strings.Contains(page, "Log in again") {
		t.Fatal("missing banner")
	}

	bobHash := lj.PasswordMD5("other-password")
	srv.Source.(*lj.Fake).Password["bob"] = bobHash
	jar, _ := cookiejar.New(nil)
	bobClient := &http.Client{Jar: jar, CheckRedirect: stopRedirect}
	loginAs(t, bobClient, ts.URL, "bob", "other-password")
	res = get(t, bobClient, ts.URL+"/admin")
	if res.StatusCode != http.StatusForbidden {
		t.Fatal(res.StatusCode)
	}
	admin := readBody(t, get(t, client, ts.URL+"/admin"))
	if !strings.Contains(admin, "auth_failed") || strings.Contains(admin, pw) {
		t.Fatal("admin page")
	}

	update := readBody(t, get(t, client, ts.URL+"/update"))
	token = csrfToken(t, update)
	res = post(t, client, ts.URL+"/update", url.Values{
		"csrf": {token}, "subject": {"Hello"}, "body": {"<p>native post</p><script>no</script>"},
		"security": {"public"}, "mood": {"glad"}, "music": {"song"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatal(res.StatusCode, readBody(t, res))
	}
	loc := res.Header.Get("Location")
	entry := readBody(t, get(t, client, ts.URL+loc))
	if strings.Contains(entry, "<script") || !strings.Contains(entry, "native post") || !strings.Contains(entry, "Leave a comment") {
		t.Fatal(entry)
	}
	token = csrfToken(t, entry)
	res = post(t, client, ts.URL+loc, url.Values{"csrf": {token}, "body": {"<p>first note</p>"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatal(res.StatusCode)
	}
	entry = readBody(t, get(t, client, ts.URL+loc))
	if !strings.Contains(entry, "first note") {
		t.Fatal(entry)
	}
	token = csrfToken(t, entry)
	res = post(t, client, ts.URL+loc, url.Values{"csrf": {token}, "body": {"<b onclick=\"x\">reply</b>"}, "parent_id": {"1"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatal(res.StatusCode)
	}
	entry = readBody(t, get(t, client, ts.URL+loc))
	if strings.Contains(entry, "onclick") || !strings.Contains(entry, "reply") || !strings.Contains(entry, "Continue this thread") && !strings.Contains(entry, "Reply") {
		t.Fatal(entry)
	}
	_ = pw
}

func testApp(t *testing.T) (*store.Store, *web.Server, *httptest.Server, *http.Client) {
	t.Helper()
	key := bytes.Repeat([]byte{9}, 32)
	st, err := store.Open(t.TempDir()+"/t.db", key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fake := &lj.Fake{Password: map[string]string{"ada": lj.PasswordMD5("hello-password")}}
	front, err := classic.New()
	if err != nil {
		t.Fatal(err)
	}
	h, err := web.New(web.Config{
		Secret: key, BaseURL: "http://127.0.0.1:8080", SiteName: "Journal",
		LJSource: "xmlrpc", UserpicDir: t.TempDir(), ImageDir: t.TempDir(),
	}, st, fake, nil, nil, nil, front)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: stopRedirect}
	return st, h, ts, client
}

func stopRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func get(t *testing.T, c *http.Client, raw string) *http.Response {
	t.Helper()
	res, err := c.Get(raw)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func post(t *testing.T, c *http.Client, raw string, form url.Values) *http.Response {
	t.Helper()
	res, err := c.PostForm(raw, form)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func csrfToken(t *testing.T, body string) string {
	t.Helper()
	const needle = `name="csrf" value="`
	i := strings.Index(body, needle)
	if i < 0 {
		t.Fatal("no csrf")
	}
	rest := body[i+len(needle):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatal("csrf")
	}
	return rest[:j]
}

func loginAs(t *testing.T, c *http.Client, base, user, password string) {
	t.Helper()
	page := readBody(t, get(t, c, base+"/login"))
	res := post(t, c, base+"/login", url.Values{
		"csrf": {csrfToken(t, page)}, "username": {user}, "password": {password},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatal(res.StatusCode, readBody(t, res))
	}
	res.Body.Close()
}

func TestDigestDisabledRedirect(t *testing.T) {
	_, srv, ts, client := testApp(t)
	defer ts.Close()
	srv.Source.(*lj.Fake).LoginErr = &lj.DigestDisabledError{Message: "Digest authentication <b>FAILED</b>!"}
	page := readBody(t, get(t, client, ts.URL+"/login"))
	res := post(t, client, ts.URL+"/login", url.Values{
		"csrf": {csrfToken(t, page)}, "username": {"ada"}, "password": {"hello-password"},
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != lj.DigestManageURL {
		t.Fatalf("%d %s", res.StatusCode, res.Header.Get("Location"))
	}
}
