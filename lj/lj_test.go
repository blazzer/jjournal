package lj

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPasswordMD5AndAuthResponse(t *testing.T) {
	if got := PasswordMD5("hello"); got != "5d41402abc4b2a76b9719d911017c592" {
		t.Fatal(got)
	}
	pw := PasswordMD5("hello")
	got := AuthResponse("xyz", pw)
	sum := md5.Sum([]byte("xyz" + pw))
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if AuthResponse(pw, "xyz") == got {
		t.Fatal("challenge and hash were not ordered")
	}
}

func TestDigestResponseRFC2617(t *testing.T) {
	got := DigestResponse("Mufasa", "Circle Of Life", "testrealm@host.com", "GET", "/dir/index.html",
		"dcd98b7102dd2f0e8b11d0f600bfb0c093", "00000001", "0a4f113b", "auth")
	if got != "6629fae49393a05397450978507c4ef1" {
		t.Fatal(got)
	}
}

func TestSessionStringRedactsSecrets(t *testing.T) {
	s := NewSession("ada", "deadbeefdeadbeefdeadbeefdeadbeef", "super-secret-cookie", "Ada")
	out := s.String()
	if strings.Contains(out, "deadbeef") || strings.Contains(out, "super-secret-cookie") {
		t.Fatal(out)
	}
}

func TestNormalizeUsername(t *testing.T) {
	got, err := NormalizeUsername(" Ada ")
	if err != nil || got != "ada" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := NormalizeUsername("bad name"); err == nil {
		t.Fatal("accepted space")
	}
	if _, err := NormalizeUsername(""); err == nil {
		t.Fatal("accepted empty")
	}
}

func TestNormalizeSecurity(t *testing.T) {
	cases := []struct {
		in   string
		mask uint32
		sec  string
		out  uint32
	}{
		{"public", 0, "public", 0},
		{"", 4, "public", 0},
		{"private", 1, "private", 0},
		{"friends", 9, "friends", 1},
		{"usemask", 1, "friends", 1},
		{"usemask", 0, "private", 0},
		{"usemask", 5, "custom", 5},
		{"custom", 6, "custom", 6},
	}
	for _, tc := range cases {
		sec, mask := NormalizeSecurity(tc.in, tc.mask)
		if sec != tc.sec || mask != tc.out {
			t.Fatalf("%s/%d -> %s/%d", tc.in, tc.mask, sec, mask)
		}
	}
}

func TestFilterSince(t *testing.T) {
	base := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	entries := []LJEntry{
		{ItemID: 1, EventTime: base},
		{ItemID: 2, EventTime: base.Add(time.Hour)},
		{ItemID: 3, EventTime: base.Add(-time.Hour)},
	}
	got := FilterSince(entries, base)
	if len(got) != 2 || got[0].ItemID != 1 || got[1].ItemID != 2 {
		t.Fatalf("%+v", got)
	}
	if len(FilterSince(entries, time.Time{})) != 3 {
		t.Fatal("zero since should keep all")
	}
}

func TestSafeMessage(t *testing.T) {
	err := &AuthError{Reason: "secret-password-value leaked"}
	got := SafeMessage(err, "secret-password-value")
	if strings.Contains(got, "secret-password-value") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatal(got)
	}
}

func TestDefaultEndpoints(t *testing.T) {
	if XMLRPCEndpoint != "https://www.livejournal.com/interface/xmlrpc" {
		t.Fatal(XMLRPCEndpoint)
	}
	d, err := DefaultDigestURL("Bob")
	if err != nil || d != "https://bob.livejournal.com/data/rss?auth=digest" {
		t.Fatal(d, err)
	}
	s, err := DefaultScrapeURL("bob", 20)
	if err != nil || s != "https://bob.livejournal.com/friends?skip=20" {
		t.Fatal(s, err)
	}
	s, err = DefaultScrapeURL("bob", 0)
	if err != nil || s != "https://bob.livejournal.com/friends" {
		t.Fatal(s, err)
	}
	if _, err := DefaultDigestURL("not a user"); err == nil {
		t.Fatal("bad user")
	}
}

