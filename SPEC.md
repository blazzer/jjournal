# Journal

Private LiveJournal-style site for about 5–30 people, plus an LJ read bridge.
This file is the build spec for an unattended implementation. Build milestones 0 through 6.
Do not stop after the probe.

## Operating notes for the unattended run

- The workspace starts empty except for this file.
- `LJ_USER` and `LJ_PASSWORD` are not set. Implement `cmd/ljprobe` and unit tests with fixtures.
  If those env vars are present when you run the probe, run it and write `./probe-out/`.
  If they are absent, write `probe-out/report.txt` saying the live probe was skipped, and continue.
  Do not invent credentials. Do not block the rest of the build on a live LJ call.
- Prefer XML-RPC as the primary `LJSource`. Keep digest RSS and HTML scrape implemented behind the same interface, selected by config, fallback order XML-RPC then digest RSS then scrape.
- `probe-out/` is gitignored. Never print or store a plaintext LJ password.
- Do not commit unless a later instruction says to.
- Do not use LiveJournal's logo, name as the product name, or copied images. Site name comes from config. Default site name: `Journal`.
- `go test ./...` must pass before you finish.
- Original assets only. Minimal CSS, no framework, no SPA, no infinite scroll. Every page is server-rendered HTML that works without JavaScript.

## Goal

Users log in with their LiveJournal username and password. Their handle on this site is their LJ username.
Their friends page is filled from their real LJ friends page, synced in the background, and merged with native posts from friends who have moved to this site.
LJ is read-only. Never post or comment to LJ. For LJ entries, comment links go to the original post on livejournal.com.

## Stack

- Go (latest stable), standard library `net/http`, `html/template`
- SQLite via `modernc.org/sqlite` (pure Go, no CGO), migrations as embedded SQL files
- HTML sanitizing: `github.com/microcosm-cc/bluemonday`
- HTML parse for the scrape fallback: `golang.org/x/net/html`
- Single binary. Config from env vars: `DB_PATH`, `LISTEN_ADDR`, `SECRET_KEY` (32-byte base64, used for AES-GCM and cookie signing), `BASE_URL`, `SITE_NAME` (default `Journal`), `LJ_SOURCE` (`xmlrpc`, `digest`, `scrape`; default `xmlrpc`)
- Dockerfile + docker-compose for deployment behind a reverse proxy (Caddy) with TLS
- Tests with the standard `testing` package. LJ responses are recorded as fixtures.

## LJSource

```go
type LJSource interface {
    Login(ctx context.Context, user string, pwMD5 string) (Session, error)
    FriendList(ctx context.Context, s Session) ([]LJFriend, error)
    FriendsPage(ctx context.Context, s Session, since time.Time) ([]LJEntry, error)
}
```

Auth response is `md5(challenge + pwMD5)` with `auth_method=challenge`. `Login` receives the hex MD5 of the password, never the plaintext.

XML-RPC endpoint: `https://www.livejournal.com/interface/xmlrpc`

- `LJ.XMLRPC.getchallenge`
- `LJ.XMLRPC.login` with `ver=1`
- `LJ.XMLRPC.getfriends`
- `LJ.XMLRPC.getfriendspage` (item limit and whether friends-locked entries appear must be recorded by the probe)
- `LJ.XMLRPC.sessiongenerate` for an `ljsession` cookie

Digest: `https://<user>.livejournal.com/data/rss?auth=digest` with HTTP digest auth and `qop=auth`. This is one journal's feed, not an aggregated friends page. Digest `FriendList` returns a typed unsupported error. The probe compares an anonymous fetch with an authenticated fetch.

Scrape fallback: `https://<user>.livejournal.com/friends` and `?skip=N`, using the session cookie. Scrape `FriendList` returns unsupported.

`cmd/ljprobe` reads `LJ_USER` and `LJ_PASSWORD`, optional `-out` (default `./probe-out`). One LJ request at a time. Stop that channel on HTTP 403/429 or a captcha page and record `blocked`. Exit 0 if any method returns entries, 2 if all fail, 1 on usage errors. Never print the password or the session cookie. Write the cookie only to `probe-out/ljsession.txt`.

## Data model (SQLite)

