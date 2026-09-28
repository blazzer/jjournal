// Package sync polls LiveJournal in the background. It never writes to LiveJournal.
package sync

import (
	"context"
	"log/slog"
	"regexp"
	gosync "sync"
	"time"

	"journal/lj"
	"journal/render"
	"journal/store"
)

// Worker syncs each user on a jittered schedule.
type Worker struct {
	Store       *store.Store
	Source      lj.LJSource
	Proxy       *render.Proxy
	Interval    time.Duration
	Jitter      time.Duration
	BlockFor    time.Duration
	FriendEvery time.Duration
	Poll        time.Duration
	MaxPages    int
	APIHost     string
	Pauses      Pauser
	Images      chan<- string
	sem         chan struct{}
	kick        chan int64
	mu          gosync.Mutex
	running     map[int64]bool
	stopped     bool
	wg          gosync.WaitGroup
	now         func() time.Time
	randFloat   func() float64
	log         *slog.Logger
}

// Pauser is the outbound host-pause view.
type Pauser interface {
	PausedUntil(host string) (time.Time, bool)
	PauseHost(host string, d time.Duration, reason string)
}

// New builds a worker. maxConcurrent defaults to 2.
func New(st *store.Store, src lj.LJSource, proxy *render.Proxy, maxConcurrent int) *Worker {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultConcurrent
	}
	return &Worker{
		Store:       st,
		Source:      src,
		Proxy:       proxy,
		Interval:    DefaultInterval,
		Jitter:      DefaultJitter,
		BlockFor:    DefaultBlockFor,
		FriendEvery: DefaultFriendEvery,
		Poll:        30 * time.Second,
		MaxPages:    DefaultMaxPages,
		sem:         make(chan struct{}, maxConcurrent),
		kick:        make(chan int64, 32),
		running:     map[int64]bool{},
		APIHost:     "www.livejournal.com",
		now:         time.Now,
		randFloat:   Unit,
		log:         slog.Default(),
	}
}

// Stop waits for syncs spawned by Run.
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetClock overrides time and jitter for tests.
func (w *Worker) SetClock(now func() time.Time, randFloat func() float64) {
	if now != nil {
		w.now = now
	}
	if randFloat != nil {
		w.randFloat = randFloat
	}
}

// Kick asks the loop to sync a user soon.
func (w *Worker) Kick(id int64) {
	if w == nil {
		return
	}
	select {
	case w.kick <- id:
	default:
	}
}

// Run polls until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	w.enqueue(ctx)
	t := time.NewTicker(w.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-w.kick:
			w.spawn(ctx, id)
		case <-t.C:
			w.enqueue(ctx)
		}
	}
}

func (w *Worker) enqueue(ctx context.Context) {
	ids, err := w.Store.UsersDue(ctx, w.now())
	if err != nil {
		w.log.Error("sync list", "err", lj.SafeMessage(err))
		return
	}
	for _, id := range ids {
		w.spawn(ctx, id)
	}
}

func (w *Worker) spawn(ctx context.Context, id int64) {
	w.mu.Lock()
	if w.stopped || w.running[id] {
		w.mu.Unlock()
		return
	}
	w.running[id] = true
	w.wg.Add(1)
	w.mu.Unlock()
	go func() {
		defer w.wg.Done()
		defer func() {
			w.mu.Lock()
			delete(w.running, id)
			w.mu.Unlock()
		}()
		if err := w.SyncUser(ctx, id); err != nil {
			w.log.Error("sync user", "user", id, "err", lj.SafeMessage(err))
		}
	}()
}