func TestFallbackOrder(t *testing.T) {
	if got := strings.Join(FallbackOrder(""), ","); got != "xmlrpc,digest,scrape" {
		t.Fatal(got)
	}
	if got := strings.Join(FallbackOrder("digest"), ","); got != "digest,xmlrpc,scrape" {
		t.Fatal(got)
	}
	if got := strings.Join(FallbackOrder("scrape"), ","); got != "scrape,xmlrpc,digest" {
		t.Fatal(got)
	}
	if _, err := NewSource("nope", nil); err == nil {
		t.Fatal("accepted unknown source")
	}
}

func TestParseFriendPageFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/friendspage.xml")
	if err != nil {
		t.Fatal(err)
	}
	val, err := decodeResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	entries := parseEntries(asSlice(asMap(val)["entries"]), "alice")
	if len(entries) != 2 {
		t.Fatalf("%d entries", len(entries))
	}
	if entries[0].Subject != "Hello" || entries[0].Mood != "cheerful" || entries[0].ItemID != 1001 || entries[0].Security != "public" {
		t.Fatalf("%+v", entries[0])
	}
	if entries[1].Security != "friends" || entries[1].JournalType != "C" || entries[1].Author != "bob" {
		t.Fatalf("%+v", entries[1])
	}
}

func TestParseFriendsHTMLFixture(t *testing.T) {
	f, err := os.Open("testdata/friends.html")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entries, err := ParseFriendsHTML(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("%d", len(entries))
	}
	if entries[0].Subject != "Hello" || entries[0].CommentCount != 3 || entries[0].UserpicURL == "" {
		t.Fatalf("%+v", entries[0])
	}
	if entries[1].Security != "friends" || entries[1].JournalType != "C" {
		t.Fatalf("%+v", entries[1])
	}
	dom := `<div class="entry"><a class="i-ljuser-username" href="https://carol.livejournal.com/">carol</a>
<h3 class="entry-title"><a href="https://carol.livejournal.com/55.html">Subj</a></h3>
<time datetime="2020-01-02T03:04:05Z"></time>
<div class="entry-text"><p>Body</p></div></div>`
	got, err := ParseFriendsHTML(strings.NewReader(dom))
	if err != nil || len(got) != 1 || got[0].ItemID != 55 || got[0].Author != "carol" || !strings.Contains(got[0].EventHTML, "Body") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestParseRSSFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/digest.xml")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := ParseRSS(data, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("%d", len(entries))
	}
	if entries[0].Subject != "Hello" || entries[0].Mood != "cheerful" || entries[0].Security != "public" {
		t.Fatalf("%+v", entries[0])
	}
	if entries[1].Security != "friends" || entries[1].ItemID != 1002 {
		t.Fatalf("%+v", entries[1])
	}
}

func TestCaptchaDetection(t *testing.T) {
	b, err := os.ReadFile("testdata/captcha.html")
	if err != nil {
		t.Fatal(err)
	}
	if !LooksLikeCaptcha(b) {
		t.Fatal("fixture should be captcha")
	}
	if LooksLikeCaptcha([]byte("<p>I wrote about a captcha in my journal</p>")) {
		t.Fatal("journal text is not a challenge page")
	}
}

func TestPagingLimit(t *testing.T) {
	ctx := context.Background()
	err := classifyFault(&FaultError{Code: 209, Message: "bad skip"})
	if !IsLimit(err) {
		t.Fatal(err)
	}
	x := NewXMLRPC(nil, "http://example.invalid")
	_, err = x.FriendsPageSkip(ctx, NewSession("ada", strings.Repeat("a", 32), "c", ""), MaxFriendsSkip+1)
	if !IsLimit(err) {
		t.Fatal(err)
	}
}

