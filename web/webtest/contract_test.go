package webtest

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"journal/lj"
	"journal/store"
	"journal/web"
)

// Case is one request in the shared front-end contract.
type Case struct {
	Name       string
	Method     string
	Path       string
	Form       url.Values
	WantStatus int
	Check      func(t *testing.T, st *store.Store)
}

// FrontFactory builds one front-end. The contract runs against every factory.
type FrontFactory func() (web.FrontEnd, error)

// Run checks CSRF, safe GETs, and security headers for one front-end.
func Run(t *testing.T, newFront FrontFactory) {
	t.Helper()
	st, ts, client := boot(t, newFront)
	defer ts.Close()

	headers := []string{"X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options", "Content-Security-Policy"}
	gets := []string{"/login", "/healthz", "/readyz", "/static/style.css"}
	for _, path := range gets {
		before, err := st.ContentHash(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		res := do(t, client, http.MethodGet, ts.URL+path, nil)
		body := read(t, res)
		if res.StatusCode != http.StatusOK && path != "/login" {
			t.Fatalf("%s %d %s", path, res.StatusCode, body)
		}
		for _, h := range headers {
			if res.Header.Get(h) == "" {
				t.Fatalf("%s missing %s", path, h)
			}
		}
		after, err := st.ContentHash(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatalf("GET %s wrote the database", path)
		}
	}

	rejectCSRF(t, client, ts.URL, "/login")
	login(t, client, ts.URL, "ada", "secret")
	for _, path := range []string{"/~ada/friends", "/update", "/manage/friends", "/admin", "/~ada/"} {
		before, err := st.ContentHash(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		res := do(t, client, http.MethodGet, ts.URL+path, nil)
		read(t, res)
		for _, h := range headers {
			if res.Header.Get(h) == "" {
				t.Fatalf("%s missing %s", path, h)
			}
		}
		after, err := st.ContentHash(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatalf("GET %s wrote the database", path)
		}
	}
	for _, path := range []string{"/logout", "/update", "/manage/friends"} {
		rejectCSRF(t, client, ts.URL, path)
	}
}

func rejectCSRF(t *testing.T, client *http.Client, base, path string) {
	t.Helper()
	res := do(t, client, http.MethodPost, base+path, url.Values{"username": {"ada"}})
	read(t, res)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing csrf %s -> %d", path, res.StatusCode)
	}
	res = do(t, client, http.MethodPost, base+path, url.Values{"csrf": {"not-the-token"}, "username": {"ada"}})
	read(t, res)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad csrf %s -> %d", path, res.StatusCode)
	}
}

func login(t *testing.T, c *http.Client, base, user, password string) {
	t.Helper()
	page := read(t, do(t, c, http.MethodGet, base+"/login", nil))
	token := csrfFrom(t, page)
	res := do(t, c, http.MethodPost, base+"/login", url.Values{"csrf": {token}, "username": {user}, "password": {password}})
	body := read(t, res)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login %d %s", res.StatusCode, body)
	}
}

func csrfFrom(t *testing.T, body string) string {
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

func boot(t *testing.T, newFront FrontFactory) (*store.Store, *httptest.Server, *http.Client) {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	st, err := store.Open(t.TempDir()+"/t.db", key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	front, err := newFront()
	if err != nil {
		t.Fatal(err)
	}
	fake := &lj.Fake{Password: map[string]string{"ada": lj.PasswordMD5("secret")}}
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
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return st, ts, client
}

func do(t *testing.T, c *http.Client, method, raw string, form url.Values) *http.Response {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, raw, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func read(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