func (w *Worker) acquire(ctx context.Context) (func(), error) {
	select {
	case w.sem <- struct{}{}:
		return func() { <-w.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SyncUser refreshes one account. Auth failures and blocks are recorded and not returned.
func (w *Worker) SyncUser(ctx context.Context, userID int64) error {
	u, err := w.Store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	now := w.now()
	if u.SyncStatus == store.StatusAuthFailed {
		return nil
	}
	if u.BlockedUntil.After(now) {
		return nil
	}
	if w.APIHost != "" && w.Pauses != nil {
		if until, ok := w.Pauses.PausedUntil(w.APIHost); ok && until.After(now) {
			extra := time.Duration(float64(w.Jitter) * w.randFloat())
			return w.Store.SetNextSync(ctx, userID, until.Add(extra))
		}
	}
	pw, cookie, err := w.Store.Secrets(ctx, userID)
	if err != nil {
		return err
	}
	if pw == "" {
		return w.Store.MarkAuthFailed(ctx, userID, "missing credentials")
	}
	sess := lj.NewSession(u.Username, pw, cookie, u.DisplayName)
	release, err := w.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	if w.needsFriends(u, now) {
		if err := w.syncFriends(ctx, &u, &sess, pw, now); err != nil {
			if lj.IsAuth(err) || lj.IsBlocked(err) {
				return w.finish(ctx, u, pw, cookie, err, now)
			}
			w.log.Error("sync friends", "user", u.ID, "err", lj.SafeMessage(err))
		}
	}
	nextSkip, err := w.syncPages(ctx, &sess, u, pw, now)
	if saveErr := w.Store.SetBackfillSkip(ctx, u.ID, nextSkip); saveErr != nil && err == nil {
		err = saveErr
	}
	if err != nil {
		return w.finish(ctx, u, pw, cookie, err, now)
	}
	next := now.Add(JitteredInterval(w.Interval, w.Jitter, w.randFloat()))
	return w.Store.MarkSyncOK(ctx, u.ID, now, next)
}

// syncPages stores one slice of the friends page and remembers where to resume.
// The newest page is always read, so edits and new posts refresh every sync.
// Older pages advance a few at a time. Entries already stored are updated in place.
func (w *Worker) syncPages(ctx context.Context, sess *lj.Session, u store.User, pw string, now time.Time) (int, error) {
	budget := w.MaxPages
	if budget <= 0 {
		budget = DefaultMaxPages
	}
	skip := u.BackfillSkip
	if skip < 0 || skip > lj.MaxFriendsSkip {
		skip = 0
	}
	if skip != 0 {
		entries, err := w.fetchPage(ctx, sess, pw, u.ID, 0)
		if lj.IsLimit(err) {
			return 0, nil
		}
		if err != nil {
			return skip, err
		}
		for _, e := range entries {
			if err := w.save(ctx, u.ID, e); err != nil {
				return skip, err
			}
		}
	}
	for budget > 0 {
		entries, err := w.fetchPage(ctx, sess, pw, u.ID, skip)
		if lj.IsLimit(err) {
			return 0, nil
		}
		if err != nil {
			return skip, err
		}
		for _, e := range entries {
			if err := w.save(ctx, u.ID, e); err != nil {
				return skip, err
			}
		}
		budget--
		if len(entries) < lj.ItemShow {
			return 0, nil
		}
		oldest := oldestEntry(entries)
		if !oldest.IsZero() && now.Sub(oldest) > lj.FriendsWindow {
			return 0, nil
		}
		if skip+lj.ItemShow > lj.MaxFriendsSkip {
			return 0, nil
		}
		skip += lj.ItemShow
	}
	return skip, nil
}

func oldestEntry(entries []lj.LJEntry) time.Time {
	var oldest time.Time
	for _, e := range entries {
		if e.EventTime.IsZero() {
			continue
		}
		if oldest.IsZero() || e.EventTime.Before(oldest) {
			oldest = e.EventTime
		}
	}
	return oldest
}

func (w *Worker) fetchPage(ctx context.Context, sess *lj.Session, pw string, userID int64, skip int) ([]lj.LJEntry, error) {
	entries, err := w.pageAt(ctx, *sess, skip)
	if lj.IsAuth(err) {
		ns, err2 := w.relogin(ctx, userID, sess.Username, pw)
		if err2 != nil {
			return nil, err2
		}
		*sess = ns
		entries, err = w.pageAt(ctx, *sess, skip)
	}
	return entries, err
}

func (w *Worker) pageAt(ctx context.Context, sess lj.Session, skip int) ([]lj.LJEntry, error) {
	if p, ok := w.Source.(interface {
		FriendsPageSkip(context.Context, lj.Session, int) ([]lj.LJEntry, error)
	}); ok {
		return p.FriendsPageSkip(ctx, sess, skip)
	}
	if skip > 0 {
		return nil, nil
	}
	return w.Source.FriendsPage(ctx, sess, time.Time{})
}

func (w *Worker) needsFriends(u store.User, now time.Time) bool {
	if u.FriendsSyncedAt.IsZero() {
		return true
	}
	return now.Sub(u.FriendsSyncedAt) >= w.FriendEvery
}

func (w *Worker) syncFriends(ctx context.Context, u *store.User, sess *lj.Session, pw string, now time.Time) error {
	friends, err := w.Source.FriendList(ctx, *sess)
	if lj.IsUnsupported(err) {
		return w.Store.TouchFriends(ctx, u.ID, now)
	}
	if lj.IsAuth(err) {
		ns, err2 := w.relogin(ctx, u.ID, u.Username, pw)
		if err2 != nil {
			return err2
		}
		*sess = ns
		friends, err = w.Source.FriendList(ctx, *sess)
	}
	if err != nil {
		return err
	}
	if err := w.Store.ReplaceLJFriends(ctx, u.ID, friends, now); err != nil {
		return err
	}
	if groups, ok := sess.Groups(); ok {
		if err := w.Store.ReplaceLJGroups(ctx, u.ID, groups); err != nil {
			return err
		}
	}
	return w.Store.TouchFriends(ctx, u.ID, now)
}

func (w *Worker) relogin(ctx context.Context, userID int64, username, pw string) (lj.Session, error) {
	sess, err := w.Source.Login(ctx, username, pw)
	if err != nil {
		return lj.Session{}, err
	}
	if err := w.Store.SaveSecrets(ctx, userID, pw, sess.Cookie); err != nil {
		return lj.Session{}, err
	}
	return sess, nil
}

func (w *Worker) save(ctx context.Context, viewerID int64, e lj.LJEntry) error {
	if e.ItemID == 0 || e.Journal == "" || e.Author == "" {
		return nil
	}
	body := render.Sanitize(e.EventHTML)
	_, err := w.Store.UpsertLJEntry(ctx, viewerID, store.LJEntryIn{
		ItemID: e.ItemID, Journal: e.Journal, Author: e.Author, URL: e.URL,
		Subject: e.Subject, BodyHTML: body, Security: e.Security, AllowMask: e.AllowMask,
		EventTime: e.EventTime, UserpicURL: e.UserpicURL, Mood: e.Mood, Music: e.Music,
		CommentCount: e.CommentCount, JournalType: e.JournalType,
	})
	if err != nil {
		return err
	}
	if e.UserpicURL != "" {
		w.enqueueImage(e.UserpicURL)
	}
	for _, raw := range htmlImageURLs(body) {
		w.enqueueImage(raw)
	}
	return nil
}

var htmlSrc = regexp.MustCompile(`(?i)\bsrc\s*=\s*"([^"]+)"`)

func htmlImageURLs(body string) []string {
	var out []string
	for _, m := range htmlSrc.FindAllStringSubmatch(body, -1) {
		u := m[1]
		if len(u) > 8 && (u[:7] == "http://" || u[:8] == "https://") {
			out = append(out, u)
		}
	}
	return out
}

func (w *Worker) enqueueImage(raw string) {
	if w == nil || w.Images == nil || raw == "" {
		return
	}
	select {
	case w.Images <- raw:
	default:
	}
}

func (w *Worker) finish(ctx context.Context, u store.User, pw, cookie string, err error, now time.Time) error {
	msg := lj.SafeMessage(err, pw, cookie)
	switch {
	case lj.IsAuth(err):
		return w.Store.MarkAuthFailed(ctx, u.ID, msg)
	case lj.IsBlocked(err):
		if w.Pauses != nil && w.APIHost != "" {
			w.Pauses.PauseHost(w.APIHost, w.BlockFor, "blocked")
		}
		return w.Store.MarkBlocked(ctx, u.ID, msg, now.Add(w.BlockFor))
	default:
		return w.Store.MarkSyncError(ctx, u.ID, msg, u.FailCount+1, now.Add(Backoff(w.Interval, u.FailCount+1, w.randFloat())))
	}
}