func TestXMLRPCLoginAndPage(t *testing.T) {
	pw := PasswordMD5("hello")
	var sawItemShow atomic.Bool
	var sawLastSync atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readAll(t, r)
		method := between(body, "<methodName>", "</methodName>")
		switch method {
		case "LJ.XMLRPC.getchallenge":
			writeXML(w, `<methodResponse><params><param><value><struct><member><name>challenge</name><value><string>abc</string></value></member></struct></value></param></params></methodResponse>`)
		case "LJ.XMLRPC.login":
			if !strings.Contains(body, "<name>ver</name>") || !strings.Contains(body, AuthResponse("abc", pw)) {
				writeFault(w, "Invalid password")
				return
			}
			writeXML(w, `<methodResponse><params><param><value><struct><member><name>fullname</name><value><string>Ada Lovelace</string></value></member></struct></value></param></params></methodResponse>`)
		case "LJ.XMLRPC.sessiongenerate":
			writeXML(w, `<methodResponse><params><param><value><struct><member><name>ljsession</name><value><string>super-secret-cookie</string></value></member></struct></value></param></params></methodResponse>`)
		case "LJ.XMLRPC.getfriends":
			writeXML(w, `<methodResponse><params><param><value><struct>
<member><name>friends</name><value><array><data><value><struct>
<member><name>username</name><value><string>bob</string></value></member>
<member><name>fullname</name><value><string>Bob</string></value></member>
<member><name>groupmask</name><value><int>3</int></value></member>
</struct></value></data></array></value></member>
<member><name>friendgroups</name><value><array><data><value><struct>
<member><name>id</name><value><int>1</int></value></member>
<member><name>name</name><value><string>Friends</string></value></member>
<member><name>public</name><value><int>1</int></value></member>
</struct></value></data></array></value></member>
</struct></value></param></params></methodResponse>`)
		case "LJ.XMLRPC.getfriendspage":
			if strings.Contains(body, "<int>50</int>") {
				sawItemShow.Store(true)
			}
			if strings.Contains(body, "2024-01-01 00:00:00") {
				sawLastSync.Store(true)
			}
			data, err := os.ReadFile("testdata/friendspage.xml")
			if err != nil {
				t.Error(err)
			}
			w.Write(data)
		default:
			http.Error(w, "nope", 500)
		}
	}))
	defer srv.Close()
	x := NewXMLRPC(srv.Client(), srv.URL)
	sess, err := x.Login(context.Background(), "Ada", pw)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Cookie != "super-secret-cookie" || sess.FullName != "Ada Lovelace" {
		t.Fatalf("%s", sess)
	}
	friends, err := x.FriendList(context.Background(), sess)
	if err != nil || len(friends) != 1 || friends[0].GroupMask != 3 {
		t.Fatalf("%+v %v", friends, err)
	}
	groups, ok := sess.Groups()
	if !ok || len(groups) != 1 || groups[0].Name != "Friends" {
		t.Fatalf("%+v %v", groups, ok)
	}
	since := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	entries, err := x.FriendsPage(context.Background(), sess, since)
	if err != nil || len(entries) != 2 {
		t.Fatalf("%v %d", err, len(entries))
	}
	if !sawItemShow.Load() || !sawLastSync.Load() {
		t.Fatalf("itemshow %v lastsync %v", sawItemShow.Load(), sawLastSync.Load())
	}
}

