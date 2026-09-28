package webtest

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"journal/app"
	"journal/lj"
	"journal/store"
	"journal/web"
	"journal/web/classic"
)

func TestClassicContract(t *testing.T) {
	Run(t, func() (web.FrontEnd, error) { return classic.New() })
}

func TestEditSettingsOfOther(t *testing.T) {
	a := app.Viewer{ID: 2, Handle: "a"}
	if err := app.EditSettings(a, "b"); err == nil {
		t.Fatal("expected forbidden")
	}
	if err := app.EditSettings(a, "a"); err != nil {
		t.Fatal(err)
	}
	admin := app.Viewer{ID: 1, Handle: "d", IsAdmin: true}
	if err := app.EditSettings(admin, "a"); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizationMatrix(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	st, err := store.Open(t.TempDir()+"/t.db", key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := t.Context()
	pass := map[string]string{"d": "pw-d", "a": "pw-a", "b": "pw-b", "c": "pw-c"}
	fake := &lj.Fake{Password: map[string]string{}}
	users := map[string]store.User{}
	for _, name := range []string{"d", "a", "b", "c"} {
		fake.Password[name] = lj.PasswordMD5(pass[name])
		u, err := st.UpsertLogin(ctx, name, name, fake.Password[name], "cookie-"+name)
		if err != nil {
			t.Fatal(err)
		}
		users[name] = u
	}
	group, err := st.CreateNativeGroup(ctx, users["a"].ID, "Circle")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddNativeFriend(ctx, users["a"].ID, users["b"].ID, store.Mask(group.Bit)); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	mk := func(sec string, mask uint32) int64 {
		id, err := st.CreateNativeEntry(ctx, users["a"].ID, store.LJEntryIn{
			Subject: sec, BodyHTML: "<p>" + sec + "</p>", Security: sec, AllowMask: mask, EventTime: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
		return id
	}
	ids := map[string]int64{
		"public":  mk("public", 0),
		"friends": mk("friends", 1),
		"private": mk("private", 0),
		"custom":  mk("custom", store.Mask(group.Bit)),
	}
	comments := map[string]int64{}
	for sec, id := range ids {
		cid, err := st.AddComment(ctx, id, 0, users["a"].ID, "<p>note</p>", now)
		if err != nil {
			t.Fatal(sec, err)
		}
		comments[sec] = cid
	}
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
	defer ts.Close()

	clients := map[string]*http.Client{}
	for _, name := range []string{"d", "a", "b", "c"} {
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		login(t, c, ts.URL, name, pass[name])
		clients[name] = c
	}

	view := map[string]map[string]int{
		"a": {"public": 200, "friends": 200, "private": 200, "custom": 200},
		"b": {"public": 200, "friends": 200, "private": 404, "custom": 200},
		"c": {"public": 200, "friends": 404, "private": 404, "custom": 404},
		"d": {"public": 200, "friends": 404, "private": 404, "custom": 404},
	}
	for who, rows := range view {
		for sec, want := range rows {
			path := "/~a/" + strconv.FormatInt(ids[sec], 10) + ".html"
			t.Run("view "+who+" "+sec, func(t *testing.T) {
				res := do(t, clients[who], http.MethodGet, ts.URL+path, nil)
				read(t, res)
				if res.StatusCode != want {
					t.Fatalf("%d want %d", res.StatusCode, want)
				}
			})
		}
	}

	adminWant := map[string]int{"d": 200, "a": 403, "b": 403, "c": 403}
	for who, want := range adminWant {
		t.Run("admin "+who, func(t *testing.T) {
			res := do(t, clients[who], http.MethodGet, ts.URL+"/admin", nil)
			read(t, res)
			if res.StatusCode != want {
				t.Fatalf("%d want %d", res.StatusCode, want)
			}
		})
	}

	commentWant := map[string]map[string]int{
		"b": {"public": 303, "friends": 303, "private": 404, "custom": 303},
		"c": {"public": 303, "friends": 404, "private": 404, "custom": 404},
	}
	for who, rows := range commentWant {
		for sec, want := range rows {
			t.Run("comment "+who+" "+sec, func(t *testing.T) {
				path := "/~a/" + strconv.FormatInt(ids[sec], 10) + ".html"
				token := ""
				if want == http.StatusSeeOther {
					page := read(t, do(t, clients[who], http.MethodGet, ts.URL+path, nil))
					token = csrfFrom(t, page)
				}
				res := do(t, clients[who], http.MethodPost, ts.URL+path, url.Values{"csrf": {token}, "body": {"<p>hi</p>"}})
				read(t, res)
				if res.StatusCode != want {
					t.Fatalf("%d want %d", res.StatusCode, want)
				}
			})
		}
	}

	t.Run("delete comment", func(t *testing.T) {
		path := "/~a/" + strconv.FormatInt(ids["public"], 10) + ".html"
		page := read(t, do(t, clients["c"], http.MethodGet, ts.URL+path, nil))
		res := do(t, clients["c"], http.MethodPost, ts.URL+path, url.Values{
			"csrf": {csrfFrom(t, page)}, "action": {"delete"}, "comment_id": {strconv.FormatInt(comments["public"], 10)},
		})
		read(t, res)
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("c delete %d", res.StatusCode)
		}
		page = read(t, do(t, clients["a"], http.MethodGet, ts.URL+path, nil))
		res = do(t, clients["a"], http.MethodPost, ts.URL+path, url.Values{
			"csrf": {csrfFrom(t, page)}, "action": {"delete"}, "comment_id": {strconv.FormatInt(comments["public"], 10)},
		})
		read(t, res)
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("a delete %d", res.StatusCode)
		}
	})
}
