package classic_test

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"journal/lj"
)

func TestSignupAndPassphraseLogin(t *testing.T) {
	st, srv, ts, client := testApp(t)
	defer ts.Close()
	srv.Config.MaxUsers = 30
	token, err := st.CreateInvite(t.Context(), 0, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	res := get(t, client, ts.URL+"/signup?invite="+token)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/signup" {
		t.Fatal(res.StatusCode, res.Header.Get("Location"))
	}
	res.Body.Close()
	if strings.Contains(res.Header.Get("Location"), token) {
		t.Fatal("token stayed in the redirect")
	}
	page := readBody(t, get(t, client, ts.URL+"/signup"))
	if !strings.Contains(page, "friends-locked") {
		t.Fatal("missing operator notice")
	}
	if !strings.Contains(page, "connect-src") && !strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors") {
		// header is on the response, not the body
	}
	res = get(t, client, ts.URL+"/signup")
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "script-src 'self'") ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "connect-src 'self'") {
		t.Fatal(res.Header.Get("Content-Security-Policy"))
	}
	page = readBody(t, res)
	res = post(t, client, ts.URL+"/signup", url.Values{
		"csrf": {csrfToken(t, page)}, "handle": {"ada"}, "display": {"Ada"},
		"passphrase": {"hello-password"}, "confirm": {"hello-password"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatal(res.StatusCode, readBody(t, res))
	}
	res.Body.Close()
	u, err := st.UserByUsername(t.Context(), "ada")
	if err != nil || !u.HasVault || !u.IsAdmin {
		t.Fatalf("%+v %v", u, err)
	}
	jar, _ := cookiejar.New(nil)
	other := &http.Client{Jar: jar, CheckRedirect: stopRedirect}
	calls := srv.Source.(*lj.Fake).LoginCalls
	loginAs(t, other, ts.URL, "ada", "hello-password")
	if srv.Source.(*lj.Fake).LoginCalls != calls {
		t.Fatal("service password was used for a profile")
	}
	jar2, _ := cookiejar.New(nil)
	bad := &http.Client{Jar: jar2, CheckRedirect: stopRedirect}
	page = readBody(t, get(t, bad, ts.URL+"/login"))
	res = post(t, bad, ts.URL+"/login", url.Values{
		"csrf": {csrfToken(t, page)}, "username": {"ada"}, "password": {"not-the-passphrase"},
	})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatal(res.StatusCode, readBody(t, res))
	}
	res.Body.Close()
}