func TestParseCurrentFriendsPageFields(t *testing.T) {
	raw := `<?xml version="1.0"?><methodResponse><params><param><value><struct>
<member><name>entries</name><value><array><data><value><struct>
<member><name>ditemid</name><value><int>1001</int></value></member>
<member><name>journalname</name><value><string>alice</string></value></member>
<member><name>journalurl</name><value><string>https://alice.livejournal.com</string></value></member>
<member><name>postername</name><value><string>alice</string></value></member>
<member><name>subject_raw</name><value><string>A title</string></value></member>
<member><name>event_raw</name><value><string>&lt;p&gt;Body text&lt;/p&gt;</string></value></member>
<member><name>logtime</name><value><int>1710000000</int></value></member>
<member><name>security</name><value><string>public</string></value></member>
</struct></value></data></array></value></member>
</struct></value></param></params></methodResponse>`
	val, err := decodeResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	entries := parseEntries(asSlice(asMap(val)["entries"]), "ada")
	if len(entries) != 1 {
		t.Fatalf("%d", len(entries))
	}
	e := entries[0]
	if e.Subject != "A title" || e.EventHTML != "<p>Body text</p>" {
		t.Fatalf("%q %q", e.Subject, e.EventHTML)
	}
	if !e.EventTime.Equal(time.Unix(1710000000, 0).UTC()) {
		t.Fatal(e.EventTime)
	}
	if e.URL != "https://alice.livejournal.com/1001.html" {
		t.Fatal(e.URL)
	}
}

func TestXMLRPCBlockedAndAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	x := NewXMLRPC(srv.Client(), srv.URL)
	_, err := x.Login(context.Background(), "ada", PasswordMD5("hello"))
	if !IsBlocked(err) {
		t.Fatal(err)
	}
	capSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := os.ReadFile("testdata/captcha.html")
		w.Write(b)
	}))
	defer capSrv.Close()
	_, err = NewXMLRPC(capSrv.Client(), capSrv.URL).Login(context.Background(), "ada", PasswordMD5("hello"))
	var blocked *BlockedError
	if !IsBlocked(err) || !asBlocked(err, &blocked) || !blocked.Captcha {
		t.Fatal(err)
	}
	faultSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readAll(t, r)
		if strings.Contains(body, "getchallenge") {
			writeXML(w, `<methodResponse><params><param><value><struct><member><name>challenge</name><value><string>abc</string></value></member></struct></value></param></params></methodResponse>`)
			return
		}
		writeFault(w, "Invalid password")
	}))
	defer faultSrv.Close()
	_, err = NewXMLRPC(faultSrv.Client(), faultSrv.URL).Login(context.Background(), "ada", PasswordMD5("hello"))
	if !IsAuth(err) {
		t.Fatal(err)
	}
}

func TestDigestQOPAndUnsupportedFriends(t *testing.T) {
	full, _ := os.ReadFile("testdata/digest.xml")
	anon, _ := os.ReadFile("testdata/digest-anon.xml")
	pw := PasswordMD5("hello")
	var authed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="livejournal", nonce="abcnonce", qop="auth", opaque="xyz", algorithm=MD5`)
			w.WriteHeader(http.StatusOK)
			w.Write(anon)
			return
		}
		uri := r.URL.RequestURI()
		// cnonce is random; parse response and nc from the header by recomputing is hard.
		// Require qop=auth and that the response matches some cnonce we extract.
		h := r.Header.Get("Authorization")
		if !strings.Contains(h, "qop=auth") || strings.Contains(h, pw) {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		cnonce := headerParam(h, "cnonce")
		nc := headerParam(h, "nc")
		got := headerParam(h, "response")
		want := DigestResponse("alice", pw, "livejournal", "GET", uri, "abcnonce", nc, cnonce, "auth")
		if got != want {
			http.Error(w, "mismatch", http.StatusUnauthorized)
			return
		}
		authed.Add(1)
		w.Write(full)
	}))
	defer srv.Close()
	d := NewDigest(srv.Client())
	d.URLFor = func(user string) (string, error) {
		return srv.URL + "/data/rss?auth=digest", nil
	}
	if _, err := d.FriendList(context.Background(), NewSession("alice", pw, "", "")); !IsUnsupported(err) {
		t.Fatal(err)
	}
	sess, err := d.Login(context.Background(), "alice", pw)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := d.FriendsPage(context.Background(), sess, time.Time{})
	if err != nil || len(entries) != 2 {
		t.Fatalf("%v %+v", err, entries)
	}
	if authed.Load() < 1 {
		t.Fatal("did not authenticate")
	}
	body, _, err := d.Fetch(context.Background(), "alice", "", false)
	if err != nil {
		t.Fatal(err)
	}
	anonEntries, err := ParseRSS(body, "alice")
	if err != nil || len(anonEntries) != 1 {
		t.Fatalf("anon %d %v", len(anonEntries), err)
	}
}

func TestDigestDisabledMessageVerbatim(t *testing.T) {
	const msg = "Digest authentication <b>FAILED</b>!"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Digest realm="lj", nonce="abcnonce", qop="auth", algorithm=MD5`)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(msg))
	}))
	defer srv.Close()
	d := NewDigest(srv.Client())
	d.URLFor = func(string) (string, error) { return srv.URL + "/data/rss?auth=digest", nil }
	_, _, err := d.Fetch(context.Background(), "alice", "secret", true)
	if err == nil || !strings.Contains(err.Error(), msg) {
		t.Fatalf("error %v", err)
	}
	out := t.TempDir()
	code, err := RunProbe(context.Background(), ProbeConfig{
		User:           "alice",
		Password:       "s3cret-password",
		OutDir:         out,
		Client:         srv.Client(),
		DigestURL:      d.URLFor,
		XMLRPCEndpoint: srv.URL,
		ScrapeURL:      func(string, int) (string, error) { return srv.URL + "/friends", nil },
	})
	if err != nil || code != 2 {
		t.Fatal(code, err)
	}
	b, _ := os.ReadFile(out + "/report.txt")
	if !strings.Contains(string(b), msg) {
		t.Fatalf("report hid the server body:\n%s", b)
	}
}

