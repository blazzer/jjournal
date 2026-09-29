package modern

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"journal/store"
	"journal/web"
	"journal/web/classic"
)

func TestModernFront(t *testing.T) {
	f, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if f.UI() != "modern" {
		t.Fatal(f.UI())
	}
}

func TestLoggedOutPageUsesModern(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 3
	}
	st, err := store.Open(t.TempDir()+"/t.db", key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	classicFront, err := classic.New()
	if err != nil {
		t.Fatal(err)
	}
	mod, err := New()
	if err != nil {
		t.Fatal(err)
	}
	h, err := web.New(web.Config{Secret: key, BaseURL: "http://127.0.0.1:8080", SiteName: "Journal"}, st, nil, nil, nil, nil, classicFront)
	if err != nil {
		t.Fatal(err)
	}
	h.Modern = mod
	ts := httptest.NewServer(h)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !bytes.Contains(body, []byte(`class="modern"`)) {
		t.Fatal(res.StatusCode, string(body))
	}
}

