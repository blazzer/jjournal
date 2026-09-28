package sync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"journal/lj"
	"journal/render"
	"journal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 3)
	}
	s, err := store.Open(t.TempDir()+"/t.db", key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSchedule(t *testing.T) {
	if JitteredInterval(20*time.Minute, 5*time.Minute, 0) != 15*time.Minute {
		t.Fatal("low")
	}
	if JitteredInterval(20*time.Minute, 5*time.Minute, 1) != 25*time.Minute {
		t.Fatal("high")
	}
	for i := 0; i < 200; i++ {
		d := JitteredInterval(20*time.Minute, 5*time.Minute, Unit())
		if d < 15*time.Minute || d > 25*time.Minute {
			t.Fatal(d)
		}
	}
	interval := 20 * time.Minute
	var prev time.Duration
	for n := 1; n <= 12; n++ {
		base := BackoffBase(interval, n)
		if base < interval {
			t.Fatalf("n=%d base %s", n, base)
		}
		if n > 1 && base < prev {
			t.Fatalf("n=%d decreased", n)
		}
		prev = base
		low := Backoff(interval, n, 0)
		if low < interval-interval/5 {
			t.Fatalf("n=%d jitter %s", n, low)
		}
	}
	if BackoffBase(interval, 1) != interval {
		t.Fatal(BackoffBase(interval, 1))
	}
	if BackoffBase(interval, 20) != 6*time.Hour {
		t.Fatal(BackoffBase(interval, 20))
	}
}

func TestSyncLimits(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	pw := strings.Repeat("ab", 16)
	ada, err := st.UpsertLogin(ctx, "ada", "Ada", pw, "cookie")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	fake := &lj.Fake{
		Friends: map[string][]lj.LJFriend{"ada": {{Username: "bob", GroupMask: 1}}},
		Groups:  map[string][]lj.LJGroup{"ada": {{ID: 1, Name: "Friends"}}},
		Entries: map[string][]lj.LJEntry{"ada": {{
			ItemID: 5, Journal: "bob", Author: "bob", Subject: "Hi",
			EventHTML: "<p>ok</p><script>alert(1)</script>", EventTime: when,
			Security: "public", URL: "https://bob.livejournal.com/5.html",
		}}},
	}
	w := New(st, fake, nil, 2)
	w.SetClock(func() time.Time { return when }, func() float64 { return 0 })
	if err := w.SyncUser(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
	if fake.FriendCalls != 1 || fake.PageCalls != 1 {
		t.Fatalf("friends %d pages %d", fake.FriendCalls, fake.PageCalls)
	}
	if err := w.SyncUser(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
	if fake.FriendCalls != 1 {
		t.Fatal("friend list refreshed too often")
	}
	if err := st.TouchFriends(ctx, ada.ID, when.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := w.SyncUser(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
	if fake.FriendCalls != 2 {
		t.Fatal(fake.FriendCalls)
	}
	page, err := st.FriendsPage(ctx, ada.ID, 0, 0, 20)
	if err != nil || len(page.Entries) == 0 {
		t.Fatal(err, len(page.Entries))
	}
	body := page.Entries[0].BodyHTML
	if strings.Contains(body, "<script") || !strings.Contains(body, "ok") {
		t.Fatal(body)
	}
	u, _ := st.UserByID(ctx, ada.ID)
	if u.SyncStatus != store.StatusOK {
		t.Fatal(u.SyncStatus, u.SyncError)
	}

	fake.PageErr = &lj.BlockedError{StatusCode: 429, Reason: "http 429"}
	calls := fake.PageCalls
	if err := w.SyncUser(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
	u, _ = st.UserByID(ctx, ada.ID)
	if u.SyncStatus != store.StatusBlocked || !u.BlockedUntil.Equal(when.Add(6*time.Hour)) {
		t.Fatalf("%+v", u)
	}
	if err := w.SyncUser(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
	if fake.PageCalls != calls+1 {
		t.Fatalf("polled while blocked: %d", fake.PageCalls)
	}
}

func TestAuthFailedStops(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	pw := strings.Repeat("cd", 16)
	ada, _ := st.UpsertLogin(ctx, "ada", "Ada", pw, "")
	fake := &lj.Fake{PageErrs: []error{&lj.AuthError{Reason: "rejected"}, nil}, LoginErr: &lj.AuthError{Reason: "rejected"}}
	w := New(st, fake, nil, 2)
	if err := w.SyncUser(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByID(ctx, ada.ID)
	if u.SyncStatus != store.StatusAuthFailed {
		t.Fatal(u.SyncStatus, u.SyncError)
	}
	pages := fake.PageCalls
	if err := w.SyncUser(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
	if fake.PageCalls != pages {
		t.Fatal("retried after auth failure")
	}
}

func TestConcurrency(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	pw := strings.Repeat("ef", 16)
	var ids []int64
	for _, name := range []string{"a1", "a2", "a3", "a4"} {
		u, err := st.UpsertLogin(ctx, name, name, pw, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	fake := &lj.Fake{Delay: 80 * time.Millisecond, UnsupportedFriends: true}
	w := New(st, fake, nil, 2)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			_ = w.SyncUser(ctx, id)
		}(id)
	}
	wg.Wait()
	if fake.MaxActive > 2 {
		t.Fatal(fake.MaxActive)
	}
}

func TestUserpicCache(t *testing.T) {
	gif := []byte{
		0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00,
		0xff, 0xff, 0xff, 0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00,
		0x01, 0x00, 0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3b,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(gif)
	}))
	defer srv.Close()
	st := testStore(t)
	ctx := context.Background()
	pw := strings.Repeat("11", 16)
	ada, _ := st.UpsertLogin(ctx, "ada", "Ada", pw, "")
	fake := &lj.Fake{UnsupportedFriends: true, Entries: map[string][]lj.LJEntry{"ada": {{
		ItemID: 1, Journal: "bob", Author: "bob", EventHTML: "hi", Security: "public",
		EventTime: time.Now(), UserpicURL: srv.URL + "/p.gif",
	}}}}
	key := make([]byte, 32)
	proxy := &render.Proxy{Key: key, Dir: t.TempDir(), AllowPrivate: true, Client: srv.Client()}
	w := New(st, fake, proxy, 2)
	if err := w.SyncUser(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
}

type skipSrc struct {
	pages   map[int][]lj.LJEntry
	limitAt int
	calls   []int
}

func (s *skipSrc) Login(context.Context, string, string) (lj.Session, error) {
	return lj.Session{}, nil
}
func (s *skipSrc) FriendList(context.Context, lj.Session) ([]lj.LJFriend, error) {
	return nil, &lj.UnsupportedError{Source: "skip", Method: "FriendList"}
}
func (s *skipSrc) FriendsPage(context.Context, lj.Session, time.Time) ([]lj.LJEntry, error) {
	return nil, nil
}
func (s *skipSrc) FriendsPageSkip(_ context.Context, _ lj.Session, skip int) ([]lj.LJEntry, error) {
	s.calls = append(s.calls, skip)
	if s.limitAt > 0 && skip >= s.limitAt {
		return nil, &lj.LimitError{Param: "skip"}
	}
	return append([]lj.LJEntry(nil), s.pages[skip]...), nil
}

func fullPage(start int, when time.Time, comments int) []lj.LJEntry {
	out := make([]lj.LJEntry, lj.ItemShow)
	for i := range out {
		out[i] = lj.LJEntry{
			ItemID: int64(start + i), Journal: "bob", Author: "bob",
			EventHTML: "<p>body</p>", Security: "public",
			EventTime:    when.Add(-time.Duration(i) * time.Minute),
			CommentCount: comments,
			URL:          "https://bob.livejournal.com/1.html",
		}
	}
	return out
}

func TestPagesAreStoredAndRefreshed(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	pw := strings.Repeat("ab", 16)
	ada, err := st.UpsertLogin(ctx, "ada", "Ada", pw, "cookie")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	src := &skipSrc{pages: map[int][]lj.LJEntry{
		0: fullPage(1, when, 1),
	}}
	w := New(st, src, nil, 2)
	w.MaxPages = 1
	w.SetClock(func() time.Time { return when }, func() float64 { return 0 })
	if err := w.SyncUser(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByID(ctx, ada.ID)
	if u.BackfillSkip != lj.ItemShow {
		t.Fatalf("skip %d", u.BackfillSkip)
	}
	page, err := st.FriendsPage(ctx, ada.ID, 0, 0, 20)
	if err != nil || len(page.Entries) == 0 || page.Entries[0].CommentCount != 1 {
		t.Fatalf("%v %+v", err, page.Entries)
	}
	src.pages[0] = fullPage(1, when, 7)
	src.pages[lj.ItemShow] = fullPage(1000, when.Add(-2*time.Hour), 2)
	if err := w.SyncUser(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
	page, err = st.FriendsPage(ctx, ada.ID, 0, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	var refreshed bool
	for _, e := range page.Entries {
		if e.ItemID == 1 && e.CommentCount == 7 {
			refreshed = true
		}
	}
	if !refreshed {
		t.Fatal("stored entry was not refreshed")
	}
	if src.calls[0] != 0 || src.calls[len(src.calls)-1] != lj.ItemShow {
		t.Fatalf("calls %v", src.calls)
	}
}

func TestSkipLimitStopsCleanly(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	pw := strings.Repeat("ab", 16)
	ada, _ := st.UpsertLogin(ctx, "ada", "Ada", pw, "cookie")
	when := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	src := &skipSrc{
		pages:   map[int][]lj.LJEntry{0: fullPage(1, when, 1)},
		limitAt: lj.ItemShow,
	}
	w := New(st, src, nil, 2)
	w.MaxPages = 2
	w.SetClock(func() time.Time { return when }, func() float64 { return 0 })
	if err := w.SyncUser(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByID(ctx, ada.ID)
	if u.SyncStatus != store.StatusOK || u.BackfillSkip != 0 {
		t.Fatalf("%s %d %s", u.SyncStatus, u.BackfillSkip, u.SyncError)
	}
}

func TestRunCancels(t *testing.T) {
	st := testStore(t)
	w := New(st, &lj.Fake{}, nil, 2)
	w.Poll = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not stop")
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type stubPause struct{ until time.Time }

func (s stubPause) PausedUntil(string) (time.Time, bool)    { return s.until, true }
func (s stubPause) PauseHost(string, time.Duration, string) {}

func TestPausedHostSkipsRequest(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	pw := strings.Repeat("ab", 16)
	ada, err := st.UpsertLogin(ctx, "ada", "Ada", pw, "cookie")
	if err != nil {
		t.Fatal(err)
	}
	fake := &lj.Fake{Entries: map[string][]lj.LJEntry{"ada": {{
		ItemID: 1, Journal: "bob", Author: "bob", EventTime: time.Date(2024, 6, 1, 11, 0, 0, 0, time.UTC),
	}}}}
	when := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	w := New(st, fake, nil, 1)
	w.SetClock(func() time.Time { return when }, func() float64 { return 0 })
	w.Pauses = stubPause{until: when.Add(2 * time.Hour)}
	if err := w.SyncUser(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
	if fake.PageCalls != 0 || fake.FriendCalls != 0 {
		t.Fatalf("pages %d friends %d", fake.PageCalls, fake.FriendCalls)
	}
	u, err := st.UserByID(ctx, ada.ID)
	if err != nil || !u.NextSyncAt.Equal(when.Add(2*time.Hour)) {
		t.Fatal(u.NextSyncAt, err)
	}
}