func TestScrapeSkipAndBlocked(t *testing.T) {
	page, _ := os.ReadFile("testdata/friends.html")
	var skips []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		skips = append(skips, r.URL.RawQuery)
		if r.Header.Get("Cookie") == "" {
			http.Error(w, "no cookie", http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.RawQuery, "skip=20") {
			w.Write([]byte(`<html><body></body></html>`))
			return
		}
		var b strings.Builder
		b.WriteString("<html><body>")
		for i := 1; i <= 20; i++ {
			fmt.Fprintf(&b, `<div class="entry" data-itemid="%d" data-journal="alice" data-poster="alice"><h3 class="entry-title"><a href="https://alice.livejournal.com/%d.html">T</a></h3><time datetime="2024-05-01T12:00:00Z"></time><div class="entry-text"><p>x</p></div></div>`, i, i)
		}
		b.WriteString("</body></html>")
		w.Write([]byte(b.String()))
		_ = page
	}))
	defer srv.Close()
	sc := NewScrape(srv.Client(), srv.URL)
	sc.URLFor = func(user string, skip int) (string, error) {
		u := srv.URL + "/friends"
		if skip > 0 {
			u += "?skip=" + itoa(skip)
		}
		return u, nil
	}
	_, err := sc.FriendList(context.Background(), Session{})
	if !IsUnsupported(err) {
		t.Fatal(err)
	}
	sess := NewSession("alice", PasswordMD5("hello"), "cookie-value", "")
	entries, err := sc.FriendsPage(context.Background(), sess, time.Time{})
	if err != nil || len(entries) != 20 {
		t.Fatalf("%v %d", err, len(entries))
	}
	if len(skips) < 2 || skips[1] != "skip=20" {
		t.Fatalf("skips %#v", skips)
	}
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer blocked.Close()
	sc.URLFor = func(string, int) (string, error) { return blocked.URL, nil }
	_, err = sc.FriendsPage(context.Background(), sess, time.Time{})
	if !IsBlocked(err) {
		t.Fatal(err)
	}
}

func TestFallbackAuthDoesNotFallThrough(t *testing.T) {
	primary := &Fake{LoginErr: &AuthError{Reason: "rejected"}}
	second := &Fake{}
	f := NewFallback([]string{"xmlrpc", "digest"}, []LJSource{primary, second})
	_, err := f.Login(context.Background(), "ada", PasswordMD5("hello"))
	if !IsAuth(err) || second.LoginCalls != 0 {
		t.Fatalf("%v calls %d", err, second.LoginCalls)
	}
}