- `users`: id, lj_username (unique, lowercase), display_name, lj_pw_md5_enc (AES-GCM), lj_session_enc, migrated_at (nullable), sync_status (`ok` | `auth_failed` | `blocked` | `error`), sync_error, last_synced_at, created_at
- `lj_friends`: user_id, friend_lj_username, groupmask, synced_at
- `native_friends`: user_id, friend_user_id
- `friend_groups`: id, user_id, name, bit (up to 30)
- `entries`: id, source (`lj` | `native`), author_lj_username, journal_lj_username, lj_itemid, lj_url, subject, body_html (sanitized at write time), security (`public` | `friends` | `private` | `custom`), allowmask, event_time, userpic_url, mood, music, lj_comment_count, created_at, updated_at. Unique on (source, journal_lj_username, lj_itemid) for LJ entries.
- `entry_visibility`: entry_id, viewer_user_id. An LJ locked entry is shown only to viewers whose own sync returned it.
- `comments`: id, entry_id (native only), parent_id, author_user_id, body_html, created_at, deleted
- `sessions`: id, user_id, expires_at

## Auth

Login form takes LJ username and password. Authenticate with `LJSource.Login`. On success, create or update the user, store the encrypted md5, start an HttpOnly Secure SameSite=Lax signed session cookie.
CSRF tokens on all POST forms.
If LJ auth fails during sync, set `sync_status=auth_failed` and show a banner asking the user to log in again.

## Sync worker

In-process goroutine pool. Each user syncs every 20 minutes with plus or minus 5 minutes of jitter. At most 2 concurrent LJ requests. Exponential backoff. Reuse the LJ session. Log in again only on an auth error.
Each run: refresh the friend list at most daily, fetch friends-page entries since `last_synced_at`, upsert entries, record `entry_visibility`, download and cache userpics.
Captcha, block, or challenge pages (HTTP 403/429, or HTML containing a captcha form): set `sync_status=blocked` and stop polling that user for 6 hours.
Admin page (first user is admin) lists each user's sync status and last error.

## Reading page

`/~{username}/friends?skip=N`, 20 entries per page, "Previous 20" / "Next 20" links. Built from the local DB only.

- LJ entries in `entry_visibility` for this viewer. Exclude entries by an author whose local account has `migrated_at` set when `event_time >= migrated_at`.
- Native entries whose author is in the viewer's friend set (`lj_friends` union `native_friends` mapped to local users) and which the viewer may see under security and allowmask rules.
- Order by `event_time` DESC, stable secondary sort on id.
- `?filter=<groupname>` uses the viewer's LJ friend groups and native groups.
- Each entry shows userpic, author, "in community X" for community posts, subject, time, security icon, mood, music, body.
- LJ entries: "N comments on LJ" link to `lj_url`, and a "via LJ" marker.
- Native entries: "N comments" / "Leave a comment" links to local threaded comments.

## Native journaling

- `/~{username}/` own journal, same paging
- `/~{username}/{id}.html` entry page, threaded comments, LJ-style nesting and indent, reply links, collapse deep threads
- `/update` post editor: subject, HTML body, security (public/friends/private/custom groups), mood, music, userpic, cut tag. The first native post sets `migrated_at`.
- `/manage/friends` add or remove native friends and groups. LJ friends are read-only.
- `/~{username}/profile`

## Content handling

Sanitize HTML with a bluemonday policy close to LJ's allowed tags. Strip scripts, event handlers, iframes except an allowlist of YouTube and Vimeo, and inline styles that affect layout outside the entry.
Rewrite `<lj user="x">` to the local profile if x is a local user, otherwise to `x.livejournal.com`, with a local user-head icon.
`<lj-cut>` on the reading page becomes "Read more" (to `lj_url` for LJ entries, the local entry page for native entries). Full text on the entry page.
Image proxy `/img?u=<signed url>`: fetch, cache on disk, serve. Only proxy URLs this site signed.

## UI

Classic mid-2000s narrow centered column, sidebar with userpic and nav, simple type hierarchy, paged navigation, small original icons for security and user heads.
Light theme, dark theme via `prefers-color-scheme`. Works on mobile widths.

## Non-goals

Posting or commenting to LJ, importing an LJ archive, native communities, search, notifications, email, S2-style custom layouts.

## Packages

`lj`, `store`, `sync`, `web`, `render`. Tests for every non-trivial function, including visibility, dedupe, paging, and hostile HTML fixtures.

## Milestone 6 deliverables

Admin sync-status page, Dockerfile, compose, Caddyfile, README with deploy steps.