func TestFallbackUsesNextSource(t *testing.T) {
	primary := &Fake{PageErr: &BlockedError{StatusCode: 429, Reason: "http 429"}, UnsupportedFriends: true}
	second := &Fake{Entries: map[string][]LJEntry{"ada": {{ItemID: 7, Author: "bob", Journal: "bob", Subject: "Hi", EventTime: time.Now()}}}}
	f := NewFallback([]string{"xmlrpc", "digest"}, []LJSource{primary, second})
	sess := NewSession("ada", PasswordMD5("hello"), "", "")
	entries, err := f.FriendsPage(context.Background(), sess, time.Time{})
	if err != nil || len(entries) != 1 || entries[0].ItemID != 7 {
		t.Fatalf("%v %+v", err, entries)
	}
	friends, err := f.FriendList(context.Background(), sess)
	if err != nil || len(friends) != 0 {
		t.Fatalf("%v %+v", err, friends)
	}
}

func TestProbeSkipAndSuccess(t *testing.T) {
	dir := t.TempDir()
	code, err := RunProbe(context.Background(), ProbeConfig{OutDir: dir})
	if err != nil || code != 0 {
		t.Fatal(code, err)
	}
	b, err := os.ReadFile(dir + "/report.txt")
	if err != nil || !strings.Contains(string(b), "live probe was skipped") {
		t.Fatalf("%s %v", b, err)
	}

	pw := "s3cret-password"
	pwMD5 := PasswordMD5(pw)
	var inflight atomic.Int32
	var maxInflight atomic.Int32
	full, _ := os.ReadFile("testdata/digest.xml")
	anon, _ := os.ReadFile("testdata/digest-anon.xml")
	page, _ := os.ReadFile("testdata/friends.html")
	fp, _ := os.ReadFile("testdata/friendspage.xml")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inflight.Add(1)
		for {
			old := maxInflight.Load()
			if n <= old || maxInflight.CompareAndSwap(old, n) {
				break
			}
		}
		defer inflight.Add(-1)
		if r.Method == http.MethodPost {
			body := readAll(t, r)
			method := between(body, "<methodName>", "</methodName>")
			switch method {
			case "LJ.XMLRPC.getchallenge":
				writeXML(w, `<methodResponse><params><param><value><struct><member><name>challenge</name><value><string>abc</string></value></member></struct></value></param></params></methodResponse>`)
			case "LJ.XMLRPC.login":
				if !strings.Contains(body, AuthResponse("abc", pwMD5)) {
					writeFault(w, "Invalid password")
					return
				}
				writeXML(w, `<methodResponse><params><param><value><struct><member><name>fullname</name><value><string>Ada</string></value></member></struct></value></param></params></methodResponse>`)
			case "LJ.XMLRPC.sessiongenerate":
				writeXML(w, `<methodResponse><params><param><value><struct><member><name>ljsession</name><value><string>super-secret-cookie</string></value></member></struct></value></param></params></methodResponse>`)
			case "LJ.XMLRPC.getfriends":
				writeXML(w, `<methodResponse><params><param><value><struct><member><name>friends</name><value><array><data></data></array></value></member></struct></value></param></params></methodResponse>`)
			case "LJ.XMLRPC.getfriendspage":
				w.Write(fp)
			default:
				http.Error(w, "no", 500)
			}
			return
		}
		if strings.Contains(r.URL.Path, "rss") {
			if r.Header.Get("Authorization") == "" {
				w.Header().Set("WWW-Authenticate", `Digest realm="livejournal", nonce="abcnonce", qop="auth"`)
				w.Write(anon)
				return
			}
			h := r.Header.Get("Authorization")
			cnonce := headerParam(h, "cnonce")
			nc := headerParam(h, "nc")
			got := headerParam(h, "response")
			want := DigestResponse("alice", pw, "livejournal", "GET", r.URL.RequestURI(), "abcnonce", nc, cnonce, "auth")
			if got != want {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			w.Write(full)
			return
		}
		if strings.Contains(r.Header.Get("Cookie"), "super-secret-cookie") {
			w.Write(page)
			return
		}
		http.Error(w, "no cookie", http.StatusUnauthorized)
	}))
	defer srv.Close()
	out := t.TempDir()
	code, err = RunProbe(context.Background(), ProbeConfig{
		User:           "alice",
		Password:       pw,
		OutDir:         out,
		Client:         srv.Client(),
		XMLRPCEndpoint: srv.URL,
		DigestURL: func(string) (string, error) {
			return srv.URL + "/data/rss?auth=digest", nil
		},
		ScrapeURL: func(_ string, skip int) (string, error) {
			if skip > 0 {
				return srv.URL + "/friends?skip=" + itoa(skip), nil
			}
			return srv.URL + "/friends", nil
		},
	})
	if err != nil || code != 0 {
		t.Fatal(code, err)
	}
	report, err := os.ReadFile(out + "/report.txt")
	if err != nil {
		t.Fatal(err)
	}
	text := string(report)
	if strings.Contains(text, pw) || strings.Contains(text, "super-secret-cookie") || strings.Contains(text, pwMD5) {
		t.Fatalf("report leaked a secret: %s", text)
	}
	if !strings.Contains(text, "friends_locked=yes") || !strings.Contains(text, "itemshow=50") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "anonymous_entries=1") || !strings.Contains(text, "authenticated_entries=2") {
		t.Fatal(text)
	}
	cookie, err := os.ReadFile(out + "/ljsession.txt")
	if err != nil || string(cookie) != "super-secret-cookie" {
		t.Fatalf("cookie file %q %v", cookie, err)
	}
	if maxInflight.Load() != 1 {
		t.Fatalf("max inflight %d", maxInflight.Load())
	}
}

func TestProbeAllBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer srv.Close()
	out := t.TempDir()
	code, err := RunProbe(context.Background(), ProbeConfig{
		User:           "alice",
		Password:       "s3cret-password",
		OutDir:         out,
		Client:         srv.Client(),
		XMLRPCEndpoint: srv.URL,
		DigestURL:      func(string) (string, error) { return srv.URL + "/rss", nil },
		ScrapeURL:      func(string, int) (string, error) { return srv.URL + "/friends", nil },
	})
	if err != nil || code != 2 {
		t.Fatal(code, err)
	}
	b, _ := os.ReadFile(out + "/report.txt")
	if !strings.Contains(string(b), "blocked=true") {
		t.Fatal(string(b))
	}
}

func TestTypedErrors(t *testing.T) {
	if !IsUnsupported(&UnsupportedError{Source: "digest", Method: "FriendList"}) {
		t.Fatal()
	}
	if !IsAuth(&AuthError{Reason: "rejected"}) {
		t.Fatal()
	}
	if !IsBlocked(&BlockedError{StatusCode: 403}) {
		t.Fatal()
	}
}

func asBlocked(err error, target **BlockedError) bool {
	b, ok := err.(*BlockedError)
	if ok {
		*target = b
	}
	return ok
}

func readAll(t *testing.T, r *http.Request) string {
	t.Helper()
	buf := make([]byte, 1<<20)
	n, _ := r.Body.Read(buf)
	return string(buf[:n])
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	j := strings.Index(s, b)
	if j < 0 {
		return ""
	}
	return s[:j]
}

func writeXML(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/xml")
	w.Write([]byte(s))
}

func writeFault(w http.ResponseWriter, msg string) {
	writeXML(w, `<methodResponse><fault><value><struct><member><name>faultCode</name><value><int>100</int></value></member><member><name>faultString</name><value><string>`+msg+`</string></value></member></struct></value></fault></methodResponse>`)
}

func headerParam(h, name string) string {
	h = strings.TrimPrefix(h, "Digest ")
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		key, val, ok := strings.Cut(part, "=")
		if !ok || key != name {
			continue
		}
		return strings.Trim(val, `"`)
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
